// SPDX-License-Identifier: MIT

package node_test

import (
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

// TestErrorBehaviourWide: a 0x1029 entry wider than UNSIGNED8 was
// truncated (0x100 acted as 0, pre-operational); a value that is not a
// behaviour code now leaves the state unchanged.
func TestErrorBehaviourWide(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	d.Remove(node.IndexErrorBehaviour)
	if err := d.Add(od.NewRecord(node.IndexErrorBehaviour, "Error behaviour",
		od.Sub(1, "Communication error", canopen.Unsigned16, od.AccessRW, uint16(0x100)))); err != nil {
		t.Fatal(err)
	}
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(nmt.ConsumerEntry(9, 60*time.Millisecond)))
	n := startNode(t, r.buses[1], d)
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
	f := nmt.ErrorControlFrame(9, canopen.StateOperational, false)
	if err := r.buses[0].Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY 0x8130", emcyFrom(5, emcy.CodeHeartbeat))
	time.Sleep(20 * time.Millisecond)
	if n.State() != canopen.StateOperational {
		t.Fatalf("state %s with error behaviour 0x100", n.State())
	}
}
