// SPDX-License-Identifier: MIT

package master_test

import (
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

// TestManagerLSSBoot boots a network of unconfigured devices: the LSS
// step gives each the node-ID of the slave whose expected identity it
// has, then the slaves boot and are configured; a device that matches no
// slave stays unconfigured.
func TestManagerLSSBoot(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 5)
	serials := []uint32{101, 102, 103, 999}
	var nodes []*node.Node
	for i, s := range serials {
		d := nettest.Dict(t, lss.Unconfigured, func(c *od.Config) { c.SerialNumber = s })
		nodes = append(nodes, startNode(t, buses[i+1], d, node.WithLSS()))
	}
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	boot := master.AssignSlave | master.AssignBoot | master.AssignMandatory
	for i, id := range []canopen.NodeID{5, 6, 7} {
		assign(t, d, id, master.NewAssignment(boot, 0, 0))
		set32(t, d, master.IndexExpectedVendorID, uint8(id), nettest.VendorID)
		set32(t, d, master.IndexExpectedSerialNumber, uint8(id), serials[i])
		concise(t, d, id, cdcf.Entry{Index: nettest.IdxParam, Data: u32(uint32(id))})
	}
	// Node 8 expects a vendor that no device has.
	assign(t, d, 8, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	set32(t, d, master.IndexExpectedVendorID, 8, 0xBAD)
	fs := lss.FastscanOptions{Timeout: 15 * time.Millisecond, Known: map[lss.Part]uint32{
		lss.PartVendor: nettest.VendorID, lss.PartProduct: nettest.ProductCode, lss.PartRevision: nettest.Revision,
	}}
	r := startManager(t, buses, d, master.WithLSSBoot(fs))
	nettest.Eventually(t, 30*time.Second, "network boot", func() bool {
		_, ok := r.rec.find(kind(master.EventNetworkBootDone, masterID))
		return ok
	})
	for i, id := range []canopen.NodeID{5, 6, 7} {
		ev := r.rec.wait(t, "LSS assignment", kind(master.EventLSSAssigned, id))
		if ev.LSS == nil || ev.LSS.Address.Serial != serials[i] || !strings.Contains(ev.String(), "lss-assigned") {
			t.Fatalf("%+v", ev)
		}
		r.rec.wait(t, "boot done", bootOK(id))
		waitState(t, nodes[i], canopen.StateOperational)
		if v, _ := nodes[i].Dictionary().GetUint(nettest.IdxParam, 0); v != uint64(id) {
			t.Fatalf("node %d param %d", id, v)
		}
	}
	if nodes[3].NodeID() != lss.Unconfigured || nodes[3].State() != canopen.StateInitialising {
		t.Fatal(nodes[3].NodeID(), nodes[3].State())
	}
	if st := r.m.Stats(); st.LSS.Sent == 0 {
		t.Fatalf("%+v", st.LSS)
	}
}
