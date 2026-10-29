// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

func mustDict(t testing.TB, path string, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := mustParse(t, path).Dictionary(node)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func getU(t testing.TB, d *od.Dictionary, index uint16, sub uint8) uint64 {
	t.Helper()
	x, err := d.GetUint(index, sub)
	if err != nil {
		t.Fatalf("%04X:%02X: %v", index, sub, err)
	}
	return x
}

func TestDictionaryIO401(t *testing.T) {
	d := mustDict(t, "testdata/io401.eds", 5)
	if len(d.Objects()) != 28 {
		t.Errorf("%d objects", len(d.Objects()))
	}
	for _, c := range []struct {
		index uint16
		sub   uint8
		want  uint64
	}{
		{0x1000, 0, 0x000F0191},
		{0x1014, 0, 0x85},
		{0x1017, 0, 1000},
		{0x1200, 1, 0x605},
		{0x1200, 2, 0x585},
		{0x1800, 1, 0x185},
		{0x1801, 1, 0x285},
		{0x1800, 2, 0xFE},
		{0x1A00, 0, 2},
		{0x1A00, 1, 0x60000108},
		{0x1018, 1, 0xF7A},
	} {
		if got := getU(t, d, c.index, c.sub); got != c.want {
			t.Errorf("%04X:%02X = %#x, want %#x", c.index, c.sub, got, c.want)
		}
	}
	v, _ := d.Variable(0x1014, 0)
	if !v.NodeIDRelative || !bytes.Equal(v.Default, []byte{0x80, 0, 0, 0}) || v.Access != od.AccessRW || v.Name != "COB-ID EMCY" {
		t.Errorf("1014 %+v", v)
	}
	// A node-ID change re-evaluates the relative defaults on reset.
	d.SetNodeID(0x10)
	d.Reset(0x1000, 0x1FFF)
	if got := getU(t, d, 0x1200, 1); got != 0x610 {
		t.Errorf("after SetNodeID: %#x", got)
	}
	if v, _ := d.GetValue(0x1008, 0); v != "OTF IO-401 8DI/8DO 2AI/2AO" {
		t.Errorf("1008 %q", v)
	}
	if v, _ := d.GetValue(0x2000, 0); v != float32(25.5) {
		t.Errorf("2000 %v", v)
	}
	v, _ = d.Variable(0x2000, 0)
	if v.Low == nil || v.High == nil {
		t.Error("REAL32 limits")
	}
	if err := d.Write(0x2000, 0, []byte{0, 0, 0xC8, 0x42}); err != canopen.AbortValueHigh { // 100.0 is the limit; 100.0 itself ok
		if err != nil {
			t.Errorf("write 100.0: %v", err)
		}
	}
	if err := d.Write(0x2100, 0, []byte{0x9B, 0xFF, 0xFF, 0xFF}); err != nil { // -101
		if err != canopen.AbortValueLow {
			t.Errorf("write -101: %v", err)
		}
	} else {
		t.Error("write -101 accepted")
	}
	if v, _ := d.GetValue(0x2100, 0); v != int64(-5) {
		t.Errorf("2100 %v", v)
	}
	o := d.Object(0x2001)
	if o.Type != od.ObjectVar || o.Subs[0].DataType != canopen.Domain || o.Subs[0].Access != od.AccessWO || len(o.Subs[0].Default) != 0 {
		t.Errorf("2001 %+v", o.Subs[0])
	}
	if v, _ := d.Variable(0x6000, 1); !v.PDOMappable || v.Access != od.AccessRO {
		t.Errorf("6000:01 %+v", v)
	}
}

func TestDictionaryDCF(t *testing.T) {
	f := mustParse(t, "testdata/node37.dcf")
	d, err := f.Dictionary(canopen.NodeID(f.DeviceComissioning.NodeID))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		index uint16
		sub   uint8
		want  uint64
	}{
		{0x1017, 0, 500},      // ParameterValue
		{0x1014, 0, 0xA5},     // ParameterValue, absolute
		{0x1800, 1, 0x1A5},    // ParameterValue relative
		{0x1800, 5, 100},      // ParameterValue
		{0x1A00, 0, 1},        // ParameterValue
		{0x1018, 4, 0xABCDEF}, // ParameterValue
		{0x1801, 1, 0x2A5},    // DefaultValue
	} {
		if got := getU(t, d, c.index, c.sub); got != c.want {
			t.Errorf("%04X:%02X = %#x, want %#x", c.index, c.sub, got, c.want)
		}
	}
	if v, _ := d.Variable(0x1014, 0); v.NodeIDRelative {
		t.Error("absolute ParameterValue made relative")
	}
	if v, _ := d.GetValue(0x2000, 0); v != float32(12.5) {
		t.Errorf("2000 %v", v)
	}
}

func TestDictionaryCompact(t *testing.T) {
	d := mustDict(t, "testdata/compact.eds", 1)
	for _, index := range []uint16{0x0020, 0x0040} {
		if d.Object(index) != nil {
			t.Errorf("type definition 0x%04X in the dictionary", index)
		}
	}
	o := d.Object(0x1003)
	if o.Type != od.ObjectArray || len(o.Subs) != 9 || o.Subs[0].Name != CompactSub0Name || o.Subs[0].Default[0] != 8 ||
		o.Subs[0].Access != od.AccessRO || o.Subs[8].Name != "Pre-defined error field_8" || o.Subs[8].DataType != canopen.Unsigned32 {
		t.Errorf("1003 %+v %+v", o.Subs[0], o.Subs[8])
	}
	o = d.Object(0x1016)
	if len(o.Subs) != 4 || o.Subs[1].Name != "Heartbeat of the manager" || o.Subs[2].Name != "Consumer heartbeat time_2" || o.Subs[3].Name != "Heartbeat of the peer" {
		t.Errorf("1016 names %v %v %v", o.Subs[1].Name, o.Subs[2].Name, o.Subs[3].Name)
	}
	if getU(t, d, 0x1016, 1) != 0x000103E8 || getU(t, d, 0x1016, 2) != 0 {
		t.Error("1016 values")
	}
	if v, _ := d.GetValue(0x2008, 2); v != int64(100) {
		t.Errorf("2008:2 %v", v)
	}
	if v, _ := d.GetValue(0x2008, 3); v != int64(-1) {
		t.Errorf("2008:3 %v", v)
	}
	if v, _ := d.Variable(0x2008, 4); v.Access != od.AccessRWW || !v.PDOMappable || v.Name != "Setpoint D" {
		t.Errorf("2008:4 %+v", v)
	}
	if o := d.Object(0x1021); o.Type != od.ObjectDomain || o.Subs[0].DataType != canopen.Domain {
		t.Errorf("1021 %+v", o.Subs[0])
	}
	if v, _ := d.GetValue(0x2001, 0); v != int64(-8388608) {
		t.Errorf("2001 %v", v)
	}
	if v, _ := d.Variable(0x2001, 0); canopen.DecodeInt(v.Low) != -8388608 || canopen.DecodeInt(v.High) != 8388607 {
		t.Errorf("2001 limits %x %x", v.Low, v.High)
	}
	if v, _ := d.GetValue(0x2002, 0); v != 1.0000001 {
		t.Errorf("2002 %v", v)
	}
	if v, _ := d.Get(0x2003, 0); !bytes.Equal(v, []byte{2, 0, 0x5E, 0x10, 0, 1}) {
		t.Errorf("2003 %x", v)
	}
	if v, _ := d.GetValue(0x2004, 0); v != "Grüße" {
		t.Errorf("2004 %q", v)
	}
	if v, _ := d.Variable(0x2006, 0); v.MaxSize != 32 {
		t.Errorf("2006 MaxSize %d", v.MaxSize)
	}
	if v, _ := d.Variable(0x2004, 0); v.MaxSize != 0 {
		t.Errorf("2004 MaxSize %d", v.MaxSize)
	}
	if getU(t, d, 0x2007, 0) != 0755 {
		t.Error("octal")
	}
}

func TestDictionaryErrors(t *testing.T) {
	base := "[1000]\nParameterName=a\nDataType=7\nAccessType=ro\n"
	for _, c := range []struct {
		name, src string
	}{
		{"unknown type", "[2000]\nDataType=0x40\nAccessType=rw\n"},
		{"no access", "[2000]\nDataType=5\n"},
		{"bad default", "[2000]\nDataType=5\nAccessType=rw\nDefaultValue=x\n"},
		{"node string", "[2000]\nDataType=9\nAccessType=rw\nDefaultValue=$NODEID+1\n"},
		{"bad limit", "[2000]\nDataType=5\nAccessType=rw\nLowLimit=x\n"},
		{"implicit", "[2000sub1]\nDataType=5\nAccessType=rw\n"},
		{"object type", "[2000]\nObjectType=3\n"},
		{"empty record", "[2000]\nObjectType=9\n"},
		{"sub type", "[2000]\nObjectType=9\n[2000sub0]\nObjectType=9\n"},
		{"bad compact value", "[2000]\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=2\n[2000Value]\n1=x\n"},
	} {
		f, err := ParseBytes([]byte(base + c.src))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Dictionary(1); err == nil || !strings.HasPrefix(err.Error(), "eds: [") {
			t.Errorf("%s: %v", c.name, err)
		}
		d, ds, err := f.BuildDictionary(1, BuildOptions{Lenient: true})
		if err != nil || len(ds) == 0 || d.Object(0x1000) == nil || ds[0].Severity != Warning {
			t.Errorf("%s lenient: %v %v", c.name, ds, err)
		}
	}
	// NULL objects are skipped.
	f, _ := ParseBytes([]byte(base + "[2000]\nObjectType=0\n"))
	if d, err := f.Dictionary(1); err != nil || d.Object(0x2000) != nil {
		t.Error(err)
	}
	// A dictionary error from od (sub-index 0 of a VAR made a duplicate)
	// is reported.
	f, _ = ParseBytes([]byte(base))
	f.Objects = append(f.Objects, f.Objects[0])
	if _, err := f.Dictionary(1); err == nil {
		t.Error("duplicate object accepted")
	}
}

func TestLenientThirdParty(t *testing.T) {
	f := mustParse(t, "testdata/third_party/python_canopen_sample.eds")
	if _, err := f.Dictionary(0x10); err == nil {
		t.Error("strict build of sample.eds should fail on DataType 0x40")
	}
	d, ds, err := f.BuildDictionary(0x10, BuildOptions{Lenient: true})
	if err != nil || len(ds) == 0 {
		t.Fatal(ds, err)
	}
	if getU(t, d, 0x1403, 1) != 1280+0x10 {
		t.Error("1280+$NODEID")
	}
	if v, _ := d.Variable(0x3030, 0); canopen.DecodeInt(v.High) != -1 || canopen.DecodeInt(v.Low) != math.MinInt32 {
		t.Errorf("3030 limits %x %x", v.Low, v.High)
	}
	if v, _ := d.Variable(0x3004, 3); v == nil || v.Name != "Sensor Status 3" || canopen.DecodeUint(v.Default) != 3 {
		t.Errorf("3004:3 %+v", v)
	}
}

// sameDict compares two dictionaries object by object.
func sameDict(t *testing.T, what string, a, b *od.Dictionary) {
	t.Helper()
	ao, bo := a.Objects(), b.Objects()
	if len(ao) != len(bo) {
		t.Fatalf("%s: %d vs %d objects", what, len(ao), len(bo))
	}
	for i, x := range ao {
		y := bo[i]
		if x.Index != y.Index || x.Name != y.Name || x.Type != y.Type || len(x.Subs) != len(y.Subs) {
			t.Fatalf("%s: object %04X: %+v vs %+v", what, x.Index, x, y)
		}
		for j, v := range x.Subs {
			w := y.Subs[j]
			if v.Index != w.Index || v.SubIndex != w.SubIndex || v.Name != w.Name || v.DataType != w.DataType || v.Access != w.Access ||
				v.PDOMappable != w.PDOMappable || v.NodeIDRelative != w.NodeIDRelative || v.MaxSize != w.MaxSize ||
				!bytes.Equal(v.Default, w.Default) || !bytes.Equal(v.Low, w.Low) || !bytes.Equal(v.High, w.High) ||
				(v.Low == nil) != (w.Low == nil) || (v.High == nil) != (w.High == nil) {
				t.Fatalf("%s: %s: %+v vs %+v", what, v.Key(), v, w)
			}
			va, _ := a.Get(v.Index, v.SubIndex)
			vb, _ := b.Get(v.Index, v.SubIndex)
			if !bytes.Equal(va, vb) {
				t.Fatalf("%s: %s value % x vs % x", what, v.Key(), va, vb)
			}
		}
	}
}

func TestFromDictionaryRoundTrip(t *testing.T) {
	for _, p := range fixtures {
		d := mustDict(t, p, 7)
		f := FromDictionary(d, ExportOptions{FileInfo: FileInfo{FileName: "x.eds"}})
		if ds := f.Validate(); HasErrors(ds) {
			t.Errorf("%s: exported file does not validate: %v", p, ds)
		}
		g, _ := roundTrip(t, f)
		d2, err := g.Dictionary(7)
		if err != nil {
			t.Fatal(p, err)
		}
		sameDict(t, p, d, d2)
	}
}

func TestFromDictionaryStandard(t *testing.T) {
	d, err := od.Standard(od.Config{NodeID: 3, DeviceType: 0x191, VendorID: 1, DeviceName: "dev", RPDOs: 2, TPDOs: 3,
		HeartbeatConsumers: 2, ErrorHistory: 4, SDOClients: 1, NodeGuarding: true, Time: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []*od.Object{
		od.NewVar(0x2000, "float", canopen.Real32, od.AccessRW, float32(math.NaN())),
		od.NewVar(0x2001, "double", canopen.Real64, od.AccessRW, -0.1),
		od.NewVar(0x2002, "bool", canopen.Boolean, od.AccessRW, true),
		od.NewVar(0x2003, "u48", canopen.Unsigned48, od.AccessRW, uint64(1<<40)),
		od.NewVar(0x2004, "unicode", canopen.UnicodeString, od.AccessRW, "héllo"),
		od.NewVar(0x2005, "octets", canopen.OctetString, od.AccessRW, []byte{1, 2, 0xFF}),
		od.NewVar(0x2006, "time", canopen.TimeOfDay, od.AccessRW, []byte{1, 2, 3, 4, 5, 6}),
		od.NewVar(0x2007, "i56", canopen.Integer56, od.AccessRW, int64(-1)<<50),
		od.NewVar(0x2008, "domain", canopen.Domain, od.AccessRW, nil),
		od.NewVar(0x2009, "f64nan", canopen.Real64, od.AccessRW, math.NaN()),
	} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	s := od.NewVar(0x200A, "sized", canopen.VisibleString, od.AccessRW, "ab")
	s.Subs[0].MaxSize = 16
	lim := od.NewVar(0x200B, "limited", canopen.Integer16, od.AccessRW, int16(5))
	lim.Subs[0].Low, lim.Subs[0].High = []byte{0xF6, 0xFF}, []byte{10, 0}
	for _, o := range []*od.Object{s, lim} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.SetValue(0x1017, 0, uint16(250))
	_ = d.SetValue(0x2004, 0, "changed")

	f := FromDictionary(d, ExportOptions{DeviceInfo: DeviceInfo{ProductName: "p", NrOfRXPDO: 99}})
	if f.DeviceInfo.NrOfRXPDO != 2 || f.DeviceInfo.NrOfTXPDO != 3 || f.FileInfo.EDSVersion != "4.0" || f.IsDCF() {
		t.Errorf("%+v", f.DeviceInfo)
	}
	if f.Object(0x1014).DefaultValue != "$NODEID+0x80" || f.Object(0x200A).Extra[0] != (KeyValue{StringLengthMinKey, "16"}) {
		t.Error(f.Object(0x1014).DefaultValue, f.Object(0x200A).Extra)
	}
	if ds := f.Validate(); HasErrors(ds) {
		t.Errorf("exported file does not validate: %v", ds)
	}
	g, _ := roundTrip(t, f)
	d2, err := g.Dictionary(3)
	if err != nil {
		t.Fatal(err)
	}
	d.Reset(0, 0xFFFF) // the EDS carries defaults, not current values
	sameDict(t, "EDS", d, d2)

	// DCF: current values as ParameterValue.
	_ = d.SetValue(0x1017, 0, uint16(250))
	_ = d.SetValue(0x2004, 0, "changed")
	f = FromDictionary(d, ExportOptions{DCF: true, Comissioning: DeviceComissioning{Baudrate: 500}})
	if !f.IsDCF() || f.DeviceComissioning.NodeID != 3 || f.DeviceComissioning.Baudrate != 500 {
		t.Errorf("%+v", f.DeviceComissioning)
	}
	if s := f.Object(0x1200).Sub(1); s.ParameterValue != "0x00000603" || s.DefaultValue != "$NODEID+0x600" {
		t.Errorf("1200sub1 %+v", s)
	}
	g, _ = roundTrip(t, f)
	d3, err := g.Dictionary(3)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		index uint16
		want  any
	}{{0x1017, uint64(250)}, {0x2004, "changed"}, {0x1014, uint64(0x83)}} {
		if v, _ := d3.GetValue(c.index, 0); v != c.want {
			t.Errorf("DCF %04X: %v", c.index, v)
		}
	}
}

func TestFormatLiteral(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	types := []canopen.DataType{canopen.Boolean, canopen.Integer8, canopen.Integer16, canopen.Integer24, canopen.Integer32,
		canopen.Integer40, canopen.Integer48, canopen.Integer56, canopen.Integer64, canopen.Unsigned8, canopen.Unsigned16,
		canopen.Unsigned24, canopen.Unsigned32, canopen.Unsigned40, canopen.Unsigned48, canopen.Unsigned56, canopen.Unsigned64,
		canopen.Real32, canopen.Real64, canopen.OctetString, canopen.Domain, canopen.TimeOfDay, canopen.TimeDifference, canopen.VisibleString}
	for _, typ := range types {
		for i := 0; i < 500; i++ {
			n := typ.Size()
			if n == 0 {
				n = r.Intn(10)
			}
			b := make([]byte, n)
			r.Read(b)
			if typ == canopen.Boolean {
				b[0] &= 1
			}
			if typ == canopen.VisibleString {
				for j := range b {
					b[j] = 'a' + b[j]%26
				}
			}
			lit := FormatLiteral(typ, b, false)
			back, err := canopen.ParseValue(typ, lit, 0)
			if err != nil || !bytes.Equal(back, b) {
				t.Fatalf("%s % x → %q → % x %v", typ, b, lit, back, err)
			}
		}
	}
	if s := FormatLiteral(canopen.Unsigned32, []byte{0x80, 0, 0, 0}, true); s != "$NODEID+0x80" {
		t.Error(s)
	}
	if s := FormatLiteral(canopen.Unsigned32, []byte{1}, false); s != "01" {
		t.Error(s)
	}
	if s := FormatLiteral(canopen.UnicodeString, []byte{'a', 0}, false); s != "a" {
		t.Error(s)
	}
}
