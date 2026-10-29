// SPDX-License-Identifier: MIT

package node_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/store"
)

// storeDict is a device dictionary with the store objects.
func storeDict(t *testing.T, id canopen.NodeID) *od.Dictionary {
	t.Helper()
	d := nettest.Dict(t, id, nil)
	if err := store.AddObjects(d); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestStoreAcrossRestart saves parameters over SDO, restarts the node on a
// fresh dictionary and checks that the saved values are back; then
// restores the defaults and checks they apply after a reset only.
func TestStoreAcrossRestart(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			nettest.CheckLeaks(t)
			var s store.Store = store.NewMemory()
			if kind == "file" {
				s = store.NewFile(filepath.Join(t.TempDir(), "params.cdcf"))
			}
			r := newRig(t, 2)
			ctx := nettest.Ctx(t)
			d := storeDict(t, 5)
			n, err := node.New(r.buses[1], d, node.WithStore(s))
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Start(); err != nil {
				t.Fatal(err)
			}
			c, _ := r.m.SDO(5)
			if err := c.WriteUint16(ctx, od.IndexProducerHeartbeat, 0, 250); err != nil {
				t.Fatal(err)
			}
			if err := c.WriteUint32(ctx, nettest.IdxParam, 0, 0xCAFE); err != nil {
				t.Fatal(err)
			}
			if err := c.WriteUint32(ctx, store.IndexVerifyConfiguration, 1, 15000); err != nil {
				t.Fatal(err)
			}
			// Process data (rww) is not a parameter.
			if err := c.WriteUint8(ctx, nettest.IdxOut8, 0, 9); err != nil {
				t.Fatal(err)
			}
			// Reads of 0x1010/0x1011 report "on command".
			for _, idx := range []uint16{store.IndexStore, store.IndexRestore} {
				if v, err := c.ReadUint(ctx, idx, 1); err != nil || v != 1 {
					t.Fatalf("%04X:01 = %d %v", idx, v, err)
				}
				if v, err := c.ReadUint(ctx, idx, 0); err != nil || v != 4 {
					t.Fatalf("%04X:00 = %d %v", idx, v, err)
				}
			}
			// A wrong signature is refused.
			if err := c.WriteUint32(ctx, store.IndexStore, 1, 0x12345678); !errors.Is(err, canopen.AbortTransfer) {
				t.Fatalf("wrong signature: %v", err)
			}
			if err := r.m.StoreParameters(ctx, 5, store.All); err != nil {
				t.Fatal(err)
			}
			_ = n.Close()

			// A new node on a fresh dictionary loads the saved values.
			d2 := storeDict(t, 5)
			n2 := startNode(t, r.buses[2], d2, node.WithStore(s))
			if v, _ := d2.GetUint(od.IndexProducerHeartbeat, 0); v != 250 {
				t.Fatalf("0x1017 = %d after restart", v)
			}
			if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 0xCAFE {
				t.Fatalf("param = %#x after restart", v)
			}
			if v, _ := d2.GetUint(store.IndexVerifyConfiguration, 1); v != 15000 {
				t.Fatalf("0x1020:01 = %d after restart", v)
			}
			if v, _ := d2.GetUint(nettest.IdxOut8, 0); v != 0 {
				t.Fatalf("process data saved: %d", v)
			}
			// A reset keeps them.
			if err := n2.Reset(ctx, nmt.ResetApplication); err != nil {
				t.Fatal(err)
			}
			if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 0xCAFE {
				t.Fatalf("param = %#x after reset", v)
			}
			// Restore the manufacturer group: effective at the next reset
			// node, not at once; the communication group stays saved.
			if err := r.m.RestoreDefaults(ctx, 5, store.Manufacturer); err != nil {
				t.Fatal(err)
			}
			if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 0xCAFE {
				t.Fatalf("restore changed the value at once: %#x", v)
			}
			if err := n2.Reset(ctx, nmt.ResetApplication); err != nil {
				t.Fatal(err)
			}
			if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 0x12345678 {
				t.Fatalf("param = %#x after restore and reset", v)
			}
			if v, _ := d2.GetUint(od.IndexProducerHeartbeat, 0); v != 250 {
				t.Fatalf("0x1017 = %d after restoring another group", v)
			}
			// A reset communication restores only the communication area.
			_ = d2.SetUint(nettest.IdxParam, 0, 7)
			if err := r.m.RestoreDefaults(ctx, 5, store.Communication); err != nil {
				t.Fatal(err)
			}
			if err := n2.Reset(ctx, nmt.ResetCommunication); err != nil {
				t.Fatal(err)
			}
			if v, _ := d2.GetUint(od.IndexProducerHeartbeat, 0); v != 0 {
				t.Fatalf("0x1017 = %d after restoring communication", v)
			}
			if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 7 {
				t.Fatalf("reset communication touched the application area: %d", v)
			}
		})
	}
}

func TestStoreRequests(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	s := store.NewMemory()
	d := storeDict(t, 5)
	if err := store.AddObjects(d); err != nil {
		t.Fatal("second AddObjects:", err)
	}
	n := startNode(t, r.buses[1], d, node.WithStore(s))
	c, _ := r.m.SDO(5)
	tests := []struct {
		name  string
		index uint16
		sub   uint8
		val   uint32
		want  error
	}{
		{"save all", store.IndexStore, store.All, store.SignatureSave, nil},
		{"save comm", store.IndexStore, store.Communication, store.SignatureSave, nil},
		{"save application", store.IndexStore, store.Application, store.SignatureSave, nil},
		{"save manufacturer", store.IndexStore, store.Manufacturer, store.SignatureSave, nil},
		{"load signature to store", store.IndexStore, store.All, store.SignatureLoad, canopen.AbortTransfer},
		{"restore all", store.IndexRestore, store.All, store.SignatureLoad, nil},
		{"save signature to restore", store.IndexRestore, store.All, store.SignatureSave, canopen.AbortTransfer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.WriteUint32(ctx, tc.index, tc.sub, tc.val)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if s.Saves() != 5 {
		t.Fatalf("%d saves", s.Saves())
	}
	if err := n.SaveParameters(9); !errors.Is(err, canopen.AbortTransfer) {
		t.Fatalf("group 9: %v", err)
	}
	if err := n.RestoreDefaults(9); !errors.Is(err, canopen.AbortTransfer) {
		t.Fatalf("restore group 9: %v", err)
	}
	// Without a store, the methods fail and the objects are plain.
	n2 := startNode(t, r.buses[1], storeDict(t, 6))
	if err := n2.SaveParameters(1); !errors.Is(err, node.ErrNoStore) {
		t.Fatal(err)
	}
	if err := n2.RestoreDefaults(1); !errors.Is(err, node.ErrNoStore) {
		t.Fatal(err)
	}
}

// failingStore fails as configured.
type failingStore struct {
	loadErr, saveErr error
	entries          []cdcf.Entry
}

func (f *failingStore) Load() ([]cdcf.Entry, error) { return f.entries, f.loadErr }
func (f *failingStore) Save([]cdcf.Entry) error     { return f.saveErr }

func TestStoreFailures(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	boom := errors.New("boom")
	// New fails when the store cannot be read.
	if _, err := node.New(r.buses[1], storeDict(t, 5), node.WithStore(&failingStore{loadErr: boom})); !errors.Is(err, boom) {
		t.Fatalf("New: %v", err)
	}
	// Values the dictionary does not accept are skipped.
	fs := &failingStore{entries: []cdcf.Entry{
		{Index: 0x5555, Data: []byte{1}},
		{Index: nettest.IdxParam, Data: []byte{1}},
		{Index: od.IndexProducerHeartbeat, Data: []byte{100, 0}},
	}}
	d := storeDict(t, 5)
	n := startNode(t, r.buses[1], d, node.WithStore(fs))
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 100 {
		t.Fatalf("0x1017 = %d", v)
	}
	// A failing save is a hardware error for the client; a failing load
	// at a reset keeps the defaults.
	fs.saveErr = boom
	c, _ := r.m.SDO(5)
	if err := c.WriteUint32(ctx, store.IndexStore, 1, store.SignatureSave); !errors.Is(err, canopen.AbortHardware) {
		t.Fatalf("save: %v", err)
	}
	if err := c.WriteUint32(ctx, store.IndexRestore, 1, store.SignatureLoad); !errors.Is(err, canopen.AbortHardware) {
		t.Fatalf("restore: %v", err)
	}
	fs.loadErr = boom
	if err := n.SaveParameters(1); !errors.Is(err, boom) {
		t.Fatalf("save with load error: %v", err)
	}
	if err := n.RestoreDefaults(1); !errors.Is(err, boom) {
		t.Fatalf("restore with load error: %v", err)
	}
	if err := n.Reset(ctx, nmt.ResetApplication); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 0 {
		t.Fatalf("0x1017 = %d after failed load", v)
	}
	// Hooks are removed on Close.
	before := d.HookCount()
	_ = n.Close()
	if after := d.HookCount(); after >= before {
		t.Fatalf("hooks %d -> %d", before, after)
	}
}

func TestFileStore(t *testing.T) {
	dir := t.TempDir()
	f := store.NewFile(filepath.Join(dir, "p.cdcf"))
	if f.Path() == "" {
		t.Fatal("path")
	}
	if es, err := f.Load(); err != nil || es != nil {
		t.Fatalf("missing file: %v %v", es, err)
	}
	if err := f.Save([]cdcf.Entry{{Index: 0x2000, Data: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	es, err := f.Load()
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	_ = os.WriteFile(f.Path(), []byte{1, 2}, 0o600)
	if _, err := f.Load(); !errors.Is(err, cdcf.ErrFormat) {
		t.Fatalf("corrupt: %v", err)
	}
	_ = os.WriteFile(f.Path(), nil, 0o600)
	if _, err := f.Load(); !errors.Is(err, cdcf.ErrFormat) {
		t.Fatalf("empty: %v", err)
	}
	bad := store.NewFile(filepath.Join(dir, "missing-dir", "p.cdcf"))
	if err := bad.Save(nil); err == nil {
		t.Fatal("save into a missing directory")
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewFile(filepath.Join(dir, "d")).Load(); err == nil {
		t.Fatal("load of a directory")
	}
	if err := store.NewFile(filepath.Join(dir, "d")).Save(nil); err == nil {
		t.Fatal("rename over a directory")
	}
}
