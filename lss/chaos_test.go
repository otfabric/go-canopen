// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
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
	for seed := int64(1); seed <= int64(iterations(2)); seed++ {
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
