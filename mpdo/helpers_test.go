// SPDX-License-Identifier: MIT

package mpdo

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Application objects of the test devices.
const (
	idxU8    uint16 = 0x2000 // UNSIGNED8, rw, mappable
	idxU16   uint16 = 0x2001 // UNSIGNED16, rw, mappable, limited to <= 1000
	idxU32   uint16 = 0x2002 // UNSIGNED32, rw, mappable
	idxArr   uint16 = 0x2003 // ARRAY of 8 UNSIGNED16, rw, mappable
	idxRO    uint16 = 0x2100 // UNSIGNED16, ro, mappable
	idxNoMap uint16 = 0x2101 // UNSIGNED8, rw, not mappable
	idxU64   uint16 = 0x2102 // UNSIGNED64, rw, mappable
	idxStr   uint16 = 0x2103 // VISIBLE_STRING, rw, mappable
	idxBool  uint16 = 0x2104 // BOOLEAN, rw, mappable
)

// device returns a dictionary of node with 2 TPDOs and 2 RPDOs and the
// application objects.
func device(t testing.TB, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: node, TPDOs: 2, RPDOs: 2})
	if err != nil {
		t.Fatal(err)
	}
	mappable := func(o *od.Object) *od.Object {
		for _, v := range o.Subs {
			v.PDOMappable = true
		}
		return o
	}
	u16 := mappable(od.NewVar(idxU16, "u16", canopen.Unsigned16, od.AccessRW, nil))
	u16.Subs[0].High = []byte{0xE8, 0x03}
	arr := od.NewArray(idxArr, "arr", canopen.Unsigned16, od.AccessRW, 8, nil, od.AccessConst)
	for _, v := range arr.Subs[1:] {
		v.PDOMappable = true
	}
	objs := []*od.Object{
		mappable(od.NewVar(idxU8, "u8", canopen.Unsigned8, od.AccessRW, nil)),
		u16,
		mappable(od.NewVar(idxU32, "u32", canopen.Unsigned32, od.AccessRW, nil)),
		arr,
		mappable(od.NewVar(idxRO, "ro", canopen.Unsigned16, od.AccessRO, uint16(0xBEEF))),
		od.NewVar(idxNoMap, "nomap", canopen.Unsigned8, od.AccessRW, nil),
		mappable(od.NewVar(idxU64, "u64", canopen.Unsigned64, od.AccessRW, nil)),
		mappable(od.NewVar(idxStr, "str", canopen.VisibleString, od.AccessRW, "abcd")),
		mappable(od.NewVar(idxBool, "flag", canopen.Boolean, od.AccessRW, nil)),
	}
	for _, o := range objs {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// setMPDO configures TPDO (transmit) or RPDO n of d as an MPDO with local
// writes: COB-ID cob, event-driven, mapping sub 00h for mode.
func setMPDO(t testing.TB, d *od.Dictionary, transmit bool, n int, cob canopen.COBID, mode Mode) {
	t.Helper()
	ci, mi := pdo.CommIndex(transmit, n), pdo.MappingIndex(transmit, n)
	sub0 := map[Mode]uint8{ModeNone: 0, ModeSAM: MappingSAM, ModeDAM: MappingDAM}[mode]
	for _, w := range []struct {
		idx uint16
		sub uint8
		x   uint64
	}{{ci, 1, uint64(cob)}, {ci, 2, 254}, {mi, 0, uint64(sub0)}} {
		if err := d.SetUint(w.idx, w.sub, w.x); err != nil {
			t.Fatal(err)
		}
	}
}

func addObj(t testing.TB, d *od.Dictionary, o *od.Object, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Add(o); err != nil {
		t.Fatal(err)
	}
}

func ctx(t testing.TB) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func never(t testing.TB, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func getU(d *od.Dictionary, index uint16, sub uint8) uint64 {
	x, _ := d.GetUint(index, sub)
	return x
}

func le(x uint64, n int) []byte {
	b := binary.LittleEndian.AppendUint64(nil, x)
	return b[:n]
}
