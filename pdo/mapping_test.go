// SPDX-License-Identifier: MIT

package pdo

import (
	"bytes"
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

func TestEntry(t *testing.T) {
	tests := []struct {
		u     uint32
		e     Entry
		dummy bool
		s     string
	}{
		{0x60000108, Entry{0x6000, 1, 8}, false, "6000:01/8"},
		{0x20030001, Entry{0x2003, 0, 1}, false, "2003:00/1"},
		{0x00050008, Entry{0x0005, 0, 8}, true, "0005:00/8"},
		{0x00070020, Entry{0x0007, 0, 32}, true, "0007:00/32"},
		{0x00080020, Entry{0x0008, 0, 32}, false, "0008:00/32"},
		{0x00010101, Entry{0x0001, 1, 1}, false, "0001:01/1"},
	}
	for _, tt := range tests {
		e := ParseEntry(tt.u)
		if e != tt.e || e.Uint32() != tt.u || e.IsDummy() != tt.dummy || e.String() != tt.s {
			t.Errorf("%08X: got %+v %08X dummy=%v %q", tt.u, e, e.Uint32(), e.IsDummy(), e.String())
		}
	}
	m := Mapping{{0x2000, 0, 8}, {0x2003, 0, 1}, {0x2001, 0, 16}}
	if m.Bits() != 25 || m.Len() != 4 || m.String() != "[2000:00/8 2003:00/1 2001:00/16]" {
		t.Errorf("mapping: %d bits, %d bytes, %s", m.Bits(), m.Len(), m)
	}
}

func TestPackUnpack(t *testing.T) {
	tests := []struct {
		name string
		m    Mapping
		vals [][]byte
		data []byte
	}{
		{"empty", Mapping{}, [][]byte{}, []byte{}},
		{"u8 u16", Mapping{{1, 1, 8}, {1, 2, 16}}, [][]byte{{0x12}, {0x34, 0x56}}, []byte{0x12, 0x34, 0x56}},
		{"bits", Mapping{{1, 1, 1}, {1, 2, 1}, {1, 3, 1}, {1, 4, 1}}, [][]byte{{1}, {0}, {1}, {1}}, []byte{0x0D}},
		{"unaligned", Mapping{{1, 1, 1}, {1, 2, 8}, {1, 3, 16}}, [][]byte{{1}, {0xFF}, {0x01, 0x80}},
			[]byte{0xFF, 0x03, 0x00, 0x01}},
		{"64 bits", Mapping{{1, 1, 64}}, [][]byte{{1, 2, 3, 4, 5, 6, 7, 8}}, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{"bit then u32 then 31 dummy bits", Mapping{{1, 1, 1}, {1, 2, 32}, {1, 3, 31}},
			[][]byte{{1}, {0xEF, 0xBE, 0xAD, 0xDE}, nil},
			[]byte{0xDF, 0x7D, 0x5B, 0xBD, 0x01, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Pack(tt.m, tt.vals)
			if err != nil || !bytes.Equal(got, tt.data) {
				t.Fatalf("Pack = % X, %v; want % X", got, err, tt.data)
			}
			vals, err := Unpack(tt.m, append(got, 0xAA)) // extra bytes are ignored
			if err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				want := tt.vals[i]
				if want == nil {
					want = make([]byte, (int(tt.m[i].Bits)+7)/8)
				}
				if !bytes.Equal(v, want) {
					t.Errorf("value %d = % X, want % X", i, v, want)
				}
			}
		})
	}
}

func TestPackErrors(t *testing.T) {
	if _, err := Pack(Mapping{{1, 1, 8}}, nil); err == nil {
		t.Error("value count mismatch accepted")
	}
	if _, err := Pack(Mapping{{1, 1, 16}}, [][]byte{{1}}); err == nil {
		t.Error("short value accepted")
	}
	long := Mapping{{1, 1, 64}, {1, 2, 8}}
	if _, err := Pack(long, [][]byte{nil, nil}); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("65 bits: %v", err)
	}
	if _, err := Unpack(long, make([]byte, 9)); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("unpack 65 bits: %v", err)
	}
	if _, err := Unpack(Mapping{{1, 1, 8}, {1, 2, 1}}, []byte{1}); !errors.Is(err, ErrLength) {
		t.Errorf("short data: %v", err)
	}
	// A wide value contributes only its low bits.
	got, _ := Pack(Mapping{{1, 1, 1}, {1, 2, 1}}, [][]byte{{0xFF}, {0x02}})
	if !bytes.Equal(got, []byte{0x01}) {
		t.Errorf("masking: % X", got)
	}
}

func TestValidateEntry(t *testing.T) {
	d := device(t, 5)
	tests := []struct {
		e        Entry
		transmit bool
		want     error
	}{
		{Entry{idxU8, 0, 8}, true, nil},
		{Entry{idxU8, 0, 8}, false, nil},
		{Entry{idxU8, 0, 16}, true, canopen.AbortNotMappable},
		{Entry{idxU8, 0, 0}, true, canopen.AbortNotMappable},
		{Entry{idxI16, 0, 16}, false, nil},
		{Entry{idxBool, 0, 1}, false, nil},
		{Entry{idxBool, 0, 8}, false, nil},
		{Entry{idxBool, 0, 2}, false, canopen.AbortNotMappable},
		{Entry{idxRO, 0, 16}, true, nil},
		{Entry{idxRO, 0, 16}, false, canopen.AbortNotMappable},
		{Entry{idxNoMap, 0, 8}, true, canopen.AbortNotMappable},
		{Entry{idxStr, 0, 8}, true, canopen.AbortNotMappable},
		{Entry{0x2FFF, 0, 8}, true, canopen.AbortNoObject},
		{Entry{idxU8, 1, 8}, true, canopen.AbortNoObject},
		{Entry{0x0005, 0, 8}, false, nil},
		{Entry{0x0001, 0, 1}, false, nil},
		{Entry{0x0007, 0, 32}, false, nil},
		{Entry{0x0005, 0, 8}, true, canopen.AbortNotMappable},
		{Entry{0x0006, 0, 8}, false, canopen.AbortNotMappable},
		{Entry{idxBits, 3, 1}, true, nil},
	}
	for _, tt := range tests {
		if err := ValidateEntry(d, tt.e, tt.transmit); err != tt.want {
			t.Errorf("%s transmit=%v: %v, want %v", tt.e, tt.transmit, err, tt.want)
		}
	}
	m := make(Mapping, MaxEntries+1)
	if err := m.Validate(d, true); err != canopen.AbortPDOLength {
		t.Errorf("65 entries: %v", err)
	}
	m = Mapping{{idxU32, 0, 32}, {idxU32, 0, 32}, {idxU8, 0, 8}}
	if err := m.Validate(d, true); err != canopen.AbortPDOLength {
		t.Errorf("72 bits: %v", err)
	}
	m = Mapping{{idxU32, 0, 32}, {idxNoMap, 0, 8}}
	if err := m.Validate(d, true); err != canopen.AbortNotMappable {
		t.Errorf("unmappable: %v", err)
	}
}

func TestTransmissionType(t *testing.T) {
	tests := []struct {
		t          TransmissionType
		sync, ev   bool
		tpdo, rpdo bool
		s          string
	}{
		{0, true, false, true, true, "synchronous acyclic (0)"},
		{1, true, false, true, true, "synchronous cyclic (1)"},
		{240, true, false, true, true, "synchronous cyclic (240)"},
		{241, false, false, false, false, "reserved (241)"},
		{251, false, false, false, false, "reserved (251)"},
		{252, false, false, true, false, "RTR-only synchronous (252)"},
		{253, false, false, true, false, "RTR-only event-driven (253)"},
		{254, false, true, true, true, "event-driven manufacturer-specific (254)"},
		{255, false, true, true, true, "event-driven profile-specific (255)"},
	}
	for _, tt := range tests {
		if tt.t.Synchronous() != tt.sync || tt.t.EventDriven() != tt.ev || tt.t.ValidFor(true) != tt.tpdo ||
			tt.t.ValidFor(false) != tt.rpdo || tt.t.String() != tt.s {
			t.Errorf("%d: %v %v %v %v %q", tt.t, tt.t.Synchronous(), tt.t.EventDriven(), tt.t.ValidFor(true), tt.t.ValidFor(false), tt.t)
		}
	}
}

func TestRestrictedAndCOBID(t *testing.T) {
	for _, id := range []uint32{0, 0x7F, 0x101, 0x180, 0x581, 0x5FF, 0x601, 0x67F, 0x6E0, 0x6FF, 0x701, 0x77F, 0x780, 0x7FF} {
		if !Restricted(canbusID(id)) {
			t.Errorf("0x%X not restricted", id)
		}
	}
	for _, id := range []uint32{0x80, 0x100, 0x181, 0x1FF, 0x580, 0x600, 0x680, 0x6DF, 0x700} {
		if Restricted(canbusID(id)) {
			t.Errorf("0x%X restricted", id)
		}
	}
	tests := []struct {
		c   canopen.COBID
		err error
	}{
		{0x185, nil},
		{0x185 | canopen.COBInvalid, nil},
		{0x005 | canopen.COBInvalid, nil},
		{0x005, canopen.AbortInvalidValue},
		{0x985, canopen.AbortInvalidValue},
		{0x12345678 | canopen.COBExtended, nil},
		{0x7FF | canopen.COBExtended, nil},
	}
	for _, tt := range tests {
		if err := checkCOBID(tt.c); err != tt.err {
			t.Errorf("%v: %v", tt.c, err)
		}
	}
}

func TestLoad(t *testing.T) {
	d := device(t, 5)
	if _, err := Load(d, true, 0); !errors.Is(err, ErrNumber) {
		t.Errorf("TPDO 0: %v", err)
	}
	if _, err := Load(d, true, 5); !errors.Is(err, canopen.AbortNoObject) {
		t.Errorf("missing TPDO 5: %v", err)
	}
	c, err := Load(d, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.COBID != 0x185 || c.TransmissionType != 255 || len(c.Mapping) != 0 || c.Active() {
		t.Errorf("default TPDO1: %+v", c)
	}
	// A mapping count beyond 64 is refused.
	if err := d.Set(MappingIndex(true, 1), 0, []byte{65}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(d, true, 1); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("65 entries: %v", err)
	}
	if err := d.Set(MappingIndex(true, 1), 0, []byte{9}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(d, true, 1); !errors.Is(err, canopen.AbortNoSubIndex) {
		t.Errorf("9 of 8 entries: %v", err)
	}
	// Optional sub-indices read as 0.
	d2 := od.New(5)
	comm := od.NewRecord(0x1400, "c", od.Sub(1, "cob", canopen.Unsigned32, od.AccessRW, uint32(0x205)),
		od.Sub(2, "type", canopen.Unsigned8, od.AccessRW, uint8(1)))
	mp := od.NewRecord(0x1600, "m", od.Sub(1, "e", canopen.Unsigned32, od.AccessRW, uint32(0x00050008)))
	mp.Subs[0].Access = od.AccessRW
	mp.Subs[0].Default = []byte{1}
	for _, o := range []*od.Object{comm, mp} {
		if err := d2.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	c, err = Load(d2, false, 1)
	if err != nil || c.TransmissionType != 1 || c.EventTimer != 0 || len(c.Mapping) != 1 || !c.Active() {
		t.Errorf("minimal RPDO: %+v %v", c, err)
	}
	if _, err := Load(d2, true, 1); err == nil {
		t.Error("missing TPDO loaded")
	}
	d2.Remove(0x1600)
	if _, err := Load(d2, false, 1); !errors.Is(err, canopen.AbortNoObject) {
		t.Errorf("missing mapping: %v", err)
	}
	d2.Remove(0x1400)
	_ = d2.Add(od.NewRecord(0x1400, "c", od.Sub(1, "cob", canopen.Unsigned32, od.AccessRW, uint32(0x205))))
	if _, err := Load(d2, false, 1); !errors.Is(err, canopen.AbortNoSubIndex) {
		t.Errorf("missing type: %v", err)
	}
}

func TestIndices(t *testing.T) {
	if CommIndex(true, 1) != 0x1800 || CommIndex(false, 512) != 0x15FF || MappingIndex(true, 3) != 0x1A02 || MappingIndex(false, 1) != 0x1600 {
		t.Error("indices")
	}
	if kind(true, 2) != "TPDO2" || kind(false, 7) != "RPDO7" {
		t.Error("kind")
	}
}
