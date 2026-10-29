// SPDX-License-Identifier: MIT

package node_test

import (
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

// TestRecreateNodeHooks creates and closes nodes on one dictionary,
// started or not, with and without a drive: the services remove their
// dictionary hooks and watchers, so the count does not grow.
func TestRecreateNodeHooks(t *testing.T) {
	nettest.CheckLeaks(t)
	bus := testbus.Ports(t, 1)[0]
	d := nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 50 })
	base := d.HookCount()
	var during int
	for i := 0; i < 5; i++ {
		var opts []node.Option
		if i%2 == 1 {
			opts = append(opts, node.WithDrive(cia402.DriveConfig{}, 0))
		}
		n, err := node.New(bus, d, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 2 {
			if err := n.Start(); err != nil {
				t.Fatal(err)
			}
			if i == 2 {
				during = d.HookCount()
				if during <= base {
					t.Fatalf("running node: %d hooks, %d before", during, base)
				}
			} else if i == 4 && d.HookCount() != during {
				t.Fatalf("running node %d: %d hooks, first running node %d", i, d.HookCount(), during)
			}
		}
		_ = n.Close()
		if got := d.HookCount(); got != base {
			t.Fatalf("after node %d: %d hooks, want %d", i, got, base)
		}
	}
}

// TestResetCommunicationRestartsHeartbeatConsumer: after a reset
// communication a lost node is no longer lost and supervised nodes wait
// for their first heartbeat again.
func TestResetCommunicationRestartsHeartbeatConsumer(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	dA := nettest.Dict(t, 5, nil)
	// 0x1016 with a default entry, so that the supervision of node 6
	// survives the reset communication.
	dA.Remove(od.IndexConsumerHeartbeat)
	if err := dA.Add(od.NewArray(od.IndexConsumerHeartbeat, "Consumer heartbeat time", canopen.Unsigned32, od.AccessRW, 1,
		nmt.ConsumerEntry(6, 100*time.Millisecond), od.AccessRO)); err != nil {
		t.Fatal(err)
	}
	dB := nettest.Dict(t, 6, func(c *od.Config) { c.HeartbeatMs = 20 })
	b := startNode(t, r.buses[2], dB)
	a := startNode(t, r.buses[1], dA)
	r.rec.wait(t, "boot-up of 5", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 5 })
	hb := a.HeartbeatConsumer()
	nettest.Eventually(t, 2*time.Second, "supervision active", func() bool { return hb.Status(6).Active })
	_ = b.Close()
	nettest.Eventually(t, 2*time.Second, "node 6 lost", func() bool { return hb.Status(6).Lost })

	since := time.Now()
	if err := r.m.ResetCommunication(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := r.m.WaitBootUp(ctx, 5, since); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, canopen.StatePreOperational)
	if s := hb.Status(6); s.Lost || s.Active || s.Timeout != 100*time.Millisecond {
		t.Fatalf("status after reset communication %+v", s)
	}
	// Node 6 returns: the supervision becomes active again.
	startNode(t, r.buses[2], dB)
	nettest.Eventually(t, 2*time.Second, "supervision active again", func() bool { return hb.Status(6).Active })
}
