// SPDX-License-Identifier: MIT

package node_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
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

// find returns the first event matching f.
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

func (r *recorder) wait(t *testing.T, what string, f func(master.Event) bool) master.Event {
	t.Helper()
	var ev master.Event
	nettest.Eventually(t, 5*time.Second, what, func() bool {
		var ok bool
		ev, ok = r.find(f)
		return ok
	})
	return ev
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.evs = nil
	r.mu.Unlock()
}

// emcyFrom matches an emergency of a node with a code.
func emcyFrom(n canopen.NodeID, code uint16) func(master.Event) bool {
	return func(e master.Event) bool {
		return e.Kind == master.EventEMCY && e.Node == n && e.EMCY.Code == code
	}
}

type rig struct {
	m     *master.Master
	rec   *recorder
	buses []*canbus.Bus // buses[0] is the master's
}

// newRig returns a started master on a virtual bus with n more ports for
// nodes.
func newRig(t *testing.T, n int, opts ...master.Option) *rig {
	t.Helper()
	buses := testbus.Ports(t, n+1)
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

// startNode starts a node on a bus.
func startNode(t *testing.T, bus canopen.Bus, d *od.Dictionary, opts ...node.Option) *node.Node {
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

// waitState waits for a node's local NMT state.
func waitState(t *testing.T, n *node.Node, st canopen.NMTState) {
	t.Helper()
	nettest.Eventually(t, 5*time.Second, "node state "+st.String(), func() bool { return n.State() == st })
}

// enabled reports when a node's services have been enabled or disabled
// for a state: the node's state handler runs after that, while the state
// (and so the heartbeat) changes just before.
type enabled struct{ last atomic.Int32 }

func (e *enabled) option() node.Option {
	return node.WithStateHandler(func(sc nmt.StateChange) { e.last.Store(int32(sc.To)) })
}

// wait waits until the services are enabled for Operational.
func (e *enabled) wait(t *testing.T) { t.Helper(); e.waitFor(t, canopen.StateOperational) }

// waitFor waits until the services follow state st.
func (e *enabled) waitFor(t *testing.T, st canopen.NMTState) {
	t.Helper()
	nettest.Eventually(t, 5*time.Second, "services in "+st.String(), func() bool { return e.last.Load() == int32(st) })
}
