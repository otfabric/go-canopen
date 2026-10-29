// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Application objects of the test devices.
const (
	idxU8    uint16 = 0x2000
	idxI16   uint16 = 0x2001
	idxU32   uint16 = 0x2002
	idxBool  uint16 = 0x2003
	idxReal  uint16 = 0x2004
	idxRO    uint16 = 0x2100 // read-only, mappable: TPDO only
	idxNoMap uint16 = 0x2101 // not mappable
	idxStr   uint16 = 0x2102 // VISIBLE_STRING, mappable flag set
	idxBits  uint16 = 0x2200 // record of 8 BOOLEANs
)

// device returns a dictionary of node with 4 TPDOs and 4 RPDOs and the
// application objects.
func device(t testing.TB, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: node, TPDOs: 4, RPDOs: 4})
	if err != nil {
		t.Fatal(err)
	}
	mappable := func(o *od.Object) *od.Object {
		for _, v := range o.Subs {
			v.PDOMappable = true
		}
		return o
	}
	objs := []*od.Object{
		mappable(od.NewVar(idxU8, "u8", canopen.Unsigned8, od.AccessRW, nil)),
		mappable(od.NewVar(idxI16, "i16", canopen.Integer16, od.AccessRW, nil)),
		mappable(od.NewVar(idxU32, "u32", canopen.Unsigned32, od.AccessRW, nil)),
		mappable(od.NewVar(idxBool, "flag", canopen.Boolean, od.AccessRW, nil)),
		mappable(od.NewVar(idxReal, "real", canopen.Real32, od.AccessRW, nil)),
		mappable(od.NewVar(idxRO, "ro", canopen.Unsigned16, od.AccessRO, nil)),
		od.NewVar(idxNoMap, "nomap", canopen.Unsigned8, od.AccessRW, nil),
		mappable(od.NewVar(idxStr, "str", canopen.VisibleString, od.AccessRW, "x")),
	}
	bits := od.NewArray(idxBits, "bit", canopen.Boolean, od.AccessRW, 8, nil, od.AccessConst)
	for _, v := range bits.Subs[1:] {
		v.PDOMappable = true
	}
	objs = append(objs, bits)
	for _, o := range objs {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// fakeSDO is an SDO client served by a dictionary's remote accesses.
type fakeSDO struct {
	d      *od.Dictionary
	writes []string
	failAt int // fail the nth download (1-based) with AbortGeneral; 0: never
}

func (f *fakeSDO) Upload(_ context.Context, index uint16, sub uint8) ([]byte, error) {
	return f.d.Read(index, sub)
}

func (f *fakeSDO) Download(_ context.Context, index uint16, sub uint8, data []byte) error {
	f.writes = append(f.writes, (&od.Variable{Index: index, SubIndex: sub}).Key())
	if f.failAt == len(f.writes) {
		return canopen.AbortGeneral
	}
	return f.d.Write(index, sub, data)
}

// configure configures a PDO of d through the fake SDO.
func configure(t testing.TB, d *od.Dictionary, transmit bool, n int, cfg Config) {
	t.Helper()
	if err := Configure(context.Background(), &fakeSDO{d: d}, transmit, n, cfg); err != nil {
		t.Fatal(err)
	}
}

// eventually waits until cond holds.
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

// never checks that cond does not become true within d.
func never(t testing.TB, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func u8(d *od.Dictionary, index uint16, sub uint8) uint64 {
	x, _ := d.GetUint(index, sub)
	return x
}

func ctx(t testing.TB) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func canbusID(x uint32) canbus.ID { return canbus.ID(x) }
