// SPDX-License-Identifier: MIT

package mpdo

import (
	"errors"
	"reflect"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

func TestMessage(t *testing.T) {
	tests := []struct {
		name string
		m    Message
		b    [8]byte
		str  string
	}{
		{"SAM", Message{Node: 5, Index: 0x2001, SubIndex: 3, Data: [4]byte{1, 2, 3, 4}},
			[8]byte{0x05, 0x01, 0x20, 0x03, 1, 2, 3, 4}, "SAM from node 5 2001:03 01 02 03 04"},
		{"DAM", Message{DAM: true, Node: 127, Index: 0x6040, SubIndex: 0, Data: [4]byte{0x0F}},
			[8]byte{0xFF, 0x40, 0x60, 0x00, 0x0F, 0, 0, 0}, "DAM to node 127 6040:00 0F 00 00 00"},
		{"DAM broadcast", Message{DAM: true, Index: 0xFFFF, SubIndex: 0xFF},
			[8]byte{0x80, 0xFF, 0xFF, 0xFF}, "DAM to node 0 FFFF:FF 00 00 00 00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.Bytes(); got != tt.b {
				t.Errorf("Bytes = % X, want % X", got, tt.b)
			}
			m, err := ParseMessage(tt.b[:])
			if err != nil || m != tt.m {
				t.Errorf("ParseMessage = %+v, %v", m, err)
			}
			if s := tt.m.String(); s != tt.str {
				t.Errorf("String = %q", s)
			}
		})
	}
	// Bit 7 of the node-ID is not carried.
	if b := (Message{Node: 0x85}).Bytes(); b[0] != 0x05 {
		t.Errorf("node 0x85 encoded as %02X", b[0])
	}
	// Longer data: the first 8 bytes.
	m, err := ParseMessage([]byte{1, 2, 0, 3, 4, 5, 6, 7, 8, 9})
	if err != nil || m.Data != [4]byte{4, 5, 6, 7} || m.Index != 2 {
		t.Errorf("long: %+v %v", m, err)
	}
	for _, n := range []int{0, 1, 7} {
		if _, err := ParseMessage(make([]byte, n)); !errors.Is(err, ErrLength) {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
}

func FuzzParseMessage(f *testing.F) {
	f.Add([]byte{0x85, 1, 0x20, 0, 1, 2, 3, 4})
	f.Add([]byte{0x05, 1, 0x20, 0, 1, 2, 3, 4, 9})
	f.Add([]byte{0x80})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseMessage(data)
		if err != nil {
			if len(data) >= Size {
				t.Fatalf("%d bytes: %v", len(data), err)
			}
			return
		}
		b := m.Bytes()
		if string(b[:]) != string(data[:Size]) {
			t.Fatalf("round trip % X -> % X", data[:Size], b)
		}
		_ = m.String()
	})
}

func TestScannerEntry(t *testing.T) {
	tests := []struct {
		name  string
		e     ScannerEntry
		u     uint32
		valid bool
		in    []uint8 // sub-indices of e.Index covered
		out   []uint8 // not covered
		str   string
	}{
		{"zero", ScannerEntry{}, 0, true, nil, nil, "0000:00+1"},
		{"single", ScannerEntry{Index: 0x2000, SubIndex: 0}, 0x00200000, true, []uint8{0}, []uint8{1}, "2000:00+1"},
		{"block size 1", ScannerEntry{Index: 0x2003, SubIndex: 2, BlockSize: 1}, 0x01200302, true, []uint8{2}, []uint8{1, 3}, "2003:02+1"},
		{"block", ScannerEntry{Index: 0x2003, SubIndex: 1, BlockSize: 8}, 0x08200301, true, []uint8{1, 5, 8}, []uint8{0, 9}, "2003:01+8"},
		{"block to FF", ScannerEntry{Index: 0x3000, SubIndex: 0xF0, BlockSize: 16}, 0x103000F0, true, []uint8{0xF0, 0xFF}, []uint8{0xEF}, "3000:F0+16"},
		{"block past FF", ScannerEntry{Index: 0x3000, SubIndex: 0xF0, BlockSize: 17}, 0x113000F0, false, []uint8{0xFF}, nil, "3000:F0+17"},
		{"index 0", ScannerEntry{SubIndex: 1}, 0x00000001, false, []uint8{1}, nil, "0000:01+1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if u := tt.e.Uint32(); u != tt.u {
				t.Errorf("Uint32 = %08X", u)
			}
			if e := ParseScannerEntry(tt.u); e != tt.e {
				t.Errorf("Parse = %+v", e)
			}
			if err := tt.e.Validate(); (err == nil) != tt.valid {
				t.Errorf("Validate = %v", err)
			} else if err != nil && !errors.Is(err, canopen.AbortInvalidValue) {
				t.Errorf("Validate = %v", err)
			}
			for _, s := range tt.in {
				if !tt.e.Contains(tt.e.Index, s) {
					t.Errorf("does not contain sub %d", s)
				}
			}
			for _, s := range tt.out {
				if tt.e.Contains(tt.e.Index, s) {
					t.Errorf("contains sub %d", s)
				}
			}
			if tt.e.Contains(tt.e.Index+1, tt.e.SubIndex) {
				t.Error("contains another index")
			}
			if s := tt.e.String(); s != tt.str {
				t.Errorf("String = %q", s)
			}
		})
	}
}

func TestDispatcherEntry(t *testing.T) {
	type lookup struct {
		node  canopen.NodeID
		index uint16
		sub   uint8
		ok    bool
		li    uint16
		ls    uint8
	}
	tests := []struct {
		name  string
		e     DispatcherEntry
		u     uint64
		valid bool
		look  []lookup
	}{
		{"zero", DispatcherEntry{}, 0, true, []lookup{{0, 0, 0, true, 0, 0}}},
		{"single", DispatcherEntry{Producer: 5, ProducerIndex: 0x2000, Index: 0x2100, SubIndex: 0},
			0x0021000020000005, true, []lookup{
				{5, 0x2000, 0, true, 0x2100, 0},
				{5, 0x2000, 1, false, 0, 0},
				{6, 0x2000, 0, false, 0, 0},
				{5, 0x2001, 0, false, 0, 0},
			}},
		{"block", DispatcherEntry{Producer: 0x7F, ProducerIndex: 0x2003, ProducerSubIndex: 1, Index: 0x2203, SubIndex: 0x11, BlockSize: 4},
			0x042203112003017F, true, []lookup{
				{0x7F, 0x2003, 0, false, 0, 0},
				{0x7F, 0x2003, 1, true, 0x2203, 0x11},
				{0x7F, 0x2003, 4, true, 0x2203, 0x14},
				{0x7F, 0x2003, 5, false, 0, 0},
			}},
		{"local overflow", DispatcherEntry{Producer: 1, ProducerIndex: 0x2000, ProducerSubIndex: 0, Index: 0x2100, SubIndex: 0xFE, BlockSize: 4},
			0x042100FE20000001, false, []lookup{
				{1, 0x2000, 1, true, 0x2100, 0xFF},
				{1, 0x2000, 2, false, 0, 0},
			}},
		{"producer overflow", DispatcherEntry{Producer: 1, ProducerIndex: 0x2000, ProducerSubIndex: 0xFE, Index: 0x2100, BlockSize: 3},
			0x032100002000FE01, false, nil},
		{"producer 0", DispatcherEntry{ProducerIndex: 0x2000, Index: 0x2100}, 0x0021000020000000, false, nil},
		{"producer 128", DispatcherEntry{Producer: 128, ProducerIndex: 0x2000, Index: 0x2100}, 0x0021000020000080, false, nil},
		{"local index 0", DispatcherEntry{Producer: 1, ProducerIndex: 0x2000}, 0x0000000020000001, false, nil},
		{"producer index 0", DispatcherEntry{Producer: 1, Index: 0x2000}, 0x0020000000000001, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if u := tt.e.Uint64(); u != tt.u {
				t.Errorf("Uint64 = %016X", u)
			}
			if e := ParseDispatcherEntry(tt.u); e != tt.e {
				t.Errorf("Parse = %+v", e)
			}
			if err := tt.e.Validate(); (err == nil) != tt.valid {
				t.Errorf("Validate = %v", err)
			}
			for _, l := range tt.look {
				li, ls, ok := tt.e.Map(l.node, l.index, l.sub)
				if ok != l.ok || li != l.li || ls != l.ls {
					t.Errorf("Map(%d, %04X, %d) = %04X:%02X %v, want %04X:%02X %v", l.node, l.index, l.sub, li, ls, ok, l.li, l.ls, l.ok)
				}
			}
			_ = tt.e.String()
		})
	}
	if s := (DispatcherEntry{Producer: 5, ProducerIndex: 0x2000, Index: 0x2100, SubIndex: 1, BlockSize: 2}).String(); s != "node 5 2000:00+2 -> 2100:01" {
		t.Errorf("String = %q", s)
	}
}

// The layouts of the two entries, field by field.
func TestEntryLayout(t *testing.T) {
	d := ParseDispatcherEntry(0xAA_BBCC_DD_EEFF_11_22)
	want := DispatcherEntry{BlockSize: 0xAA, Index: 0xBBCC, SubIndex: 0xDD, ProducerIndex: 0xEEFF, ProducerSubIndex: 0x11, Producer: 0x22}
	if d != want {
		t.Errorf("dispatcher %+v", d)
	}
	s := ParseScannerEntry(0xAA_BBCC_DD)
	if s != (ScannerEntry{BlockSize: 0xAA, Index: 0xBBCC, SubIndex: 0xDD}) {
		t.Errorf("scanner %+v", s)
	}
}

func FuzzEntries(f *testing.F) {
	f.Add(uint64(0x042203112003017F))
	f.Add(uint64(0))
	f.Add(uint64(0xFFFFFFFFFFFFFFFF))
	f.Fuzz(func(t *testing.T, u uint64) {
		d := ParseDispatcherEntry(u)
		if d.Uint64() != u {
			t.Fatalf("dispatcher round trip %016X -> %016X", u, d.Uint64())
		}
		if d.Validate() == nil && d != (DispatcherEntry{}) {
			for k := 0; k < blockLen(d.BlockSize); k++ {
				li, ls, ok := d.Map(d.Producer, d.ProducerIndex, d.ProducerSubIndex+uint8(k))
				if !ok || li != d.Index || int(ls) != int(d.SubIndex)+k {
					t.Fatalf("%v: k=%d -> %04X:%02X %v", d, k, li, ls, ok)
				}
			}
		}
		s := ParseScannerEntry(uint32(u))
		if s.Uint32() != uint32(u) {
			t.Fatalf("scanner round trip %08X", uint32(u))
		}
		if s.Validate() == nil && s != (ScannerEntry{}) && !s.Contains(s.Index, s.SubIndex) {
			t.Fatalf("%v does not contain its first sub-index", s)
		}
	})
}

func TestNewLists(t *testing.T) {
	good := ScannerEntry{Index: 0x2000}
	goodD := DispatcherEntry{Producer: 1, ProducerIndex: 0x2000, Index: 0x2000}
	for _, tc := range []struct {
		name       string
		list, size int
		n          int
		bad        bool
	}{
		{"list 0", 0, 4, 0, false},
		{"list 49", 49, 4, 0, false},
		{"size 0", 1, 0, 0, false},
		{"size 255", 1, 255, 0, false},
		{"too many", 1, 1, 2, false},
		{"bad entry", 1, 4, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := make([]ScannerEntry, tc.n)
			de := make([]DispatcherEntry, tc.n)
			for i := range se {
				se[i], de[i] = good, goodD
			}
			if tc.bad {
				se[0], de[0] = ScannerEntry{SubIndex: 1}, DispatcherEntry{Producer: 1}
			}
			if _, err := NewScannerList(tc.list, tc.size, se...); err == nil {
				t.Error("scanner list accepted")
			}
			if _, err := NewDispatcherList(tc.list, tc.size, de...); err == nil {
				t.Error("dispatcher list accepted")
			}
		})
	}
	s, err := NewScannerList(48, 254, good)
	if err != nil || s.Index != IndexScannerLast || len(s.Subs) != 255 {
		t.Fatalf("%v %v", s, err)
	}
	dl, err := NewDispatcherList(48, 1, goodD)
	if err != nil || dl.Index != IndexDispatcherLast || dl.Subs[1].DataType != canopen.Unsigned64 {
		t.Fatalf("%v %v", dl, err)
	}
}

func TestListLookup(t *testing.T) {
	d := device(t, 5)
	if err := d.Add(od.NewVar(IndexScanner+5, "not an array", canopen.Unsigned8, od.AccessRO, uint8(3))); err != nil {
		t.Fatal(err)
	}
	s1, err1 := NewScannerList(1, 4, ScannerEntry{Index: idxU8}, ScannerEntry{Index: idxArr, SubIndex: 1, BlockSize: 4})
	addObj(t, d, s1, err1)
	s3, err3 := NewScannerList(3, 4, ScannerEntry{Index: idxU32})
	addObj(t, d, s3, err3)
	d1, errd := NewDispatcherList(1, 4,
		DispatcherEntry{Producer: 9, ProducerIndex: 0x2000, Index: idxU8},
		DispatcherEntry{Producer: 9, ProducerIndex: 0x2003, ProducerSubIndex: 1, Index: idxArr, SubIndex: 5, BlockSize: 2},
		DispatcherEntry{Producer: 9, ProducerIndex: 0x2003, ProducerSubIndex: 1, Index: idxU32, BlockSize: 8}, // shadowed
	)
	addObj(t, d, d1, errd)

	if got := ScannerEntries(d); len(got) != 3 || got[2].Index != idxU32 {
		t.Errorf("ScannerEntries = %v", got)
	}
	if got := DispatcherEntries(d); len(got) != 3 {
		t.Errorf("DispatcherEntries = %v", got)
	}
	for _, tc := range []struct {
		index uint16
		sub   uint8
		want  bool
	}{{idxU8, 0, true}, {idxArr, 4, true}, {idxArr, 5, false}, {idxU32, 0, true}, {idxU16, 0, false}} {
		if got := Scanned(d, tc.index, tc.sub); got != tc.want {
			t.Errorf("Scanned(%04X:%02X) = %v", tc.index, tc.sub, got)
		}
	}
	for _, tc := range []struct {
		node  canopen.NodeID
		index uint16
		sub   uint8
		li    uint16
		ls    uint8
		ok    bool
	}{
		{9, 0x2000, 0, idxU8, 0, true},
		{9, 0x2003, 2, idxArr, 6, true},
		{9, 0x2003, 3, idxU32, 2, true}, // second block only
		{8, 0x2000, 0, 0, 0, false},
	} {
		li, ls, ok := Dispatch(d, tc.node, tc.index, tc.sub)
		if li != tc.li || ls != tc.ls || ok != tc.ok {
			t.Errorf("Dispatch(%d, %04X:%02X) = %04X:%02X %v", tc.node, tc.index, tc.sub, li, ls, ok)
		}
	}
	// Entries past sub-index 00h are not in use.
	if err := d.SetUint(IndexDispatcher, 0, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Dispatch(d, 9, 0x2003, 2); ok {
		t.Error("entry past sub 00h used")
	}
}

func TestProtectLists(t *testing.T) {
	d := device(t, 5)
	s, err := NewScannerList(2, 3)
	addObj(t, d, s, err)
	dl, err := NewDispatcherList(2, 3)
	addObj(t, d, dl, err)
	before := d.HookCount()
	unprotect := ProtectLists(d)
	if d.HookCount() != before+2 {
		t.Fatalf("hooks %d -> %d", before, d.HookCount())
	}
	si, di := IndexScanner+1, IndexDispatcher+1
	tests := []struct {
		name  string
		index uint16
		sub   uint8
		x     uint64
		size  int
		want  error
	}{
		{"scanner entry", si, 1, uint64(ScannerEntry{Index: idxU8}.Uint32()), 4, nil},
		{"scanner zero", si, 2, 0, 4, nil},
		{"scanner overflow", si, 2, uint64(ScannerEntry{Index: idxU8, SubIndex: 0xFF, BlockSize: 2}.Uint32()), 4, canopen.AbortInvalidValue},
		{"scanner count", si, 0, 3, 1, nil},
		{"scanner count high", si, 0, 4, 1, canopen.AbortValueHigh},
		{"dispatcher entry", di, 1, DispatcherEntry{Producer: 3, ProducerIndex: 1, Index: 2}.Uint64(), 8, nil},
		{"dispatcher producer 0", di, 1, DispatcherEntry{ProducerIndex: 1, Index: 2}.Uint64(), 8, canopen.AbortInvalidValue},
		{"dispatcher count", di, 0, 2, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := d.Write(tt.index, tt.sub, le(tt.x, tt.size))
			if !errors.Is(err, tt.want) {
				t.Errorf("Write = %v, want %v", err, tt.want)
			}
		})
	}
	unprotect()
	unprotect()
	if d.HookCount() != before {
		t.Errorf("hooks after unprotect: %d", d.HookCount())
	}
	if err := d.Write(di, 1, le(DispatcherEntry{ProducerIndex: 1, Index: 2}.Uint64(), 8)); err != nil {
		t.Errorf("unprotected write: %v", err)
	}
}

func TestModeAndNumbers(t *testing.T) {
	for m, s := range map[Mode]string{ModeNone: "none", ModeSAM: "SAM", ModeDAM: "DAM", Mode(9): "Mode(9)"} {
		if m.String() != s {
			t.Errorf("%d: %q", m, m.String())
		}
	}
	d := device(t, 5)
	setMPDO(t, d, true, 1, 0x181, ModeSAM)
	setMPDO(t, d, true, 2, 0x182, ModeDAM)
	setMPDO(t, d, false, 2, 0x202, ModeDAM)
	tp, rp := Numbers(d)
	if !reflect.DeepEqual(tp, []int{1, 2}) || !reflect.DeepEqual(rp, []int{2}) {
		t.Errorf("Numbers = %v %v", tp, rp)
	}
	for _, tc := range []struct {
		transmit bool
		n        int
		want     Mode
	}{{true, 1, ModeSAM}, {true, 2, ModeDAM}, {false, 1, ModeNone}, {true, 3, ModeNone}, {true, 0, ModeNone}, {true, 513, ModeNone}} {
		if got := ModeOf(d, tc.transmit, tc.n); got != tc.want {
			t.Errorf("ModeOf(%v, %d) = %v", tc.transmit, tc.n, got)
		}
	}
}
