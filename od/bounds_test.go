// SPDX-License-Identifier: MIT

package od

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-canopen"
)

// A negative element count used to give sub-index 0 the value 255 (uint8
// wrap) on an array without elements.
func TestNewArrayNegative(t *testing.T) {
	o := NewArray(0x2000, "a", canopen.Unsigned8, AccessRW, -1, nil, AccessConst)
	if len(o.Subs) != 1 || !bytes.Equal(o.Subs[0].Default, []byte{0}) {
		t.Fatalf("subs %d, sub 0 default %x; want 1, 00", len(o.Subs), o.Subs[0].Default)
	}
}

// 255 elements fit; 256 repeat a sub-index and Add rejects the object
// instead of wrapping to sub-index 0.
func TestNewArrayLimit(t *testing.T) {
	d := New(1)
	o := NewArray(0x2000, "a", canopen.Unsigned8, AccessRW, 255, nil, AccessConst)
	if err := d.Add(o); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Get(0x2000, 0); err != nil || !bytes.Equal(b, []byte{255}) {
		t.Fatalf("sub 0 = %x, %v", b, err)
	}
	if err := d.Add(NewArray(0x2001, "b", canopen.Unsigned8, AccessRW, 256, nil, AccessConst)); err == nil {
		t.Fatal("256 elements accepted")
	}
}

// A PDO number outside 1..512 used to wrap the index (n 0 gave 0x13FF).
func TestPDOObjectsOutOfRange(t *testing.T) {
	for _, n := range []int{0, -3, MaxPDOs + 1} {
		comm, mapping := PDOObjects(false, n, 8)
		if comm.Index != IndexRPDOComm || mapping.Index != IndexRPDOMapping {
			t.Errorf("n %d: indices 0x%04X 0x%04X", n, comm.Index, mapping.Index)
		}
	}
	comm, mapping := PDOObjects(true, MaxPDOs, 8)
	if comm.Index != IndexTPDOComm+MaxPDOs-1 || mapping.Index != IndexTPDOMapping+MaxPDOs-1 {
		t.Errorf("TPDO 512: indices 0x%04X 0x%04X", comm.Index, mapping.Index)
	}
}

// A node-ID-relative default adds the node-ID across all bytes.
func TestNodeRelativeDefaultBytes(t *testing.T) {
	d := New(0x7F)
	v := Sub(0, "x", canopen.Unsigned64, AccessRW, uint64(0x00000001_FFFFFF90))
	v.NodeIDRelative = true
	o := &Object{Index: 0x2000, Name: "x", Type: ObjectVar, Subs: []*Variable{v}}
	v.Index = 0x2000
	if err := d.Add(o); err != nil {
		t.Fatal(err)
	}
	b, err := d.Get(0x2000, 0)
	if want := []byte{0x0F, 0, 0, 0, 2, 0, 0, 0}; err != nil || !bytes.Equal(b, want) {
		t.Fatalf("got %x, %v; want %x", b, err, want)
	}
}

// A node-ID-relative default that cannot hold every valid node-ID is
// refused, at each width and for signed types; one that just fits is
// accepted and never wraps.
func TestNodeRelativeDefaultRange(t *testing.T) {
	cases := []struct {
		dt  canopen.DataType
		def any
		ok  bool
	}{
		{canopen.Unsigned8, uint8(0xFF - 127), true},
		{canopen.Unsigned8, uint8(0xFF - 126), false},
		{canopen.Unsigned8, uint8(0xF0), false},
		{canopen.Unsigned16, uint16(0xFFFF - 127), true},
		{canopen.Unsigned16, uint16(0xFFFF - 126), false},
		{canopen.Unsigned32, uint32(0xFFFFFFFF - 127), true},
		{canopen.Unsigned32, uint32(0xFFFFFFFF - 126), false},
		{canopen.Unsigned64, uint64(0xFFFFFFFFFFFFFFFF - 127), true},
		{canopen.Unsigned64, uint64(0xFFFFFFFFFFFFFFFF - 126), false},
		{canopen.Integer8, int8(0), true},
		{canopen.Integer8, int8(1), false},
		{canopen.Integer8, int8(-1), true},
		{canopen.Integer8, int8(-128), true},
		{canopen.Integer16, int16(0x7FFF - 127), true},
		{canopen.Integer16, int16(0x7FFF - 126), false},
		{canopen.Integer64, int64(0x7FFFFFFFFFFFFFFF - 127), true},
		{canopen.Integer64, int64(0x7FFFFFFFFFFFFFFF - 126), false},
	}
	for _, tc := range cases {
		v := Sub(0, "x", tc.dt, AccessRW, tc.def)
		v.Index, v.NodeIDRelative = 0x2000, true
		d := New(canopen.MaxNodeID)
		err := d.Add(&Object{Index: 0x2000, Name: "x", Type: ObjectVar, Subs: []*Variable{v}})
		if (err == nil) != tc.ok {
			t.Errorf("%s %v: err %v, want ok %v", tc.dt, tc.def, err, tc.ok)
			continue
		}
		if err != nil {
			continue
		}
		b, _ := d.Get(0x2000, 0)
		want, _ := canopen.Encode(tc.dt, tc.def)
		if tc.dt.IsSigned() {
			if got := canopen.DecodeInt(b); got != canopen.DecodeInt(want)+127 {
				t.Errorf("%s %v: got %d", tc.dt, tc.def, got)
			}
		} else if got := canopen.DecodeUint(b); got != canopen.DecodeUint(want)+127 {
			t.Errorf("%s %v: got %d", tc.dt, tc.def, got)
		}
	}
}

// A node-ID above 127 (LSS unconfigured, 0xFF) never wraps a default:
// when the sum would overflow, the default is kept unchanged.
func TestNodeRelativeDefaultUnconfigured(t *testing.T) {
	v := Sub(0, "x", canopen.Unsigned8, AccessRW, uint8(0x80))
	v.Index, v.NodeIDRelative = 0x2000, true
	d := New(canopen.MaxNodeID)
	if err := d.Add(&Object{Index: 0x2000, Name: "x", Type: ObjectVar, Subs: []*Variable{v}}); err != nil {
		t.Fatal(err)
	}
	d.SetNodeID(0xFF)
	if b := d.DefaultValue(v); !bytes.Equal(b, []byte{0x80}) {
		t.Fatalf("node 0xFF: got %x, want 80", b)
	}
	d.SetNodeID(0x10)
	if b := d.DefaultValue(v); !bytes.Equal(b, []byte{0x90}) {
		t.Fatalf("node 0x10: got %x, want 90", b)
	}
}
