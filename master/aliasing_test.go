// SPDX-License-Identifier: MIT

package master_test

import (
	"slices"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/sdo"
)

// TestStatsOwned (safety.md, aliasing): the SDO map of a Stats snapshot
// is the caller's own; changing it changes no later snapshot.
func TestStatsOwned(t *testing.T) {
	r := newRig(t, 0)
	for _, id := range []canopen.NodeID{5, 6} {
		if _, err := r.m.SDO(id); err != nil {
			t.Fatal(err)
		}
	}
	s := r.m.Stats()
	if len(s.SDO) != 2 {
		t.Fatalf("SDO stats of %d nodes, want 2", len(s.SDO))
	}
	delete(s.SDO, 5)
	s.SDO[7] = sdo.ClientStats{Uploads: 1}
	again := r.m.Stats()
	if _, ok := again.SDO[5]; !ok || len(again.SDO) != 2 {
		t.Fatalf("SDO stats %v after changing an earlier snapshot, want nodes 5 and 6", again.SDO)
	}
}

// TestScanResultsOwned: Scan leaves the node list it is given unchanged,
// and each call returns a list of its own.
func TestScanResultsOwned(t *testing.T) {
	// A timeout long enough for a loaded system: node 5 must be found.
	r := newRig(t, 1, master.WithScanOptions(sdo.WithTimeout(2*time.Second)))
	startNode(t, r.buses[1], nettest.Dict(t, 5, nil))
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 5))
	ctx := nettest.Ctx(t)
	nodes := []canopen.NodeID{5, 200}
	given := slices.Clone(nodes)
	first, err := r.m.Scan(ctx, nodes)
	if len(first) != 1 || err == nil {
		t.Fatalf("scan: %+v, %v", first, err)
	}
	if !slices.Equal(nodes, given) {
		t.Fatalf("node list %v after Scan, want %v", nodes, given)
	}
	want := first[0]
	first[0].Node, first[0].DeviceType = 99, 0
	second, _ := r.m.Scan(ctx, nodes)
	if len(second) != 1 || second[0] != want {
		t.Fatalf("second scan %+v after changing the first result, want %+v", second, want)
	}
}
