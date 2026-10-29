// SPDX-License-Identifier: MIT

package od

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-canopen"
)

func testDict(t *testing.T) *Dictionary {
	t.Helper()
	d, err := Standard(Config{
		NodeID: 5, DeviceType: 0x00020192, VendorID: 0x1234, ProductCode: 2, Revision: 3, SerialNumber: 4,
		DeviceName: "dev", HardwareVersion: "hw1", SoftwareVersion: "sw1", HeartbeatMs: 100,
		HeartbeatConsumers: 2, ErrorHistory: 4, RPDOs: 5, TPDOs: 5, SDOClients: 1, NodeGuarding: true, Time: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	u8 := NewVar(0x2000, "u8", canopen.Unsigned8, AccessRW, uint8(7))
	u8.Subs[0].Low, u8.Subs[0].High = []byte{1}, []byte{100}
	i16 := NewVar(0x2001, "i16", canopen.Integer16, AccessRW, int16(-5))
	i16.Subs[0].Low, i16.Subs[0].High = []byte{0xF6, 0xFF}, []byte{10, 0}
	f32 := NewVar(0x2002, "f32", canopen.Real32, AccessRW, float32(0))
	f32.Subs[0].Low, f32.Subs[0].High = mustEncode(canopen.Real32, float32(-1)), mustEncode(canopen.Real32, float32(1))
	str := NewVar(0x2003, "str", canopen.VisibleString, AccessRW, "hi")
	str.Subs[0].MaxSize = 4
	for _, o := range []*Object{
		u8, i16, f32, str,
		NewVar(0x2004, "wo", canopen.Unsigned32, AccessWO, nil),
		NewVar(0x2005, "bool", canopen.Boolean, AccessRWW, true),
		NewVar(0x2006, "dom", canopen.Domain, AccessRW, []byte{1, 2}),
		NewRecord(0x2007, "rec", Sub(1, "a", canopen.Unsigned8, AccessRO, uint8(1)), Sub(3, "c", canopen.Unsigned16, AccessRWR, uint16(3))),
		NewVar(0x2008, "f64", canopen.Real64, AccessRW, 0.5),
	} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestStandardDefaults(t *testing.T) {
	d := testDict(t)
	for _, tc := range []struct {
		idx  uint16
		sub  uint8
		want uint64
	}{
		{0x1000, 0, 0x00020192},
		{0x1005, 0, 0x80},
		{0x1014, 0, 0x85},
		{0x1017, 0, 100},
		{0x1018, 0, 4},
		{0x1018, 1, 0x1234},
		{0x1018, 4, 4},
		{0x1200, 1, 0x605},
		{0x1200, 2, 0x585},
		{0x1280, 1, 0x80000000},
		{0x1400, 1, 0x205},
		{0x1403, 1, 0x505},
		{0x1404, 1, 0x80000000},
		{0x1800, 1, 0x185},
		{0x1800, 2, 255},
		{0x1800, 0, 6},
		{0x1400, 0, 5},
		{0x1A00, 0, 0},
		{0x1A04, 8, 0},
		{0x1003, 0, 4},
		{0x1016, 0, 2},
		{0x1012, 0, 0x100},
	} {
		got, err := d.GetUint(tc.idx, tc.sub)
		if err != nil || got != tc.want {
			t.Errorf("%04X:%02X = %#x, %v; want %#x", tc.idx, tc.sub, got, err, tc.want)
		}
	}
	if s, _ := d.GetValue(0x1008, 0); s != "dev" {
		t.Errorf("device name %v", s)
	}
	if d.NodeID() != 5 || len(d.Objects()) != 49 {
		t.Errorf("node %d objects %d", d.NodeID(), len(d.Objects()))
	}
	if d.Object(0x1A00).Subs[0].Name != "Number of mapped objects" || d.Object(0x1A00).Type != ObjectRecord {
		t.Error("mapping object")
	}
}

func TestStandardLimits(t *testing.T) {
	for _, c := range []Config{{RPDOs: 513}, {TPDOs: 513}, {MappingEntries: 65}, {SDOClients: 129}, {HeartbeatConsumers: 128}, {ErrorHistory: 255}} {
		if _, err := Standard(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestRemoteAccess(t *testing.T) {
	d := testDict(t)
	for _, tc := range []struct {
		name string
		idx  uint16
		sub  uint8
		data []byte
		want error
	}{
		{"no object", 0x3000, 0, []byte{1}, canopen.AbortNoObject},
		{"no sub", 0x2007, 2, []byte{1}, canopen.AbortNoSubIndex},
		{"read only", 0x1000, 0, []byte{1, 2, 3, 4}, canopen.AbortReadOnly},
		{"const", 0x1008, 0, []byte("x"), canopen.AbortReadOnly},
		{"too long", 0x1017, 0, []byte{1, 2, 3}, canopen.AbortLengthHigh},
		{"too short", 0x1017, 0, []byte{1}, canopen.AbortLengthLow},
		{"below", 0x2000, 0, []byte{0}, canopen.AbortValueLow},
		{"above", 0x2000, 0, []byte{101}, canopen.AbortValueHigh},
		{"signed below", 0x2001, 0, []byte{0xF5, 0xFF}, canopen.AbortValueLow},
		{"signed above", 0x2001, 0, []byte{11, 0}, canopen.AbortValueHigh},
		{"float above", 0x2002, 0, mustEncode(canopen.Real32, float32(1.5)), canopen.AbortValueHigh},
		{"float below", 0x2002, 0, mustEncode(canopen.Real32, float32(-1.5)), canopen.AbortValueLow},
		{"string too long", 0x2003, 0, []byte("hello"), canopen.AbortLengthHigh},
		{"bool 2", 0x2005, 0, []byte{2}, canopen.AbortInvalidValue},
		{"ok u8", 0x2000, 0, []byte{100}, nil},
		{"ok i16", 0x2001, 0, []byte{0xF6, 0xFF}, nil},
		{"ok float", 0x2002, 0, mustEncode(canopen.Real32, float32(0.25)), nil},
		{"ok string", 0x2003, 0, []byte("abcd"), nil},
		{"ok wo", 0x2004, 0, []byte{1, 2, 3, 4}, nil},
		{"ok domain", 0x2006, 0, bytes.Repeat([]byte{9}, 1000), nil},
		{"ok rwr", 0x2007, 3, []byte{1, 0}, nil},
		{"ok f64", 0x2008, 0, mustEncode(canopen.Real64, 0.75), nil},
	} {
		err := d.Write(tc.idx, tc.sub, tc.data)
		if !errors.Is(err, tc.want) && err != tc.want {
			t.Errorf("%s: Write = %v, want %v", tc.name, err, tc.want)
			continue
		}
		if err == nil && tc.idx != 0x2004 {
			got, err := d.Read(tc.idx, tc.sub)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Errorf("%s: Read = %x, %v", tc.name, got, err)
			}
		}
	}
	if _, err := d.Read(0x2004, 0); err != canopen.AbortWriteOnly {
		t.Error(err)
	}
	for _, k := range []struct {
		idx uint16
		sub uint8
		err error
	}{{0x3000, 0, canopen.AbortNoObject}, {0x2007, 9, canopen.AbortNoSubIndex}} {
		if _, err := d.Read(k.idx, k.sub); err != k.err {
			t.Error(err)
		}
		if _, err := d.Get(k.idx, k.sub); err != k.err {
			t.Error(err)
		}
		if err := d.Set(k.idx, k.sub, nil); err != k.err {
			t.Error(err)
		}
		if _, err := d.GetValue(k.idx, k.sub); err != k.err {
			t.Error(err)
		}
		if err := d.SetValue(k.idx, k.sub, 1); err != k.err {
			t.Error(err)
		}
		if _, err := d.GetUint(k.idx, k.sub); err != k.err {
			t.Error(err)
		}
	}
}

func TestLocalAccess(t *testing.T) {
	d := testDict(t)
	// Local writes bypass access rights and limits, not the length.
	if err := d.Set(0x1000, 0, []byte{9, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(0x2000, 0, []byte{200}); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(0x2000, 0, []byte{1, 2}); !errors.Is(err, canopen.ErrDataType) {
		t.Fatal(err)
	}
	if err := d.SetUint(0x1017, 0, 70000); !errors.Is(err, canopen.ErrDataType) || !strings.Contains(err.Error(), "1017:00") {
		t.Fatal(err)
	}
	if err := d.SetValue(0x2003, 0, "longer than four"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUint(0x1017, 0, 500); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetValue(0x1017, 0); v != uint64(500) {
		t.Fatal(v)
	}
	if v, _ := d.GetValue(0x2001, 0); v != int64(-5) {
		t.Fatal(v)
	}
	// A nil value of a variable type stores an empty value.
	if err := d.Set(0x2006, 0, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.Get(0x2006, 0); b == nil || len(b) != 0 {
		t.Fatalf("%#v", b)
	}
}

func TestHooksAndWatchers(t *testing.T) {
	d := testDict(t)
	var order []string
	d.OnWrite(0x2007, -1, func(v *Variable, data []byte) error { order = append(order, "all"); return nil })
	d.OnWrite(0x2007, 3, func(v *Variable, data []byte) error {
		order = append(order, "sub3")
		if data[0] == 0xFF {
			return canopen.AbortDeviceState
		}
		return nil
	})
	var seen []string
	cancel := d.Watch(func(v *Variable, data []byte) { seen = append(seen, v.Key()) })
	if err := d.Write(0x2007, 3, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "sub3,all" {
		t.Fatal(order)
	}
	if err := d.Write(0x2007, 3, []byte{0xFF, 0}); err != canopen.AbortDeviceState {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(0x2007, 3); v != 1 {
		t.Fatal("refused write stored", v)
	}
	_ = d.Set(0x2000, 0, []byte{3})
	cancel()
	_ = d.Set(0x2000, 0, []byte{4})
	if strings.Join(seen, ",") != "2007:03,2000:00" {
		t.Fatal(seen)
	}
	d.OnRead(0x2000, 0, func(v *Variable, stored []byte) ([]byte, error) { return []byte{stored[0] + 1}, nil })
	if b, _ := d.Read(0x2000, 0); b[0] != 5 {
		t.Fatal(b)
	}
	d.OnRead(0x2000, -1, func(v *Variable, stored []byte) ([]byte, error) { return nil, canopen.AbortNoData })
	if _, err := d.Read(0x2000, 0); err != canopen.AbortNoData {
		t.Fatal(err)
	}
	// Hooks may use the dictionary (no lock held).
	d.OnWrite(0x2001, 0, func(v *Variable, data []byte) error { return d.Set(0x2000, 0, []byte{50}) })
	if err := d.Write(0x2001, 0, []byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(0x2000, 0); v != 50 {
		t.Fatal(v)
	}
}

func TestResetAndNodeID(t *testing.T) {
	d := testDict(t)
	_ = d.Set(0x1017, 0, []byte{0, 0})
	_ = d.Set(0x2000, 0, []byte{99})
	var changed []string
	d.Watch(func(v *Variable, _ []byte) { changed = append(changed, v.Key()) })
	d.SetNodeID(9)
	d.Reset(0x1000, 0x1FFF)
	if v, _ := d.GetUint(0x1017, 0); v != 100 {
		t.Fatal(v)
	}
	if v, _ := d.GetUint(0x1800, 1); v != 0x189 {
		t.Fatalf("%x", v)
	}
	if v, _ := d.GetUint(0x2000, 0); v != 99 {
		t.Fatal("application area reset", v)
	}
	d.Reset(0x2000, 0x9FFF)
	if v, _ := d.GetUint(0x2000, 0); v != 7 {
		t.Fatal(v)
	}
	if len(changed) == 0 {
		t.Fatal("no watcher calls")
	}
	if b := d.DefaultValue(d.Object(0x1200).Sub(1)); canopen.DecodeUint(b) != 0x609 {
		t.Fatalf("%x", b)
	}
	d.Remove(0x2000)
	d.Remove(0x2000)
	if d.Object(0x2000) != nil {
		t.Fatal("not removed")
	}
}

func TestAddChecks(t *testing.T) {
	d := New(1)
	bad := []*Object{
		nil,
		{Index: 1, Type: ObjectVar},
		{Index: 2, Type: ObjectVar, Subs: []*Variable{{Index: 2, SubIndex: 1, DataType: canopen.Unsigned8, Access: AccessRW}}},
		{Index: 3, Type: ObjectRecord, Subs: []*Variable{nil}},
		{Index: 4, Type: ObjectRecord, Subs: []*Variable{{Index: 5, DataType: canopen.Unsigned8, Access: AccessRW}}},
		{Index: 6, Type: ObjectRecord, Subs: []*Variable{{Index: 6, SubIndex: 1, DataType: canopen.Unsigned8, Access: AccessRW}, {Index: 6, SubIndex: 1, DataType: canopen.Unsigned8, Access: AccessRW}}},
		{Index: 7, Type: ObjectVar, Subs: []*Variable{{Index: 7, DataType: canopen.PDOMapping, Access: AccessRW}}},
		{Index: 8, Type: ObjectVar, Subs: []*Variable{{Index: 8, DataType: canopen.Unsigned8}}},
		{Index: 9, Type: ObjectVar, Subs: []*Variable{{Index: 9, DataType: canopen.Unsigned8, Access: AccessRW, Default: []byte{1, 2}}}},
		{Index: 10, Type: ObjectVar, Subs: []*Variable{{Index: 10, DataType: canopen.VisibleString, Access: AccessRW, NodeIDRelative: true}}},
	}
	for i, o := range bad {
		if err := d.Add(o); err == nil {
			t.Errorf("bad object %d accepted", i)
		}
	}
	if err := d.Add(NewVar(0x2000, "a", canopen.Unsigned8, AccessRW, nil)); err != nil {
		t.Fatal(err)
	}
	if err := d.Add(NewVar(0x2000, "a", canopen.Unsigned8, AccessRW, nil)); err == nil {
		t.Fatal("duplicate accepted")
	}
	if NewRecord(0x3000, "empty").Subs[0].Default[0] != 0 {
		t.Fatal("empty record sub 0")
	}
	r := NewRecord(0x3001, "own sub0", Sub(0, "n", canopen.Unsigned8, AccessRW, uint8(9)))
	if len(r.Subs) != 1 || r.Subs[0].Index != 0x3001 {
		t.Fatal(r.Subs)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("bad default did not panic")
		}
	}()
	Sub(1, "x", canopen.Unsigned8, AccessRW, 300)
}

func TestNames(t *testing.T) {
	for typ, s := range map[ObjectType]string{ObjectNull: "NULL", ObjectDomain: "DOMAIN", ObjectDefType: "DEFTYPE", ObjectDefStruct: "DEFSTRUCT", ObjectVar: "VAR", ObjectArray: "ARRAY", ObjectRecord: "RECORD", 1: "ObjectType(1)"} {
		if typ.String() != s {
			t.Errorf("%q", typ.String())
		}
	}
	for _, a := range []Access{AccessRO, AccessWO, AccessRW, AccessRWR, AccessRWW, AccessConst} {
		got, err := ParseAccess(strings.ToUpper(a.String()))
		if err != nil || got != a {
			t.Error(a, err)
		}
	}
	if _, err := ParseAccess("rx"); err == nil || Access(0).String() != "Access(0)" {
		t.Error("bad access")
	}
	if !AccessRWR.Readable() || AccessWO.Readable() || !AccessWO.Writable() || AccessConst.Writable() || AccessRO.Writable() {
		t.Error("access rules")
	}
	if (&Variable{Index: 0x1018, SubIndex: 4}).Key() != "1018:04" {
		t.Error("key")
	}
}

func TestConcurrent(t *testing.T) {
	d := testDict(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = d.Write(0x2000, 0, []byte{byte(1 + (i+j)%100)})
				_, _ = d.Read(0x2000, 0)
				_ = d.Objects()
				c := d.Watch(func(*Variable, []byte) {})
				c()
			}
		}(i)
	}
	wg.Wait()
}
