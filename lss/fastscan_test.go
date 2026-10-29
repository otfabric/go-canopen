// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

func addresses(ss []*Slave) []Address {
	out := make([]Address, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Address())
	}
	slices.SortFunc(out, compareAddress)
	return out
}

func compareAddress(a, b Address) int {
	for p := PartVendor; p <= PartSerial; p++ {
		if a.Part(p) != b.Part(p) {
			if a.Part(p) < b.Part(p) {
				return -1
			}
			return 1
		}
	}
	return 0
}

func TestFastscanList(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	ports := testbus.Ports(t, 5)
	m := startMaster(t, ports[0])
	ss := slaves(t, 1, ports[1:4])
	// A configured slave does not take part.
	startSlave(t, ports[4], Address{Vendor: 1, Product: 1, Revision: 1, Serial: 1}, 9)

	got, err := m.Fastscan(ctx, FastscanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := addresses(ss); !slices.Equal(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for _, s := range ss {
		if s.State() != Waiting {
			t.Fatal("fastscan list selected a slave")
		}
	}
	got, err = m.Fastscan(ctx, FastscanOptions{Max: 1, Known: map[Part]uint32{PartVendor: 0x29C, PartProduct: 0x1234}})
	if err != nil || len(got) != 1 || got[0] != addresses(ss)[0] {
		t.Fatalf("%v %v", got, err)
	}
	got, err = m.Fastscan(ctx, FastscanOptions{Known: map[Part]uint32{PartVendor: 0x29D}})
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestFastscanNone(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0])
	startSlave(t, ports[1], testAddr, 4)
	got, err := m.Fastscan(ctx, FastscanOptions{Retries: -1})
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, ok, err := m.FastscanSelect(ctx, FastscanOptions{}); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	as, err := m.AssignAll(ctx, 1)
	if err != nil || len(as) != 0 {
		t.Fatalf("%v %v", as, err)
	}
}

func TestFastscanSelect(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	m, ss := network(t, 2, 3)
	a, ok, err := m.FastscanSelect(ctx, FastscanOptions{Timeout: testWindow})
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if want := addresses(ss)[0]; a != want {
		t.Fatalf("selected %v, want the lowest %v", a, want)
	}
	for _, s := range ss {
		if (s.State() == Configuration) != (s.Address() == a) {
			t.Fatalf("%v in %v", s.Address(), s.State())
		}
	}
	got, err := m.InquireAddress(ctx)
	if err != nil || got != a {
		t.Fatalf("%v %v", got, err)
	}
	// With a known serial number only that slave is selected.
	if err := m.SwitchStateGlobal(ctx, Waiting); err != nil {
		t.Fatal(err)
	}
	last := addresses(ss)[2]
	a, ok, err = m.FastscanSelect(ctx, FastscanOptions{Known: map[Part]uint32{PartSerial: last.Serial, PartRevision: last.Revision}})
	if err != nil || !ok || a != last {
		t.Fatalf("%v %v %v", a, ok, err)
	}
}

func TestAssignAll(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	m, ss := network(t, 3, 4)
	var seen []Assignment
	var stored int
	for _, s := range ss {
		s.cfg.store = func(Settings) error { stored++; return nil }
	}
	as, err := m.AssignAll(ctx, 10, AssignAvoid(11), AssignStore(), AssignHandler(func(a Assignment) { seen = append(seen, a) }),
		AssignFastscan(FastscanOptions{Max: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 4 || !slices.Equal(as, seen) {
		t.Fatalf("%v, handler %v", as, seen)
	}
	wantIDs := []canopen.NodeID{10, 12, 13, 14}
	byAddr := map[Address]canopen.NodeID{}
	for i, a := range as {
		if a.NodeID != wantIDs[i] {
			t.Fatalf("assignment %d: %v", i, a)
		}
		byAddr[a.Address] = a.NodeID
	}
	for _, s := range ss {
		if s.NodeID() != byAddr[s.Address()] || s.State() != Waiting {
			t.Fatalf("%v: node %d, state %v", s.Address(), s.NodeID(), s.State())
		}
	}
	if stored != 4 || m.Stats().Assigned != 4 {
		t.Fatalf("stored %d, %+v", stored, m.Stats())
	}
	// Nothing is left.
	if ok, err := m.IdentifyNonConfigured(ctx); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestAssignAllLimits(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	m, ss := network(t, 4, 3)
	as, err := m.AssignAll(ctx, 126, AssignMax(1))
	if err != nil || len(as) != 1 || as[0].NodeID != 126 {
		t.Fatalf("%v %v", as, err)
	}
	as, err = m.AssignAll(ctx, 127, nil)
	if !errors.Is(err, ErrNoNodeID) || len(as) != 1 || as[0].NodeID != 127 {
		t.Fatalf("%v %v", as, err)
	}
	if _, err := m.AssignAll(ctx, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	// Store refused: the run stops, the node-ID is used up.
	for _, s := range ss {
		s.cfg.store = func(Settings) error { return errors.New("flash") }
	}
	as, err = m.AssignAll(ctx, 20, AssignStore())
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Code != CodeStoreFailed || len(as) != 0 {
		t.Fatalf("%v %v", as, err)
	}
}

func TestAssignAllConflict(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0], WithRetries(0))
	// A slave that is stuck in the configuration state (it never leaves
	// it) makes AssignAll refuse to go on.
	stuck := startSlave(t, ports[1], testAddr, 5)
	stuck.cfg.state = func(st State) {
		if st == Waiting {
			stuck.mu.Lock()
			stuck.state = Configuration
			stuck.mu.Unlock()
		}
	}
	slaves(t, 5, ports[2:])
	if err := m.SwitchStateGlobal(ctx, Configuration); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return stuck.State() == Configuration })
	_, err := m.AssignAll(ctx, 1)
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if m.Stats().Conflicts == 0 {
		t.Fatal("not counted")
	}
}

func TestCheckSelected(t *testing.T) {
	t.Parallel() // no leak check: goroutine counts are global
	ctx := ctxT(t)
	m, ss := network(t, 6, 2)
	// Both slaves in the configuration state: the check fails.
	if err := m.SwitchStateGlobal(ctx, Configuration); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return ss[0].State() == Configuration && ss[1].State() == Configuration })
	if ok, err := m.checkSelected(ctx, FastscanOptions{}, ss[0].Address()); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if err := m.SwitchStateGlobal(ctx, Waiting); err != nil {
		t.Fatal(err)
	}
	// None: the check fails too.
	if ok, err := m.checkSelected(ctx, FastscanOptions{}, ss[0].Address()); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if err := m.SwitchStateSelective(ctx, ss[1].Address()); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.checkSelected(ctx, FastscanOptions{}, ss[1].Address()); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}

// responder answers master frames with reply (when it returns ok) until
// the test ends.
func responder(t *testing.T, bus *canbus.Bus, reply func(f canbus.Frame) (canbus.Frame, bool)) {
	t.Helper()
	sub, err := bus.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-sub.Done():
				return
			case f := <-sub.Frames():
				if r, ok := reply(f); ok {
					_ = bus.Send(context.Background(), &r)
				}
			}
		}
	}()
	t.Cleanup(func() { _ = sub.Close(); <-done })
}

// noVerify behaves as an unconfigured slave with testAddr in Fastscan,
// except that it never acknowledges a verification (a query that would
// move it to another part).
func noVerify(f canbus.Frame) (canbus.Frame, bool) {
	if Command(f.Data[0]) != CmdFastscan {
		return canbus.Frame{}, false
	}
	ack := frame(SlaveID, CmdIdentifySlave)
	bit, sub, next := f.Data[5], Part(f.Data[6]), Part(f.Data[7])
	if bit == fastscanConfirm {
		return ack, true
	}
	if bit > 31 || sub > PartSerial || next != sub {
		return canbus.Frame{}, false
	}
	mask := ^uint32(0) << bit
	return ack, (u32(&f)^testAddr.Part(sub))&mask == 0
}

func TestFastscanVerifyLost(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0], WithFastscanRetries(-1))
	responder(t, ports[1], noVerify)
	got, err := m.Fastscan(ctx, FastscanOptions{Known: map[Part]uint32{PartVendor: testAddr.Vendor}})
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, _, err := m.FastscanSelect(ctx, FastscanOptions{}); !errors.Is(err, ErrFastscan) {
		t.Fatal(err)
	}
	if _, err := m.AssignAll(ctx, 1); !errors.Is(err, ErrFastscan) {
		t.Fatal(err)
	}
	if m.Stats().Restarts == 0 {
		t.Fatalf("%+v", m.Stats())
	}
}

func TestFastscanOnlyConfirm(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0], WithFastscanRetries(0))
	responder(t, ports[1], func(f canbus.Frame) (canbus.Frame, bool) {
		return frame(SlaveID, CmdIdentifySlave), Command(f.Data[0]) == CmdFastscan && f.Data[5] == fastscanConfirm
	})
	if _, _, err := m.FastscanSelect(ctx, FastscanOptions{}); !errors.Is(err, ErrFastscan) {
		t.Fatal(err)
	}
}

func TestAssignAllWrongIdentity(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0], WithRetries(0))
	s := startSlave(t, ports[1], testAddr, Unconfigured)
	// Another device answers every identity inquiry with a wrong value.
	responder(t, ports[2], func(f canbus.Frame) (canbus.Frame, bool) {
		cs := Command(f.Data[0])
		return frame32(SlaveID, cs, 0), cs >= CmdInquireVendor && cs <= CmdInquireSerial
	})
	_, err := m.AssignAll(ctx, 1)
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if s.NodeID() != Unconfigured || s.Pending().NodeID != Unconfigured {
		t.Fatal("assigned despite the conflict")
	}
}

func TestAssignAllSetNodeIDFails(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0], WithRetries(0))
	startSlave(t, ports[1], testAddr, Unconfigured, WithNodeIDHandler(func(canopen.NodeID) error { return errors.New("no") }))
	_, err := m.AssignAll(ctx, 7)
	var ae *AssignError
	var ce *ConfigError
	if !errors.As(err, &ae) || ae.NodeID != 7 || ae.Address != testAddr || !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if !strings.Contains(ae.Error(), "node-ID 7") {
		t.Fatal(ae.Error())
	}
}
