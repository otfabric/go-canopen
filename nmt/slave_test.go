// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func newDict(t *testing.T, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: node, HeartbeatConsumers: 4, NodeGuarding: true, ErrorHistory: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Add(od.NewVar(0x2000, "app", canopen.Unsigned16, od.AccessRW, uint16(7))); err != nil {
		t.Fatal(err)
	}
	return d
}

func isBootUp(node canopen.NodeID) func(*canbus.Frame) bool {
	return func(f *canbus.Frame) bool {
		return f.ID == 0x700+canbus.ID(node) && f.Len == 1 && f.Data[0] == 0
	}
}

func TestSlaveStateMachine(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[StateChange]()
	s, err := NewSlave(a, 5, WithStateHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != canopen.StateInitialising {
		t.Fatal(s.State())
	}
	sub := subscribe(t, b)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recv(t, sub, isBootUp(5))
	if s.State() != canopen.StatePreOperational {
		t.Fatal(s.State())
	}
	ev.wait(t, time.Second, func(c StateChange) bool { return c.To == canopen.StatePreOperational && c.Command == 0 })

	m := NewMaster(b)
	ctx := context.Background()
	for _, tc := range []struct {
		node canopen.NodeID
		cmd  canopen.NMTCommand
		want canopen.NMTState
	}{
		{5, canopen.NMTStart, canopen.StateOperational},
		{0, canopen.NMTStop, canopen.StateStopped},
		{5, canopen.NMTEnterPreOperational, canopen.StatePreOperational},
		{0, canopen.NMTStart, canopen.StateOperational},
	} {
		if err := m.Send(ctx, tc.node, tc.cmd); err != nil {
			t.Fatal(err)
		}
		c := ev.wait(t, time.Second, func(StateChange) bool { return true })
		if c.To != tc.want || c.Command != tc.cmd || c.Node != 5 {
			t.Fatalf("%v: change %+v", tc.cmd, c)
		}
	}
	// Commands for other nodes and malformed frames are ignored.
	_ = m.Send(ctx, 6, canopen.NMTStop)
	f, _ := canbus.NewStandard(0, []byte{0x01})
	send(t, b, f)
	f, _ = canbus.NewStandard(0, []byte{0x03, 5})
	send(t, b, f)
	eventually(t, time.Second, func() bool { st := s.Stats(); return st.Ignored == 1 && st.Invalid == 2 })
	if s.State() != canopen.StateOperational {
		t.Fatal(s.State())
	}
	// Start in Operational changes nothing.
	_ = m.Send(ctx, 5, canopen.NMTStart)
	ev.none(t, 50*time.Millisecond, func(StateChange) bool { return true })

	if err := s.SetState(canopen.StateStopped); err != nil || s.State() != canopen.StateStopped {
		t.Fatal(err, s.State())
	}
	for _, st := range []canopen.NMTState{canopen.StateInitialising, canopen.StateUnknown} {
		if err := s.SetState(st); err == nil {
			t.Errorf("SetState(%v) accepted", st)
		}
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("second Start: %v", err)
	}
	st := s.Stats()
	if st.BootUps != 1 || st.Commands != 5 {
		t.Errorf("stats %+v", st)
	}
	_ = s.Close()
	_ = s.Close()
	if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
	if err := s.Reset(ctx, ResetCommunication); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Reset after Close: %v", err)
	}
}

func TestSlaveReset(t *testing.T) {
	a, b := testbus.Pair(t)
	d := newDict(t, 5)
	var resets []ResetKind
	ev := newEvents[StateChange]()
	s, err := NewSlave(a, 5, WithDictionary(d), WithStateHandler(ev.add), WithResetHandler(func(k ResetKind) {
		resets = append(resets, k)
		if v, _ := d.GetUint(0x2000, 0); k == ResetApplication && v != 7 {
			t.Errorf("application area not restored before the handler: %d", v)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	sub := subscribe(t, b)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recv(t, sub, isBootUp(5))
	m := NewMaster(b)
	ctx := context.Background()

	_ = d.SetUint(0x2000, 0, 99)
	_ = d.SetUint(od.IndexProducerHeartbeat, 0, 1000)
	if err := m.Send(ctx, 5, canopen.NMTResetCommunication); err != nil {
		t.Fatal(err)
	}
	recv(t, sub, isBootUp(5))
	ev.wait(t, time.Second, func(c StateChange) bool {
		return c.To == canopen.StateInitialising && c.Command == canopen.NMTResetCommunication
	})
	ev.wait(t, time.Second, func(c StateChange) bool { return c.To == canopen.StatePreOperational })
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 0 {
		t.Errorf("0x1017 = %d after reset communication", v)
	}
	if v, _ := d.GetUint(0x2000, 0); v != 99 {
		t.Errorf("0x2000 = %d after reset communication", v)
	}

	// Reset node with a pending node-ID: application area restored, new
	// node-ID in the boot-up and in the node-ID-relative defaults.
	if err := s.SetNodeID(9); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeID(0); err == nil {
		t.Error("node 0 accepted")
	}
	if err := m.Send(ctx, 0, canopen.NMTResetNode); err != nil {
		t.Fatal(err)
	}
	recv(t, sub, isBootUp(9))
	eventually(t, time.Second, func() bool { return s.State() == canopen.StatePreOperational })
	if v, _ := d.GetUint(0x2000, 0); v != 7 {
		t.Errorf("0x2000 = %d after reset node", v)
	}
	if v, _ := d.GetUint(od.IndexCOBIDEmcy, 0); v != 0x89 {
		t.Errorf("0x1014 = 0x%X after node-ID change", v)
	}
	if s.NodeID() != 9 {
		t.Error(s.NodeID())
	}
	// Commands for the old node-ID are ignored now.
	_ = m.Send(ctx, 5, canopen.NMTStart)
	eventually(t, time.Second, func() bool { return s.Stats().Ignored == 1 })

	if err := s.Reset(ctx, ResetApplication); err != nil {
		t.Fatal(err)
	}
	recv(t, sub, isBootUp(9))
	if err := s.Reset(ctx, 7); err == nil {
		t.Error("invalid reset kind accepted")
	}
	want := []ResetKind{ResetCommunication, ResetApplication, ResetApplication}
	if len(resets) != len(want) {
		t.Fatalf("resets %v", resets)
	}
	for i := range want {
		if resets[i] != want[i] {
			t.Fatalf("resets %v", resets)
		}
	}
	if st := s.Stats(); st.Resets != 3 || st.BootUps != 4 {
		t.Errorf("stats %+v", st)
	}
}

func TestSlaveErrors(t *testing.T) {
	a, _ := testbus.Pair(t)
	if _, err := NewSlave(a, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Error(err)
	}
	s, _ := NewSlave(a, 1)
	_ = a.Close()
	if err := s.Start(); err == nil {
		t.Error("Start on a closed bus")
	}
	if s.State() != canopen.StateInitialising {
		t.Error(s.State())
	}
	// Boot-up fails: the subscription works but the send does not.
	b := newFailBus(testbus.Ports(t, 1)[0])
	s, _ = NewSlave(b, 1)
	if err := s.Start(); err == nil {
		t.Error("Start with a failing send")
	}
	b.fail.Store(false)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	b.fail.Store(true)
	if err := s.Reset(context.Background(), ResetCommunication); err == nil {
		t.Error("Reset with a failing send")
	}
	if s.State() != canopen.StateInitialising {
		t.Error(s.State())
	}
	_ = s.Close()
}

// failBus fails every Send while fail is set.
type failBus struct {
	*canbus.Bus
	fail atomic.Bool
}

func newFailBus(b *canbus.Bus) *failBus {
	f := &failBus{Bus: b}
	f.fail.Store(true)
	return f
}

func (f *failBus) Send(ctx context.Context, fr *canbus.Frame) error {
	if f.fail.Load() {
		return errors.New("send failed")
	}
	return f.Bus.Send(ctx, fr)
}

// TestSlaveBootUpHandler: the handler runs before every boot-up message,
// in Initialising, and after the reset handler.
func TestSlaveBootUpHandler(t *testing.T) {
	a, b := testbus.Pair(t)
	var mu sync.Mutex
	var calls []string
	var s *Slave
	var err error
	s, err = NewSlave(a, 5,
		WithResetHandler(func(ResetKind) { mu.Lock(); calls = append(calls, "reset"); mu.Unlock() }),
		WithBootUpHandler(func() {
			if st := s.State(); st != canopen.StateInitialising {
				t.Errorf("boot-up handler in %v", st)
			}
			mu.Lock()
			calls = append(calls, "boot-up")
			mu.Unlock()
		}))
	if err != nil {
		t.Fatal(err)
	}
	sub := subscribe(t, b)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	recv(t, sub, isBootUp(5))
	if err := NewMaster(b).Send(context.Background(), 5, canopen.NMTResetNode); err != nil {
		t.Fatal(err)
	}
	recv(t, sub, isBootUp(5))
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"boot-up", "reset", "boot-up"}; !slices.Equal(calls, want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
}
