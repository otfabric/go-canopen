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
