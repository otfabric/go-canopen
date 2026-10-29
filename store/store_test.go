// SPDX-License-Identifier: MIT

package store_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/store"
)

func dict(t *testing.T) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: 5, HeartbeatMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddObjects(d); err != nil {
		t.Fatal(err)
	}
	_ = d.Add(od.NewVar(0x2000, "param", canopen.Unsigned16, od.AccessRW, uint16(7)))
	_ = d.Add(od.NewVar(0x2001, "output", canopen.Unsigned8, od.AccessRWW, nil))
	_ = d.Add(od.NewVar(0x6000, "profile", canopen.Unsigned8, od.AccessRW, uint8(1)))
	return d
}

func TestRange(t *testing.T) {
	tests := []struct {
		group       uint8
		first, last uint16
		ok          bool
	}{
		{store.All, 0x1000, 0x9FFF, true},
		{store.Communication, 0x1000, 0x1FFF, true},
		{store.Application, 0x6000, 0x9FFF, true},
		{store.Manufacturer, 0x2000, 0x5FFF, true},
		{0, 0, 0, false},
		{5, 0, 0, false},
	}
	for _, tc := range tests {
		f, l, ok := store.Range(tc.group)
		if f != tc.first || l != tc.last || ok != tc.ok {
			t.Errorf("group %d: %04X..%04X %v", tc.group, f, l, ok)
		}
	}
	for _, idx := range []uint16{0x1010, 0x1011, 0x1F50, 0x1F51, 0x1F82, 0x1F83} {
		if !store.Excluded(idx) {
			t.Errorf("%04X not excluded", idx)
		}
	}
	if store.Excluded(0x1017) {
		t.Error("0x1017 excluded")
	}
}

func TestSnapshotMergeApply(t *testing.T) {
	d := dict(t)
	_ = d.SetUint(0x2000, 0, 9)
	_ = d.SetUint(0x2001, 0, 3)
	snap := store.Snapshot(d, 0x2000, 0x5FFF)
	if len(snap) != 1 || snap[0].String() != "2000:00=09 00" {
		t.Fatalf("snapshot %v", snap)
	}
	comm := store.Snapshot(d, 0x1000, 0x1FFF)
	for _, e := range comm {
		if e.Index == 0x1010 || e.Index == 0x1011 || e.Index == 0x1000 {
			t.Fatalf("%v saved", e)
		}
	}
	merged := store.Merge(append(comm, cdcf.Entry{Index: 0x2000, Data: []byte{1, 0}}), snap, 0x2000, 0x5FFF)
	if len(merged) != len(comm)+1 || merged[len(merged)-1].String() != "2000:00=09 00" {
		t.Fatalf("merged %v", merged)
	}
	if w := store.Without(merged, 0x1000, 0x1FFF); len(w) != 1 {
		t.Fatalf("without %v", w)
	}
	d2 := dict(t)
	err := store.Apply(d2, append(merged, cdcf.Entry{Index: 0x3000, Data: []byte{1}}, cdcf.Entry{Index: 0x6000, Data: []byte{1, 2}}), 0x1000, 0x9FFF)
	if err == nil || !errors.Is(err, canopen.AbortNoObject) || !errors.Is(err, canopen.ErrDataType) {
		t.Fatalf("apply: %v", err)
	}
	if v, _ := d2.GetUint(0x2000, 0); v != 9 {
		t.Fatalf("applied %d", v)
	}
	if err := store.Apply(d2, []cdcf.Entry{{Index: 0x2000, Data: []byte{4, 0}}}, 0x1000, 0x1FFF); err != nil {
		t.Fatal(err)
	}
	if v, _ := d2.GetUint(0x2000, 0); v != 9 {
		t.Fatal("applied outside the range")
	}
}

func TestStores(t *testing.T) {
	stores := map[string]store.Store{
		"memory": store.NewMemory(),
		"zero":   &store.Memory{},
		"file":   store.NewFile(filepath.Join(t.TempDir(), "s.cdcf")),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			if es, err := s.Load(); err != nil || len(es) != 0 {
				t.Fatalf("empty: %v %v", es, err)
			}
			want := []cdcf.Entry{{Index: 0x2000, Data: []byte{9, 0}}, {Index: 0x1017, Data: []byte{100, 0}}}
			if err := s.Save(want); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load()
			if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
	if stores["memory"].(*store.Memory).Saves() != 1 {
		t.Fatal("saves")
	}
	f := stores["file"].(*store.File)
	_ = os.WriteFile(f.Path(), []byte{9}, 0o600)
	if _, err := f.Load(); !errors.Is(err, cdcf.ErrFormat) {
		t.Fatal(err)
	}
	if err := store.NewFile(filepath.Join(t.TempDir(), "no", "dir")).Save(nil); err == nil {
		t.Fatal("missing directory")
	}
	dir := t.TempDir()
	if _, err := store.NewFile(dir).Load(); err == nil {
		t.Fatal("directory loaded")
	}
	if err := store.NewFile(dir).Save(nil); err == nil {
		t.Fatal("saved over a directory")
	}
}

func ExampleSnapshot() {
	d, _ := od.Standard(od.Config{NodeID: 5, HeartbeatMs: 250})
	s := store.NewMemory()
	_ = s.Save(store.Snapshot(d, 0x1017, 0x1017))
	saved, _ := s.Load()
	fmt.Println(saved)
	// Output: [1017:00=FA 00]
}
