// SPDX-License-Identifier: MIT

package node_test

import (
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// TestSyncWindow checks that the node enforces the synchronous window
// 0x1007 from the reception of each SYNC: a synchronous RPDO received
// after the window is discarded, one received within it applies at the
// next SYNC.
func TestSyncWindow(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	for _, w := range []struct {
		index uint16
		sub   uint8
		x     uint64
	}{
		{pdo.CommIndex(false, 1), 1, 0x205 | uint64(canopen.COBInvalid)},
		{pdo.MappingIndex(false, 1), 0, 0},
		{pdo.MappingIndex(false, 1), 1, uint64(nettest.IdxOut8)<<16 | 8},
		{pdo.MappingIndex(false, 1), 0, 1},
		{pdo.CommIndex(false, 1), 2, 1},
		{pdo.CommIndex(false, 1), 1, 0x205},
		{od.IndexSyncWindow, 0, 100_000}, // 100 ms
	} {
		if err := d.SetUint(w.index, w.sub, w.x); err != nil {
			t.Fatal(err)
		}
	}
	n := startNode(t, r.buses[1], d)
	if err := r.m.StartNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateOperational)
	rpdo := func(v byte) {
		f, _ := canbus.NewStandard(0x205, []byte{v})
		if err := r.buses[0].Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	sync := func() {
		if err := r.m.SYNC().Send(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Outside the window: discarded.
	sync()
	nettest.Eventually(t, 2*time.Second, "SYNC received", func() bool { return n.Stats().SYNC.Received >= 1 })
	time.Sleep(250 * time.Millisecond)
	rpdo(7)
	nettest.Eventually(t, 2*time.Second, "outside window", func() bool { return n.Stats().RPDO[1].OutsideWindow == 1 })
	sync()
	nettest.Eventually(t, 2*time.Second, "second SYNC", func() bool { return n.Stats().SYNC.Received >= 2 })
	if v, _ := d.GetUint(nettest.IdxOut8, 0); v != 0 {
		t.Fatalf("value outside the window applied: %d", v)
	}
	// Within the window (sent right after the SYNC): applied at the next.
	rpdo(9)
	nettest.Eventually(t, 2*time.Second, "received", func() bool { return n.Stats().RPDO[1].Received == 2 })
	sync()
	nettest.Eventually(t, 2*time.Second, "applied", func() bool {
		v, _ := d.GetUint(nettest.IdxOut8, 0)
		return v == 9
	})
}
