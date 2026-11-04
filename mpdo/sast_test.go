// SPDX-License-Identifier: MIT

package mpdo

import (
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// widen turns index:sub of d into an UNSIGNED64 and stores x in it: a
// malformed dictionary (EDS) whose object is wider than CiA 301 defines.
func widen(t *testing.T, d *od.Dictionary, index uint16, sub uint8, x uint64) {
	t.Helper()
	v, err := d.Variable(index, sub)
	if err != nil {
		t.Fatal(err)
	}
	v.DataType = canopen.Unsigned64
	if err := d.Set(index, sub, le(x, 8)); err != nil {
		t.Fatal(err)
	}
}

// TestScannerWide: a scanner entry above 32 bits is refused on a remote
// write and allows nothing when stored, instead of being truncated into an
// entry that allows a SAM-MPDO.
func TestScannerWide(t *testing.T) {
	d := device(t, 5)
	o, err := NewScannerList(1, 2)
	addObj(t, d, o, err)
	un := ProtectLists(d)
	defer un()
	wide := 1<<32 | uint64(ScannerEntry{Index: idxU8, BlockSize: 1}.Uint32())
	widen(t, d, IndexScanner, 1, 0)
	if err := d.Write(IndexScanner, 1, le(wide, 8)); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("write: %v", err)
	}
	widen(t, d, IndexScanner, 1, wide)
	if err := d.SetUint(IndexScanner, 0, 1); err != nil {
		t.Fatal(err)
	}
	if Scanned(d, idxU8, 0) {
		t.Error("wide entry allows a SAM-MPDO")
	}
	if e := ScannerEntries(d); len(e) != 0 {
		t.Errorf("entries %v", e)
	}
	// A value in range still works.
	widen(t, d, IndexScanner, 1, wide&0xFFFFFFFF)
	if !Scanned(d, idxU8, 0) || len(ScannerEntries(d)) != 1 {
		t.Error("entry in range")
	}
}

// TestListBounds: the last dispatcher list (0x1FFF) is searched, and a
// sub-index 00h above FEh (local write) reads at most FEh entries.
func TestListBounds(t *testing.T) {
	d := device(t, 5)
	o, err := NewDispatcherList(MaxLists, MaxListEntries)
	addObj(t, d, o, err)
	e := DispatcherEntry{Producer: 9, ProducerIndex: 0x3000, Index: idxU8, BlockSize: 1}
	if err := d.SetUint(IndexDispatcherLast, MaxListEntries, e.Uint64()); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(IndexDispatcherLast, 0, []byte{0xFF}); err != nil {
		t.Fatal(err)
	}
	if li, ls, ok := Dispatch(d, 9, 0x3000, 0); !ok || li != idxU8 || ls != 0 {
		t.Errorf("Dispatch = %04X %02X %v", li, ls, ok)
	}
	if got := DispatcherEntries(d); len(got) != 1 || got[0] != e {
		t.Errorf("entries %v", got)
	}
}

// TestLoadWide: PDO parameters wider than their data type make the MPDO
// inactive, and a wide inhibit time saturates.
func TestLoadWide(t *testing.T) {
	d := device(t, 5)
	setMPDO(t, d, true, 1, 0x185, ModeSAM)
	ci := pdo.CommIndex(true, 1)
	widen(t, d, ci, 3, 1<<62)
	if c := load(d, true, 1); !c.active() || c.inhibit != 0xFFFF*100*time.Microsecond {
		t.Errorf("%+v", c)
	}
	widen(t, d, ci, 2, 0x1FE)
	if c := load(d, true, 1); c.active() || c.tt != 0 {
		t.Errorf("wide transmission type: %+v", c)
	}
	widen(t, d, ci, 1, 1<<32|0x185)
	if c := load(d, true, 1); c.cob != canopen.COBInvalid {
		t.Errorf("wide COB-ID: %+v", c)
	}
}

// TestProtectPDOWide: a transmission type above 8 bits is not taken as its
// truncation (0x1FE is not event-driven 254), and a COB-ID above 32 bits
// counts as invalid.
func TestProtectPDOWide(t *testing.T) {
	d := device(t, 5)
	setMPDO(t, d, true, 1, 0x185|canopen.COBInvalid, ModeSAM)
	un, err := ProtectPDO(d, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	ci, mi := pdo.CommIndex(true, 1), pdo.MappingIndex(true, 1)
	widen(t, d, ci, 2, 254)
	if err := d.Write(ci, 2, le(0x1FE, 8)); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("transmission type: %v", err)
	}
	if err := d.Write(ci, 2, le(255, 8)); err != nil {
		t.Errorf("transmission type 255: %v", err)
	}
	widen(t, d, ci, 1, 1<<32|0x185)
	if err := d.Write(mi, 0, []byte{MappingDAM}); err != nil {
		t.Errorf("mode change with wide COB-ID: %v", err)
	}
}
