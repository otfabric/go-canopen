// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
)

// iterations returns the number of chaos seeds: def, or
// CANOPEN_CHAOS_ITERATIONS (make chaos).
func iterations(def int) int {
	if n, err := strconv.Atoi(os.Getenv("CANOPEN_CHAOS_ITERATIONS")); err == nil && n > 0 {
		return n
	}
	return def
}

// chaosNetwork puts a master on one virtual bus and n slave ports on
// another, joined by a chaos bridge with cfg in both directions (a lost
// master frame is lost for every slave, as on a CAN bus where the
// sender's frame is either received by all or retransmitted).
func chaosNetwork(t *testing.T, cfg chaos.Config, n int) (*canbus.Bus, []*canbus.Bus, *chaos.Bridge) {
	t.Helper()
	va := virtual.New(virtual.Config{Name: "lss-master"})
	vb := virtual.New(virtual.Config{Name: "lss-slaves"})
	t.Cleanup(func() { _ = va.Close(); _ = vb.Close() })
	open := func(v *virtual.Bus) canbus.Transport {
		p, err := v.Open()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ia, ib := open(va), open(vb)
	br, err := chaos.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = br.Both(ctx, ia, ib) }()
	t.Cleanup(func() { cancel(); <-done })
	bus := func(v *virtual.Bus) *canbus.Bus {
		b, err := canbus.NewBus(open(v))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		return b
	}
	m := bus(va)
	out := make([]*canbus.Bus, n)
	for i := range out {
		out[i] = bus(vb)
	}
	return m, out, br
}

// TestMonkeyAssignAll assigns node-IDs to unconfigured slaves through a
// lossy, duplicating link: runs may fail (and are repeated with fresh
// node-IDs) but no two slaves ever end up with the same node-ID, and
// every slave gets one.
func TestMonkeyAssignAll(t *testing.T) {
	checkLeaks(t)
	// Seeds 6 and 100 found a slave left in the configuration state
	// (TestAssignAllLostSelectionAck).
	seeds := []int64{6, 100}
	for seed := int64(1); seed <= int64(iterations(2)); seed++ {
		if !slices.Contains(seeds, seed) {
			seeds = append(seeds, seed)
		}
	}
	for _, seed := range seeds {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			ctx := ctxT(t)
			mb, sb, br := chaosNetwork(t, chaos.Config{Seed: seed, Drop: 0.08, Duplicate: 0.05}, 3)
			m := startMaster(t, mb, WithTimeout(30*time.Millisecond), WithRetries(3), WithFastscanTimeout(6*time.Millisecond))
			ss := slaves(t, seed, sb)

			assigned := map[Address]canopen.NodeID{}
			first := canopen.NodeID(1)
			for run := 0; run < 10 && len(assigned) < len(ss); run++ {
				as, err := m.AssignAll(ctx, first, AssignFastscan(FastscanOptions{Retries: 2}))
				for _, a := range as {
					if _, dup := assigned[a.Address]; dup {
						t.Fatalf("%v assigned twice", a.Address)
					}
					assigned[a.Address] = a.NodeID
					first = a.NodeID + 1
				}
				var ae *AssignError
				if errors.As(err, &ae) {
					first = ae.NodeID + 1 // possibly taken
				}
				if err != nil {
					t.Logf("run %d: %v", run, err)
				}
			}
			// Let the last switch to waiting settle, then compare.
			eventually(t, 2*time.Second, func() bool {
				for _, s := range ss {
					if s.NodeID() == Unconfigured {
						return false
					}
				}
				return true
			})
			seen := map[canopen.NodeID]Address{}
			for _, s := range ss {
				if prev, dup := seen[s.NodeID()]; dup {
					t.Fatalf("node-ID %d on %v and %v", s.NodeID(), prev, s.Address())
				}
				seen[s.NodeID()] = s.Address()
				if id, ok := assigned[s.Address()]; ok && id != s.NodeID() {
					t.Fatalf("%v reported %d, has %d", s.Address(), id, s.NodeID())
				}
			}
			st := br.Stats()
			if st.Dropped == 0 {
				t.Fatal("no faults injected")
			}
			t.Logf("bridge %+v, master %+v", st, m.Stats())
		})
	}
}

// filteredNetwork is [chaosNetwork] with a scripted link: a frame of a
// slave is lost when drop says so, given the master's last frame.
func filteredNetwork(t *testing.T, n int, drop func(f, request *canbus.Frame) bool) (*canbus.Bus, []*canbus.Bus) {
	t.Helper()
	va := virtual.New(virtual.Config{Name: "lss-master"})
	vb := virtual.New(virtual.Config{Name: "lss-slaves"})
	t.Cleanup(func() { _ = va.Close(); _ = vb.Close() })
	bus := func(v *virtual.Bus) *canbus.Bus {
		p, err := v.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := canbus.NewBus(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		return b
	}
	ma, sa := bus(va), bus(vb)
	var mu sync.Mutex
	var request canbus.Frame
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	forward := func(from, to *canbus.Bus, pass func(f *canbus.Frame) bool) {
		sub, err := from.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = sub.Close() }()
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				if pass(&f) {
					out := f
					out.Timestamp = time.Time{}
					_ = to.Send(ctx, &out)
				}
			}
		}()
	}
	forward(ma, sa, func(f *canbus.Frame) bool {
		mu.Lock()
		request = *f
		mu.Unlock()
		return true
	})
	forward(sa, ma, func(f *canbus.Frame) bool {
		mu.Lock()
		defer mu.Unlock()
		return !drop(f, &request)
	})
	t.Cleanup(func() { cancel(); wg.Wait() })
	m := bus(va)
	out := make([]*canbus.Bus, n)
	for i := range out {
		out[i] = bus(vb)
	}
	return m, out
}

// TestAssignAllLostSelectionAck is the sequence that TestMonkeyAssignAll
// seeds 6 and 100 found: the acknowledgement of the Fastscan step that
// selects a slave is lost, so the selection restarts with that slave
// still in the configuration state. When its answer to the check of the
// next selection is lost as well, the configure node-ID request reaches
// both slaves. The slave must leave the configuration state at the
// restart.
func TestAssignAllLostSelectionAck(t *testing.T) {
	checkLeaks(t)
	ack, answer := false, false
	first := Address{Vendor: 1, Product: 2, Revision: 3, Serial: 4}
	mb, sb := filteredNetwork(t, 2, func(f, req *canbus.Frame) bool {
		selecting := Command(req.Data[0]) == CmdFastscan && req.Data[5] == 0 && Part(req.Data[6]) == PartSerial && Part(req.Data[7]) == PartVendor
		switch {
		case !ack && selecting && Command(f.Data[0]) == CmdIdentifySlave:
			ack = true
			return true
		case ack && !answer && Command(f.Data[0]) == CmdInquireSerial && u32(f) == first.Serial:
			answer = true
			return true
		}
		return false
	})
	m := startMaster(t, mb, WithTimeout(30*time.Millisecond), WithFastscanTimeout(6*time.Millisecond))
	a := startSlave(t, sb[0], first, Unconfigured)
	second := first
	second.Serial = 5
	b := startSlave(t, sb[1], second, Unconfigured)
	as, err := m.AssignAll(ctxT(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ack || !answer {
		t.Fatalf("frames lost: acknowledgement %v, answer %v", ack, answer)
	}
	if len(as) != 2 {
		t.Fatalf("assignments %+v", as)
	}
	eventually(t, 2*time.Second, func() bool { return a.NodeID() != Unconfigured && b.NodeID() != Unconfigured })
	if a.NodeID() == b.NodeID() {
		t.Fatalf("both slaves have node-ID %d", a.NodeID())
	}
}

// TestMonkeyFastscanList lists slaves through a lossy link: a lost frame
// may hide a slave, it never invents one.
func TestMonkeyFastscanList(t *testing.T) {
	checkLeaks(t)
	for seed := int64(1); seed <= int64(iterations(1)); seed++ {
		t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
			ctx := ctxT(t)
			mb, sb, _ := chaosNetwork(t, chaos.Config{Seed: seed, Drop: 0.05, Duplicate: 0.05}, 2)
			m := startMaster(t, mb, WithFastscanTimeout(6*time.Millisecond), WithFastscanRetries(2))
			ss := slaves(t, seed+100, sb)
			got, err := m.Fastscan(ctx, FastscanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := addresses(ss)
			for _, a := range got {
				if !slices.Contains(want, a) {
					t.Fatalf("invented %v (slaves %v)", a, want)
				}
			}
			t.Logf("found %d of %d, master %+v", len(got), len(want), m.Stats())
		})
	}
}
