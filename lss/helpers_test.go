// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// testWindow is the Fastscan window of the tests: virtual bus slaves
// answer within microseconds.
const testWindow = 4 * time.Millisecond

func checkLeaks(t testing.TB) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:n])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func ctxT(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// startSlave creates and starts a slave, closed when the test ends.
func startSlave(t testing.TB, bus canopen.Bus, a Address, node canopen.NodeID, opts ...SlaveOption) *Slave {
	t.Helper()
	s, err := NewSlave(bus, a, node, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// startMaster creates and starts a master, closed when the test ends.
func startMaster(t testing.TB, bus canopen.Bus, opts ...MasterOption) *Master {
	t.Helper()
	m := NewMaster(bus, append([]MasterOption{WithTimeout(100 * time.Millisecond), WithFastscanTimeout(testWindow)}, opts...)...)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// network returns a started master and n started unconfigured slaves
// with random serial numbers (vendor and product shared, revisions 1 or
// 2) on one virtual bus.
func network(t testing.TB, seed int64, n int, opts ...MasterOption) (*Master, []*Slave) {
	t.Helper()
	ports := testbus.Ports(t, n+1)
	m := startMaster(t, ports[0], opts...)
	return m, slaves(t, seed, ports[1:])
}

func slaves(t testing.TB, seed int64, buses []*canbus.Bus) []*Slave {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	seen := map[Address]bool{}
	out := make([]*Slave, 0, len(buses))
	for _, b := range buses {
		var a Address
		for {
			a = Address{Vendor: 0x0000029C, Product: 0x1234, Revision: uint32(1 + rng.Intn(2)), Serial: rng.Uint32()}
			if !seen[a] {
				break
			}
		}
		seen[a] = true
		out = append(out, startSlave(t, b, a, Unconfigured))
	}
	return out
}

// masterFrame builds a frame on the master identifier.
func masterFrame(data ...byte) canbus.Frame {
	f := canbus.Frame{ID: MasterID, Len: uint8(len(data))}
	copy(f.Data[:], data)
	return f
}

func eventually(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
