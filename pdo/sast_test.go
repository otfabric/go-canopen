// SPDX-License-Identifier: MIT

package pdo

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// widen turns index:sub of d into an UNSIGNED64 and stores x in it: a
// malformed dictionary (EDS) whose PDO parameter is wider than CiA 301
// defines.
func widen(t *testing.T, d *od.Dictionary, index uint16, sub uint8, x uint64) {
	t.Helper()
	v, err := d.Variable(index, sub)
	if err != nil {
		t.Fatal(err)
	}
	v.DataType = canopen.Unsigned64
	if err := d.Set(index, sub, binary.LittleEndian.AppendUint64(nil, x)); err != nil {
		t.Fatal(err)
	}
}

// TestIndicesOutOfRange: a PDO number outside 1..512 has no parameter
// index (RPDO 513 used to alias the mapping of RPDO1, 0x1600).
func TestIndicesOutOfRange(t *testing.T) {
	for _, n := range []int{-1, 0, 513, 1 << 20} {
		for _, transmit := range []bool{false, true} {
			if ci, mi := CommIndex(transmit, n), MappingIndex(transmit, n); ci != 0 || mi != 0 {
				t.Errorf("n=%d transmit=%v: 0x%04X 0x%04X", n, transmit, ci, mi)
			}
		}
	}
	if CommIndex(false, 512) != 0x15FF || MappingIndex(true, 512) != 0x1BFF {
		t.Error("PDO 512")
	}
}

// TestLoadWideValues: a parameter value wider than its data type is
// refused instead of being truncated into a different valid value.
func TestLoadWideValues(t *testing.T) {
	ci, mi := CommIndex(true, 1), MappingIndex(true, 1)
	cases := []struct {
		index uint16
		sub   uint8
		x     uint64
	}{
		{ci, 1, 1<<32 | 0x185},
		{ci, 2, 0x101},
		{ci, 3, 0x1_0000},
		{ci, 5, 0x1_0064},
		{ci, 6, 0x101},
		{mi, 1, 1<<32 | uint64(Entry{idxU8, 0, 8}.Uint32())},
	}
	for _, c := range cases {
		d := device(t, 5)
		if err := d.Set(mi, 0, []byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := d.Set(mi, 1, le32(Entry{idxU8, 0, 8}.Uint32())); err != nil {
			t.Fatal(err)
		}
		widen(t, d, c.index, c.sub, c.x)
		if _, err := Load(d, true, 1); !errors.Is(err, canopen.AbortInvalidValue) {
			t.Errorf("0x%04X:%02X = 0x%X: %v", c.index, c.sub, c.x, err)
		}
	}
	// The same values in range load unchanged.
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, InhibitTime: 0xFFFF, EventTimer: 0xFFFF, SyncStart: 240, Mapping: Mapping{{idxU8, 0, 8}}})
	c, err := Load(d, true, 1)
	if err != nil || c.COBID != 0x185 || c.InhibitTime != 0xFFFF || c.EventTimer != 0xFFFF || c.SyncStart != 240 {
		t.Errorf("%+v %v", c, err)
	}
}

// TestProtectWideValues: remote writes to parameters a malformed
// dictionary makes wider than CiA 301 are checked on the full value, not
// on a truncation that would pass.
func TestProtectWideValues(t *testing.T) {
	ci, mi := CommIndex(true, 1), MappingIndex(true, 1)
	write := func(d *od.Dictionary, index uint16, sub uint8, x uint64) error {
		return d.Write(index, sub, binary.LittleEndian.AppendUint64(nil, x))
	}
	protected := func() *od.Dictionary {
		d := device(t, 5)
		un, err := Protect(d, true, 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(un)
		return d
	}

	// COB-ID: truncated, 0x1_8000_0185 is a valid write of an invalid
	// COB-ID.
	d := protected()
	widen(t, d, ci, 1, uint64(canopen.COBInvalid|0x185))
	if err := write(d, ci, 1, 1<<32|uint64(canopen.COBInvalid|0x185)); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("COB-ID: %v", err)
	}
	// A current COB-ID above 32 bits counts as invalid: the mapping can
	// be written.
	widen(t, d, ci, 1, 1<<32|0x185)
	if err := d.Write(mi, 0, []byte{0}); err != nil {
		t.Errorf("mapping with wide COB-ID: %v", err)
	}

	// Transmission type: truncated, 0x101 is the valid type 1.
	d = protected()
	widen(t, d, ci, 2, 1)
	if err := write(d, ci, 2, 0x101); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("transmission type: %v", err)
	}

	// Mapping entry: truncated, it maps idxU8.
	d = protected()
	widen(t, d, mi, 1, 0)
	if err := write(d, mi, 1, 1<<32|uint64(Entry{idxU8, 0, 8}.Uint32())); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("mapping entry: %v", err)
	}
	// An entry stored above 32 bits cannot be enabled.
	widen(t, d, mi, 1, 1<<32|uint64(Entry{idxU8, 0, 8}.Uint32()))
	if err := d.Write(mi, 0, []byte{1}); !errors.Is(err, canopen.AbortNotMappable) {
		t.Errorf("enable wide entry: %v", err)
	}
}

// TestSyncWindowWide: a 0x1007 value above 32 bits is no window, not a
// wrapped (possibly negative) duration.
func TestSyncWindowWide(t *testing.T) {
	d := device(t, 5)
	widen(t, d, od.IndexSyncWindow, 0, 1<<63|1)
	if w := (&base{d: d}).syncWindow(); w != 0 {
		t.Errorf("window %v", w)
	}
	widen(t, d, od.IndexSyncWindow, 0, 0xFFFFFFFF)
	if w := (&base{d: d}).syncWindow(); w <= 0 {
		t.Errorf("window %v", w)
	}
}
