// SPDX-License-Identifier: MIT

package node_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
)

// lssDict is an unconfigured device dictionary with serial number serial.
func lssDict(t *testing.T, serial uint32) *od.Dictionary {
	t.Helper()
	d := nettest.Dict(t, lss.Unconfigured, func(c *od.Config) { c.SerialNumber = serial; c.HeartbeatMs = 50 })
	if err := store.AddObjects(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func lssAddr(serial uint32) lss.Address {
	return lss.Address{Vendor: nettest.VendorID, Product: nettest.ProductCode, Revision: nettest.Revision, Serial: serial}
}

// TestLSSUnconfiguredNode starts a node without node-ID: it stays silent
// until LSS configures one, then boots; the stored node-ID survives a
// restart.
func TestLSSUnconfiguredNode(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1, master.WithLSSOptions(lss.WithTimeout(200*time.Millisecond)))
	ctx := nettest.Ctx(t)
	mem := store.NewMemory()
	var bits atomic.Int32
	bits.Store(-1)
	d := lssDict(t, 77)
	n, err := node.New(r.buses[1], d, node.WithStore(mem), node.WithLSS(
		lss.WithBitTimingHandler(func(_ uint8, b lss.BitTiming) error { bits.Store(int32(b)); return nil })))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.StoredLSS(); ok {
		t.Fatal("stored settings on an empty store")
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	if n.NodeID() != lss.Unconfigured || n.State() != canopen.StateInitialising {
		t.Fatal(n.NodeID(), n.State())
	}
	time.Sleep(150 * time.Millisecond)
	if st := n.Stats(); st.Heartbeat.Sent != 0 || st.NMT.BootUps != 0 {
		t.Fatalf("unconfigured node sent: %+v", st.Heartbeat)
	}
	if _, ok := r.rec.find(func(e master.Event) bool { return e.Kind == master.EventBootUp }); ok {
		t.Fatal("boot-up of an unconfigured node")
	}
	lm := r.m.LSS()
	addr := lssAddr(77)
	err = lm.Configure(ctx, addr, func(ctx context.Context) error {
		if err := lm.SetNodeID(ctx, 21); err != nil {
			return err
		}
		if err := lm.SetBitTiming(ctx, lss.BitTiming(2)); err != nil {
			return err
		}
		return lm.Store(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "boot-up of node 21", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 21 })
	waitState(t, n, canopen.StatePreOperational)
	if n.NodeID() != 21 || n.LSS().NodeID() != 21 || bits.Load() != 2 {
		t.Fatal(n.NodeID(), n.LSS().NodeID(), bits.Load())
	}
	// The SDO server answers on the new node-ID.
	if v, err := mustSDO(t, r.m, 21).ReadUint(ctx, od.IndexIdentity, 4); err != nil || v != 77 {
		t.Fatal(v, err)
	}
	if st := n.Stats(); st.LSS.Stores != 1 || st.LSS.NodeIDs != 1 {
		t.Fatalf("%+v", st.LSS)
	}
	// Parameter saves keep the LSS settings.
	if err := n.SaveParameters(store.All); err != nil {
		t.Fatal(err)
	}
	if err := n.RestoreDefaults(store.Communication); err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart on a fresh unconfigured dictionary: the stored node-ID applies.
	r.rec.reset()
	n2, err := node.New(r.buses[1], lssDict(t, 77), node.WithStore(mem), node.WithLSS())
	if err != nil {
		t.Fatal(err)
	}
	set, ok := n2.StoredLSS()
	if !ok || set.NodeID != 21 || !set.BitTimingSet || set.BitTiming != 2 {
		t.Fatal(set, ok)
	}
	if err := n2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n2.Close() }()
	r.rec.wait(t, "boot-up after restart", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 21 })
	if n2.NodeID() != 21 || n2.LSS().NodeID() != 21 {
		t.Fatal(n2.NodeID())
	}
	// LSS can make it unconfigured again: after the reset it is silent.
	err = lm.Configure(ctx, addr, func(ctx context.Context) error { return lm.SetNodeID(ctx, lss.Unconfigured) })
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.ResetCommunication(ctx, 21); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "unconfigured", func() bool {
		return n2.NodeID() == lss.Unconfigured && n2.LSS().NodeID() == lss.Unconfigured
	})
	if n2.State() != canopen.StateInitialising {
		t.Fatal(n2.State())
	}
	// And give it a node-ID again with AssignAll.
	got, err := lm.AssignAll(ctx, 30)
	if err != nil || len(got) != 1 || got[0].NodeID != 30 {
		t.Fatal(got, err)
	}
	r.rec.wait(t, "boot-up of node 30", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 30 })
}

// TestLSSConfiguredNode runs LSS on a configured node: a new node-ID
// applies at the next reset communication.
func TestLSSConfiguredNode(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 9, nil)
	n := startNode(t, r.buses[1], d, node.WithLSS())
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 9 })
	lm := r.m.LSS()
	addr := lssAddr(9)
	// Without a store, "store configuration" is not supported.
	err := lm.Configure(ctx, addr, func(ctx context.Context) error {
		if err := lm.SetNodeID(ctx, 10); err != nil {
			return err
		}
		return lm.Store(ctx)
	})
	var ce *lss.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("store without a store: %v", err)
	}
	if n.NodeID() != 9 {
		t.Fatal(n.NodeID())
	}
	if err := n.Reset(ctx, nmt.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	if n.NodeID() != 10 || n.LSS().NodeID() != 10 {
		t.Fatal(n.NodeID(), n.LSS().NodeID())
	}
	if id, err := func() (canopen.NodeID, error) {
		var id canopen.NodeID
		err := lm.Configure(ctx, addr, func(ctx context.Context) error {
			var err error
			id, err = lm.InquireNodeID(ctx)
			return err
		})
		return id, err
	}(); err != nil || id != 10 {
		t.Fatal(id, err)
	}
}

func TestLSSNeedsIdentity(t *testing.T) {
	r := newRig(t, 1)
	d := od.New(5)
	if _, err := node.New(r.buses[1], d, node.WithLSS()); err == nil {
		t.Fatal("LSS without 0x1018")
	}
	// Unconfigured without LSS is refused.
	if _, err := node.New(r.buses[1], nettest.Dict(t, lss.Unconfigured, nil)); err == nil {
		t.Fatal("unconfigured node without LSS")
	}
}

func mustSDO(t *testing.T, m *master.Master, id canopen.NodeID) *sdo.Client {
	t.Helper()
	c, err := m.SDO(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
