// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

func TestFDFrames(t *testing.T) {
	f := ToFD(CommandFrame(5, canopen.NMTStart), true)
	if !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.ID != 0 || f.Len != 2 {
		t.Fatalf("%+v", f)
	}
	if _, _, err := ParseCommand(&f); !errors.Is(err, canopen.ErrProtocol) {
		t.Fatal("classical parser accepted an FD frame", err)
	}
	cmd, node, err := ParseCommandFD(&f)
	if err != nil || cmd != canopen.NMTStart || node != 5 {
		t.Fatal(cmd, node, err)
	}
	c := CommandFrame(5, canopen.NMTStop)
	if cmd, _, err := ParseCommandFD(&c); err != nil || cmd != canopen.NMTStop {
		t.Fatal(cmd, err)
	}
	h := ToFD(ErrorControlFrame(7, canopen.StateOperational, false), false)
	if _, _, _, err := ParseErrorControl(&h); err == nil {
		t.Fatal("classical parser accepted an FD frame")
	}
	n, st, _, err := ParseErrorControlFD(&h)
	if err != nil || n != 7 || st != canopen.StateOperational {
		t.Fatal(n, st, err)
	}
	// A frame that cannot be CAN FD (invalid length) is returned as is.
	bad := canbus.Frame{ID: 1, Len: 9}
	if g := ToFD(bad, false); g.IsFD() {
		t.Fatal(g)
	}
}

func TestFDSlaveHeartbeat(t *testing.T) {
	ports := testbus.Ports(t, 2, virtual.WithFD())
	a, b := ports[0], ports[1]
	sub := subscribe(t, b)
	s, err := NewSlave(a, 9, WithFD(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	boot := recv(t, sub, isBootUp(9))
	if !boot.IsFD() {
		t.Fatal("boot-up not in a CAN FD frame")
	}
	p, err := NewHeartbeatProducer(a, 9, s.State, WithFD(false), WithHeartbeatPeriod(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	ev := newEvents[HeartbeatEvent]()
	c, err := NewHeartbeatConsumer(b, WithFD(false), WithHeartbeatHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	m := NewMaster(b, WithFD(false))
	if err := m.Send(context.Background(), 9, canopen.NMTStart); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { return s.State() == canopen.StateOperational })
	eventually(t, 2*time.Second, func() bool { return c.State(9) == canopen.StateOperational })
	hb := recv(t, sub, func(f *canbus.Frame) bool { return f.ID == 0x709 && f.Data[0] == byte(canopen.StateOperational) })
	if !hb.IsFD() || hb.Flags&canbus.FlagBRS != 0 {
		t.Fatalf("%+v", hb)
	}
}

func TestSlaveUnconfigured(t *testing.T) {
	a, b := testbus.Pair(t)
	d := newDict(t, Unconfigured)
	ev := newEvents[StateChange]()
	resets := newEvents[ResetKind]()
	s, err := NewSlave(a, Unconfigured, WithDictionary(d), WithStateHandler(ev.add), WithResetHandler(resets.add))
	if err != nil {
		t.Fatal(err)
	}
	sub := subscribe(t, b)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	m := NewMaster(b)
	ctx := ctxT(t)
	// Commands other than resets are ignored; no boot-up.
	if err := m.Send(ctx, 0, canopen.NMTStart); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return s.Stats().Ignored == 1 })
	if s.State() != canopen.StateInitialising {
		t.Fatal(s.State())
	}
	// A reset without a pending node-ID keeps it unconfigured.
	if err := m.Send(ctx, 0, canopen.NMTResetCommunication); err != nil {
		t.Fatal(err)
	}
	resets.wait(t, time.Second, func(k ResetKind) bool { return k == ResetCommunication })
	if s.State() != canopen.StateInitialising || s.NodeID() != Unconfigured || s.Stats().BootUps != 0 {
		t.Fatal(s.State(), s.NodeID(), s.Stats())
	}
	// With a pending node-ID the broadcast reset boots it.
	if err := s.SetNodeID(12); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, 0, canopen.NMTResetCommunication); err != nil {
		t.Fatal(err)
	}
	recv(t, sub, isBootUp(12))
	eventually(t, time.Second, func() bool { return s.State() == canopen.StatePreOperational })
	if s.NodeID() != 12 || d.NodeID() != 12 {
		t.Fatal(s.NodeID(), d.NodeID())
	}
	// Back to unconfigured: the reset ends in Initialising.
	if err := s.SetNodeID(Unconfigured); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(ctx, ResetCommunication); err != nil {
		t.Fatal(err)
	}
	if s.State() != canopen.StateInitialising || s.NodeID() != Unconfigured {
		t.Fatal(s.State(), s.NodeID())
	}
	if err := s.SetNodeID(0); err == nil {
		t.Fatal("node-ID 0 accepted")
	}
	if _, err := NewSlave(a, 0); err == nil {
		t.Fatal("node-ID 0 accepted")
	}
}

// TestReceivedTime checks that heartbeat events carry the frame's receive
// timestamp, so that a consumer processing frames late (a starved
// goroutine) reports when they arrived: the CiA 302 manager compares
// boot-up times with the end of its boot procedures.
func TestReceivedTime(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Second)
	if got := received(&canbus.Frame{Timestamp: past}, now); !got.Equal(past) {
		t.Fatal(got)
	}
	if got := received(&canbus.Frame{}, now); !got.Equal(now) {
		t.Fatal("zero timestamp:", got)
	}
	if got := received(&canbus.Frame{Timestamp: now.Add(time.Hour)}, now); !got.Equal(now) {
		t.Fatal("future timestamp:", got)
	}
	c, err := NewHeartbeatConsumer(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := ErrorControlFrame(4, canopen.StateInitialising, false)
	f.Timestamp = past
	evs := c.receive(&f, received(&f, now))
	if len(evs) != 1 || evs[0].Kind != EventBootUp || !evs[0].Time.Equal(past) || !c.Status(4).LastBoot.Equal(past) {
		t.Fatalf("%+v %+v", evs, c.Status(4))
	}
}
