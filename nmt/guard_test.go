// SPDX-License-Identifier: MIT

package nmt

import (
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestNodeGuarding(t *testing.T) {
	ports := testbus.Ports(t, 3)
	mev, sev := newEvents[GuardEvent](), newEvents[GuardEvent]()
	slave, _ := NewSlave(ports[1], 4)
	_ = slave.Start()
	defer func() { _ = slave.Close() }()

	r, err := NewGuardResponder(ports[1], 4, slave.State, WithLifeGuarding(20*time.Millisecond, 3), WithGuardHandler(sev.add))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	sub := subscribe(t, ports[2])

	g := NewGuardMaster(ports[0], WithGuardHandler(mev.add))
	if err := g.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if err := g.Guard(4, 20*time.Millisecond, 3); err != nil {
		t.Fatal(err)
	}
	e := mev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind != GuardBootUp }) // the slave's boot-up may still be in flight
	if e.Kind != GuardStateChange || e.Node != 4 || e.State != canopen.StatePreOperational {
		t.Fatalf("event %+v", e)
	}
	// Responses toggle.
	var toggles []bool
	for len(toggles) < 3 {
		f := recv(t, sub, func(f *canbus.Frame) bool {
			return f.ID == 0x704 && f.Flags&canbus.FlagRemote == 0 && f.Data[0]&0x7F != 0
		})
		toggles = append(toggles, f.Data[0]&0x80 != 0)
	}
	if toggles[0] == toggles[1] || toggles[1] == toggles[2] {
		t.Errorf("toggles %v", toggles)
	}
	_ = slave.SetState(canopen.StateOperational)
	mev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardStateChange && e.State == canopen.StateOperational })
	if g.State(4) != canopen.StateOperational || g.State(5) != canopen.StateUnknown {
		t.Error(g.State(4))
	}

	// The responder stops: node guarding event once, then resumed.
	_ = r.Close()
	e = mev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardTimeout })
	if e.Node != 4 || e.State != canopen.StateUnknown {
		t.Errorf("timeout %+v", e)
	}
	mev.none(t, 100*time.Millisecond, func(e GuardEvent) bool { return e.Kind == GuardTimeout })
	r2, _ := NewGuardResponder(ports[1], 4, slave.State, WithLifeGuarding(20*time.Millisecond, 3), WithGuardHandler(sev.add))
	_ = r2.Start()
	defer func() { _ = r2.Close() }()
	mev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardResumed })

	// The master stops: life guarding event on the responder side.
	_ = g.Guard(4, 0, 0)
	sev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardTimeout && e.Node == 4 })
	sev.none(t, 100*time.Millisecond, func(e GuardEvent) bool { return e.Kind == GuardTimeout })
	_ = g.Guard(4, 20*time.Millisecond, 3)
	sev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardResumed && e.Node == 4 })
	if r2.Stats().Timeouts != 1 || r2.Stats().Requests == 0 {
		t.Errorf("responder stats %+v", r2.Stats())
	}
	st := g.Stats()
	if st.Timeouts != 1 || st.Requests == 0 || st.Responses == 0 || st.Missed == 0 {
		t.Errorf("master stats %+v", st)
	}
	if err := g.Guard(0, time.Second, 1); err == nil {
		t.Error("node 0 accepted")
	}
	if err := g.Guard(1, -time.Second, 1); err == nil {
		t.Error("negative guard time accepted")
	}
}

func TestGuardMasterToggleAndBootUp(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[GuardEvent]()
	g := NewGuardMaster(a, WithGuardHandler(ev.add))
	_ = g.Start()
	defer func() { _ = g.Close() }()
	sub := subscribe(t, b, canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x706, Mask: 0x7FF, Format: canbus.FormatStandard, Remote: canbus.RemoteOnly}}}))
	_ = g.Guard(6, 30*time.Millisecond, 1)
	answer := func(st canopen.NMTState, toggle bool) {
		recv(t, sub, nil)
		send(t, b, ErrorControlFrame(6, st, toggle))
	}
	answer(canopen.StateOperational, true) // first response: any toggle
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardStateChange })
	answer(canopen.StateOperational, true) // not alternated
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardToggleError })
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardTimeout }) // factor 1: one miss is a loss
	answer(canopen.StateOperational, false)
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardResumed })
	// A boot-up message restarts the toggle at 0.
	send(t, b, ErrorControlFrame(6, canopen.StateInitialising, false))
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardBootUp })
	answer(canopen.StatePreOperational, false)
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardStateChange && e.State == canopen.StatePreOperational })
	// Unsolicited and malformed frames are ignored.
	send(t, b, ErrorControlFrame(6, canopen.StatePreOperational, true))
	send(t, b, ErrorControlFrame(9, canopen.StatePreOperational, true))
	f, _ := canbus.NewStandard(0x706, []byte{1, 2})
	send(t, b, f)
	if g.Stats().ToggleErrors != 1 {
		t.Errorf("stats %+v", g.Stats())
	}
	if err := g.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
}

func TestGuardResponderDictionary(t *testing.T) {
	a, b := testbus.Pair(t)
	d := newDict(t, 2)
	_ = d.SetUint(od.IndexGuardTime, 0, 0)
	ev := newEvents[GuardEvent]()
	var st = canopen.StateInitialising
	r, err := NewGuardResponder(a, 2, func() canopen.NMTState { return st }, WithDictionary(d), WithGuardHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Start()
	defer func() { _ = r.Close() }()
	sub := subscribe(t, b, canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{ID: 0x702, Mask: 0x7FF, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly}}}))
	// Initialising: no answer.
	send(t, b, GuardRequest(2))
	eventually(t, time.Second, func() bool { return true })
	// Life guarding from 0x100C/0x100D written by SDO.
	_ = d.Write(od.IndexGuardTime, 0, []byte{10, 0})
	_ = d.Write(od.IndexLifeTimeFactor, 0, []byte{2})
	ev.wait(t, time.Second, func(e GuardEvent) bool { return e.Kind == GuardTimeout })
	if err := r.SetNodeID(3); err != nil {
		t.Fatal(err)
	}
	if err := r.SetNodeID(0); err == nil {
		t.Error("node 0 accepted")
	}
	r.ResetToggle()
	send(t, b, GuardRequest(2)) // old node-ID: ignored
	r.SetLifeGuarding(0, 0)
	ev.none(t, 50*time.Millisecond, func(GuardEvent) bool { return true })
	if r.Stats().Requests != 0 {
		t.Error(r.Stats())
	}
	_ = sub

	if _, err := NewGuardResponder(a, 2, constState(0), WithDictionary(od.New(2))); err == nil {
		t.Error("dictionary without 0x100C accepted")
	}
	d2 := od.New(2)
	_ = d2.Add(od.NewVar(od.IndexGuardTime, "gt", canopen.Unsigned16, od.AccessRW, nil))
	if _, err := NewGuardResponder(a, 2, constState(0), WithDictionary(d2)); err == nil {
		t.Error("dictionary without 0x100D accepted")
	}
	if _, err := NewGuardResponder(a, 0, constState(0)); err == nil {
		t.Error("node 0 accepted")
	}
	if _, err := NewGuardResponder(a, 1, nil); err == nil {
		t.Error("nil state accepted")
	}
}
