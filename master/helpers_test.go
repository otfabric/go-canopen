// SPDX-License-Identifier: MIT

package master_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// recorder keeps the master's events.
type recorder struct {
	mu  sync.Mutex
	evs []master.Event
}

func (r *recorder) add(e master.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, e)
	r.mu.Unlock()
}

func (r *recorder) find(f func(master.Event) bool) (master.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.evs {
		if f(e) {
			return e, true
		}
	}
	return master.Event{}, false
}

func (r *recorder) all() []master.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]master.Event(nil), r.evs...)
}

func (r *recorder) count(f func(master.Event) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if f(e) {
			n++
		}
	}
	return n
}

func (r *recorder) wait(t testing.TB, what string, f func(master.Event) bool) master.Event {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if ev, ok := r.find(f); ok {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s; events: %v", what, r.all())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitTimeout bounds the waits for events: generous, for CPU-starved runs
// (race detector, -cpu 1, many packages in parallel); a passing test does
// not wait it out.
const waitTimeout = 20 * time.Second

func kind(k master.EventKind, n canopen.NodeID) func(master.Event) bool {
	return func(e master.Event) bool { return e.Kind == k && e.Node == n }
}

type rig struct {
	m     *master.Master
	rec   *recorder
	buses []*canbus.Bus
}

func newRig(t testing.TB, n int, opts ...master.Option) *rig {
	t.Helper()
	return newRigOn(t, testbus.Ports(t, n+1), opts...)
}

func newRigOn(t testing.TB, buses []*canbus.Bus, opts ...master.Option) *rig {
	t.Helper()
	rec := &recorder{}
	opts = append([]master.Option{
		master.WithEventHandler(rec.add),
		master.WithSDOOptions(sdo.WithTimeout(150 * time.Millisecond)),
	}, opts...)
	m, err := master.New(buses[0], opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return &rig{m: m, rec: rec, buses: buses}
}

func startNode(t testing.TB, bus canopen.Bus, d *od.Dictionary, opts ...node.Option) *node.Node {
	t.Helper()
	n, err := node.New(bus, d, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func waitState(t testing.TB, n *node.Node, st canopen.NMTState) {
	t.Helper()
	nettest.Eventually(t, waitTimeout, "node state "+st.String(), func() bool { return n.State() == st })
}

func u16(x uint16) []byte { return []byte{byte(x), byte(x >> 8)} }

func u32(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }

// values collects decoded PDO values.
type values struct {
	mu sync.Mutex
	v  [][]any
}

func (p *values) handler(_ *canbus.Frame, vals []pdo.Value) {
	row := make([]any, len(vals))
	for i, v := range vals {
		row[i] = v.Value
	}
	p.mu.Lock()
	p.v = append(p.v, row)
	p.mu.Unlock()
}

func (p *values) last() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.v) == 0 {
		return nil
	}
	return p.v[len(p.v)-1]
}

func contextWithCancel(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}
