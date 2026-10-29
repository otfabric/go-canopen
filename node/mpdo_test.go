// SPDX-License-Identifier: MIT

package node_test

import (
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/mpdo"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// makeMPDO configures PDO n of d locally as an MPDO.
func makeMPDO(t *testing.T, d *od.Dictionary, transmit bool, n int, mode uint8, cob canopen.COBID) {
	t.Helper()
	for _, w := range []struct {
		index uint16
		sub   uint8
		x     uint64
	}{
		{pdo.CommIndex(transmit, n), 1, uint64(cob | canopen.COBInvalid)},
		{pdo.MappingIndex(transmit, n), 0, 0},
		{pdo.CommIndex(transmit, n), 2, uint64(pdo.TransEventManufacturer)},
		{pdo.MappingIndex(transmit, n), 0, uint64(mode)},
		{pdo.CommIndex(transmit, n), 1, uint64(cob)},
	} {
		if err := d.SetUint(w.index, w.sub, w.x); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMPDOBetweenNodes sends DAM- and SAM-MPDOs from one node to another,
// and makes a third PDO an MPDO over the network.
func TestMPDOBetweenNodes(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	da := nettest.Dict(t, 5, nil)
	db := nettest.Dict(t, 6, nil)
	makeMPDO(t, da, true, 1, mpdo.MappingDAM, 0x185)
	makeMPDO(t, db, false, 1, mpdo.MappingDAM, 0x185)
	makeMPDO(t, da, true, 2, mpdo.MappingSAM, 0x285)
	makeMPDO(t, db, false, 2, mpdo.MappingSAM, 0x285)
	sl, err := mpdo.NewScannerList(1, 4, mpdo.ScannerEntry{Index: nettest.IdxOut32})
	if err != nil {
		t.Fatal(err)
	}
	if err := da.Add(sl); err != nil {
		t.Fatal(err)
	}
	dl, err := mpdo.NewDispatcherList(1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Add(dl); err != nil {
		t.Fatal(err)
	}
	var ea, eb enabled
	a := startNode(t, r.buses[1], da, ea.option())
	b := startNode(t, r.buses[2], db, eb.option())
	if a.MPDOProducer(1) == nil || a.MPDOProducer(2) == nil || b.MPDOConsumer(1) == nil || b.MPDOConsumer(2) == nil {
		t.Fatal("MPDO services missing")
	}
	if a.MPDOProducer(3) != nil || b.MPDOConsumer(3) != nil {
		t.Fatal("MPDO service for a classical PDO")
	}
	// Disabled before Operational.
	if err := a.MPDOProducer(1).SendDAM(ctx, 6, nettest.IdxOut16, 0, []byte{1, 2}); !errors.Is(err, mpdo.ErrDisabled) {
		t.Fatalf("send while pre-operational: %v", err)
	}
	// The dispatcher list is written over the network.
	err = r.m.WriteDispatcherList(ctx, 6, 1, []mpdo.DispatcherEntry{{
		Producer: 5, ProducerIndex: nettest.IdxOut32, Index: nettest.IdxOut32, BlockSize: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.WriteScannerList(ctx, 6, 1, nil); !errors.Is(err, canopen.AbortNoObject) {
		t.Fatalf("scanner list the node lacks: %v", err)
	}
	if err := r.m.WriteScannerList(ctx, 6, 0, nil); err == nil {
		t.Fatal("list 0 accepted")
	}
	if err := r.m.StartNode(ctx, 0); err != nil {
		t.Fatal(err)
	}
	ea.wait(t)
	eb.wait(t)

	// DAM: node 5 writes node 6's 0x2001.
	if err := a.MPDOProducer(1).SendDAM(ctx, 6, nettest.IdxOut16, 0, []byte{0x34, 0x12}); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "DAM value", func() bool {
		v, _ := db.GetUint(nettest.IdxOut16, 0)
		return v == 0x1234
	})
	// A DAM-MPDO for a missing object raises EMCY 0x8230 on node 6.
	if err := a.MPDOProducer(1).SendDAM(ctx, 6, 0x5FFF, 0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY 0x8230", emcyFrom(6, mpdo.EMCYDAMNoObject))

	// SAM: node 5 sends its own 0x2002, node 6 dispatches it.
	if err := da.SetUint(nettest.IdxOut32, 0, 0xDEADBEEF); err != nil {
		t.Fatal(err)
	}
	if err := a.MPDOProducer(2).Send(ctx, nettest.IdxOut32, 0); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "SAM value", func() bool {
		v, _ := db.GetUint(nettest.IdxOut32, 0)
		return v == 0xDEADBEEF
	})
	if err := a.MPDOProducer(2).Send(ctx, nettest.IdxOut16, 0); !errors.Is(err, mpdo.ErrNotScanned) {
		t.Fatalf("unscanned object: %v", err)
	}

	// TPDO 3 of node 5 and RPDO 3 of node 6 become DAM MPDOs over the
	// network: the nodes create the services.
	if err := r.m.PreOperational(ctx, 0); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, canopen.StatePreOperational)
	waitState(t, b, canopen.StatePreOperational)
	if err := r.m.ConfigureMPDO(ctx, 5, true, 3, mpdo.MappingDAM, 0x385, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.m.ConfigureMPDO(ctx, 6, false, 3, mpdo.MappingDAM, 0x385, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.m.ConfigureMPDO(ctx, 6, false, 3, 7, 0x385, 0); err == nil {
		t.Fatal("mode 7 accepted")
	}
	if a.MPDOProducer(3) == nil || b.MPDOConsumer(3) == nil {
		t.Fatal("MPDO services not created")
	}
	if err := r.m.StartNode(ctx, 0); err != nil {
		t.Fatal(err)
	}
	ea.wait(t)
	eb.wait(t)
	// Broadcast DAM (node 0): every consumer writes.
	if err := a.MPDOProducer(3).SendDAM(ctx, 0, nettest.IdxOut8, 0, []byte{0x5A}); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "broadcast DAM value", func() bool {
		v, _ := db.GetUint(nettest.IdxOut8, 0)
		return v == 0x5A
	})
	sa, sb := a.Stats(), b.Stats()
	if sa.MPDOProducer[1].Sent < 2 || sa.MPDOProducer[2].Sent != 1 || sa.MPDOProducer[3].Sent != 1 {
		t.Fatalf("%+v", sa.MPDOProducer)
	}
	if sb.MPDOConsumer[1].Applied != 1 || sb.MPDOConsumer[1].NoObject != 1 || sb.MPDOConsumer[2].Applied != 1 || sb.MPDOConsumer[3].Applied != 1 {
		t.Fatalf("%+v", sb.MPDOConsumer)
	}
	// Leaving Operational clears the MPDO emergency.
	if err := r.m.PreOperational(ctx, 6); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "error reset", emcyFrom(6, 0))
}
