// SPDX-License-Identifier: MIT

package codegen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/internal/golden/compact"
	"github.com/otfabric/go-canopen/internal/golden/drive402"
	"github.com/otfabric/go-canopen/internal/golden/ds301"
	"github.com/otfabric/go-canopen/internal/golden/io401"
	"github.com/otfabric/go-canopen/internal/golden/node37"
	"github.com/otfabric/go-canopen/od"
)

// golden ties each golden package to its fixture.
type golden struct {
	src, pkg string
	newDict  func(canopen.NodeID) (*od.Dictionary, error)
	objects  func() []*od.Object
	local    func(*od.Dictionary) any
	remote   func(sdo any) any
}

var goldens = []golden{
	{"../eds/testdata/io401.eds", "io401", io401.NewDictionary, io401.Objects,
		func(d *od.Dictionary) any { return io401.Local{D: d} }, func(s any) any { return io401.Remote{SDO: s.(io401.SDO)} }},
	{"../eds/testdata/drive402.eds", "drive402", drive402.NewDictionary, drive402.Objects,
		func(d *od.Dictionary) any { return drive402.Local{D: d} }, func(s any) any { return drive402.Remote{SDO: s.(drive402.SDO)} }},
	{"../eds/testdata/compact.eds", "compact", compact.NewDictionary, compact.Objects,
		func(d *od.Dictionary) any { return compact.Local{D: d} }, func(s any) any { return compact.Remote{SDO: s.(compact.SDO)} }},
	{"../eds/testdata/node37.dcf", "node37", node37.NewDictionary, node37.Objects,
		func(d *od.Dictionary) any { return node37.Local{D: d} }, func(s any) any { return node37.Remote{SDO: s.(node37.SDO)} }},
	{"../eds/testdata/third_party/canopennode_ds301_profile.eds", "ds301", ds301.NewDictionary, ds301.Objects,
		func(d *od.Dictionary) any { return ds301.Local{D: d} }, func(s any) any { return ds301.Remote{SDO: s.(ds301.SDO)} }},
}

func parse(t testing.TB, path string) *eds.File {
	t.Helper()
	f, err := eds.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestGoldenUpToDate is gengolden -check as a test: the committed golden
// code is what the generator produces now.
func TestGoldenUpToDate(t *testing.T) {
	for _, g := range goldens {
		src, err := os.ReadFile(g.src)
		if err != nil {
			t.Fatal(err)
		}
		f, err := eds.ParseBytes(src)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(src)
		code, err := Generate(f, Options{Package: g.pkg, Source: filepath.Base(g.src), SourceSHA256: hex.EncodeToString(sum[:])})
		if err != nil {
			t.Fatal(err)
		}
		have, err := os.ReadFile(filepath.Join("../internal/golden", g.pkg, g.pkg+".go"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(have, code) {
			t.Errorf("internal/golden/%s is out of date: run go generate ./codegen", g.pkg)
		}
	}
}

// sameDict requires two dictionaries to be equal: every object, sub-object,
// type, access, default, limit and current value.
func sameDict(t *testing.T, what string, a, b *od.Dictionary) {
	t.Helper()
	ao, bo := a.Objects(), b.Objects()
	if len(ao) != len(bo) {
		t.Fatalf("%s: %d vs %d objects", what, len(ao), len(bo))
	}
	for i, x := range ao {
		y := bo[i]
		if x.Index != y.Index || x.Name != y.Name || x.Type != y.Type || len(x.Subs) != len(y.Subs) {
			t.Fatalf("%s: object %04X differs", what, x.Index)
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
				t.Fatalf("%s: %s: % x vs % x", what, v.Key(), va, vb)
			}
		}
	}
}

// TestDifferential requires the generated NewDictionary to build exactly
// the dictionary eds.File.Dictionary builds from the same file.
func TestDifferential(t *testing.T) {
	for _, g := range goldens {
		f := parse(t, g.src)
		for _, node := range []canopen.NodeID{1, 0x25, 127} {
			want, err := f.Dictionary(node)
			if err != nil {
				t.Fatal(err)
			}
			got, err := g.newDict(node)
			if err != nil {
				t.Fatal(err)
			}
			sameDict(t, g.pkg, want, got)
		}
		if len(g.objects()) != len(want(t, f).Objects()) {
			t.Error("Objects length")
		}
	}
}

func want(t *testing.T, f *eds.File) *od.Dictionary {
	d, err := f.Dictionary(1)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// names returns the generated accessor names of a file.
func names(t *testing.T, f *eds.File) []variable {
	d, err := f.Dictionary(0)
	if err != nil {
		t.Fatal(err)
	}
	g := &gen{f: f, objs: d.Objects()}
	g.name()
	return g.vars
}

// sample returns a value of a Go type different from the zero value,
// valid for the data type.
func sample(typ reflect.Type, dt canopen.DataType) reflect.Value {
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(-3)
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(2.5)
	case reflect.String:
		v.SetString("text")
	case reflect.Slice:
		n := dt.Size()
		if n == 0 {
			n = 3
		}
		v.Set(reflect.ValueOf(bytes.Repeat([]byte{0xA5}, n)))
	}
	return v
}

// TestLocal drives every generated local getter and setter through
// reflection and checks them against the dictionary's bytes.
func TestLocal(t *testing.T) {
	for _, g := range goldens {
		f := parse(t, g.src)
		d, err := g.newDict(0x25)
		if err != nil {
			t.Fatal(err)
		}
		l := reflect.ValueOf(g.local(d))
		for _, x := range names(t, f) {
			get, set := l.MethodByName(x.name), l.MethodByName("Set"+x.name)
			if !get.IsValid() || !set.IsValid() {
				t.Fatalf("%s: no %s/Set%s", g.pkg, x.name, x.name)
			}
			out := get.Call(nil)
			if !out[1].IsNil() {
				t.Fatalf("%s.%s: %v", g.pkg, x.name, out[1])
			}
			cur, _ := d.Get(x.v.Index, x.v.SubIndex)
			enc, err := canopen.Encode(x.v.DataType, out[0].Interface())
			if err != nil || !bytes.Equal(enc, cur) {
				t.Fatalf("%s.%s = %v, dictionary % x", g.pkg, x.name, out[0], cur)
			}
			v := sample(out[0].Type(), x.v.DataType)
			if res := set.Call([]reflect.Value{v}); !res[0].IsNil() {
				t.Fatalf("%s.Set%s(%v): %v", g.pkg, x.name, v, res[0])
			}
			cur, _ = d.Get(x.v.Index, x.v.SubIndex)
			enc, _ = canopen.Encode(x.v.DataType, v.Interface())
			if !bytes.Equal(enc, cur) {
				t.Fatalf("%s.Set%s(%v) stored % x", g.pkg, x.name, v, cur)
			}
			if back := get.Call(nil); !reflect.DeepEqual(back[0].Interface(), v.Interface()) {
				t.Fatalf("%s.%s after set = %v, want %v", g.pkg, x.name, back[0], v)
			}
		}
	}
}

// fakeSDO serves uploads and downloads from a dictionary with remote
// semantics.
type fakeSDO struct {
	d     *od.Dictionary
	fail  error
	short bool
}

func (s *fakeSDO) Upload(_ context.Context, index uint16, sub uint8) ([]byte, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	b, err := s.d.Read(index, sub)
	if s.short && len(b) > 0 {
		b = b[:len(b)-1]
	}
	if s.short && len(b) == 0 {
		b = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	}
	return b, err
}

func (s *fakeSDO) Download(_ context.Context, index uint16, sub uint8, data []byte) error {
	if s.fail != nil {
		return s.fail
	}
	return s.d.Write(index, sub, data)
}

func TestRemote(t *testing.T) {
	ctx := context.Background()
	ctxV := reflect.ValueOf(ctx)
	for _, g := range goldens {
		f := parse(t, g.src)
		d, err := g.newDict(0x25)
		if err != nil {
			t.Fatal(err)
		}
		sdo := &fakeSDO{d: d}
		r := reflect.ValueOf(g.remote(sdo))
		for _, x := range names(t, f) {
			get, set := r.MethodByName(x.name), r.MethodByName("Set"+x.name)
			if get.IsValid() != x.v.Access.Readable() || set.IsValid() != x.v.Access.Writable() {
				t.Fatalf("%s.%s: getter %v setter %v for %s", g.pkg, x.name, get.IsValid(), set.IsValid(), x.v.Access)
			}
			if get.IsValid() {
				out := get.Call([]reflect.Value{ctxV})
				if !out[1].IsNil() {
					t.Fatalf("%s.%s: %v", g.pkg, x.name, out[1])
				}
				cur, _ := d.Get(x.v.Index, x.v.SubIndex)
				if enc, _ := canopen.Encode(x.v.DataType, out[0].Interface()); !bytes.Equal(enc, cur) {
					t.Fatalf("%s.%s = %v, dictionary % x", g.pkg, x.name, out[0], cur)
				}
				sdo.short = true
				out = get.Call([]reflect.Value{ctxV})
				sdo.short = false
				if x.v.DataType.Size() != 0 && out[1].IsNil() {
					t.Fatalf("%s.%s: wrong length accepted", g.pkg, x.name)
				}
				sdo.fail = canopen.AbortNoObject
				out = get.Call([]reflect.Value{ctxV})
				sdo.fail = nil
				if !errors.Is(out[1].Interface().(error), canopen.AbortNoObject) {
					t.Fatalf("%s.%s: SDO error lost", g.pkg, x.name)
				}
			}
			if set.IsValid() {
				typ := set.Type().In(1)
				v := sample(typ, x.v.DataType)
				res := set.Call([]reflect.Value{ctxV, v})
				if res[0].IsNil() {
					cur, _ := d.Get(x.v.Index, x.v.SubIndex)
					if enc, _ := canopen.Encode(x.v.DataType, v.Interface()); !bytes.Equal(enc, cur) {
						t.Fatalf("%s.Set%s stored % x", g.pkg, x.name, cur)
					}
				} else if _, isAbort := res[0].Interface().(canopen.AbortCode); !isAbort {
					t.Fatalf("%s.Set%s(%v): %v", g.pkg, x.name, v, res[0]) // limits may refuse the sample
				}
			}
		}
	}
}

func TestGeneratedTypes(t *testing.T) {
	d, err := compact.NewDictionary(3)
	if err != nil {
		t.Fatal(err)
	}
	l := compact.Local{D: d}
	if v, err := l.Signed24BitPosition(); err != nil || v != -8388608 {
		t.Error(v, err)
	}
	if err := l.SetSigned24BitPosition(1 << 23); err == nil {
		t.Error("24-bit range not checked")
	}
	if v, err := l.CalibrationGain(); err != nil || v != 1.0000001 {
		t.Error(v, err)
	}
	if v, err := l.DisplayText(); err != nil || v != "Grüße" {
		t.Error(v, err)
	}
	if v, err := l.MACAddress(); err != nil || !bytes.Equal(v, []byte{2, 0, 0x5E, 0x10, 0, 1}) {
		t.Error(v, err)
	}
	if v, err := l.SetpointsSetpointB(); err != nil || v != 100 {
		t.Error(v, err)
	}
	if err := l.SetLastService([]byte{1}); err == nil {
		t.Error("TIME_OF_DAY length not checked")
	}
	if v, err := l.OperatingHours(); err != nil || v != 0 {
		t.Error(v, err)
	}
	if compact.SubSetpointsSetpointD != 4 || compact.IndexSetpoints != 0x2008 || compact.VendorNumber != 0xF7A {
		t.Error("constants")
	}
	io, _ := io401.NewDictionary(0x25)
	il := io401.Local{D: io}
	if v, err := il.COBIDEMCY(); err != nil || v != 0xA5 {
		t.Error(v, err)
	}
	if v, err := il.FilterTimeConstant(); err != nil || v != 25.5 {
		t.Error(v, err)
	}
	if v, err := il.AnalogueInputGlobalInterruptEnable(); err != nil || v {
		t.Error(v, err)
	}
	r := drive402.Remote{SDO: &fakeSDO{d: mustDrive(t)}}
	if err := r.SetModesOfOperation(context.Background(), 11); !errors.Is(err, canopen.AbortValueHigh) {
		t.Errorf("limit: %v", err)
	}
	if err := r.SetControlword(context.Background(), 0x0F); err != nil {
		t.Error(err)
	}
	if v, err := r.Statusword(context.Background()); err != nil || v != 0 {
		t.Error(v, err)
	}
	// A dictionary missing the object: the local accessor reports it.
	if _, err := (io401.Local{D: od.New(1)}).DeviceType(); !errors.Is(err, canopen.AbortNoObject) {
		t.Error(err)
	}
	// Wrong stored lengths cannot happen through od.Set, but the helpers
	// check anyway; exercise the string and float helpers with a
	// mismatching remote.
	bad := compact.Remote{SDO: &fakeSDO{d: d, short: true}}
	if _, err := bad.CalibrationGain(context.Background()); err == nil {
		t.Error("short REAL64")
	}
	if _, err := bad.DisplayText(context.Background()); err != nil {
		t.Error("string of any length", err)
	}
}

func mustDrive(t *testing.T) *od.Dictionary {
	d, err := drive402.NewDictionary(2)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGenerateErrors(t *testing.T) {
	f := parse(t, "../eds/testdata/io401.eds")
	for _, pkg := range []string{"", "1x", "a-b", "_", "func"} {
		if _, err := Generate(f, Options{Package: pkg}); err == nil {
			t.Errorf("package %q accepted", pkg)
		}
	}
	if _, err := Generate(parse(t, "../eds/testdata/broken.dcf"), Options{Package: "x"}); err == nil || !strings.Contains(err.Error(), "errors") {
		t.Error(err)
	}
	g := parse(t, "../eds/testdata/io401.eds")
	g.Object(0x2100).LowLimit = "$NODEID+1"
	if _, err := Generate(g, Options{Package: "x"}); err == nil || !strings.Contains(err.Error(), "$NODEID") {
		t.Error(err)
	}
	g = parse(t, "../eds/testdata/io401.eds")
	g.Object(0x1018).Sub(1).HighLimit = "$NODEID+1"
	if _, err := Generate(g, Options{Package: "x"}); err == nil {
		t.Error("sub limit")
	}
	// Validates (a defined type only warns) but cannot build a dictionary.
	h, err := eds.ParseBytes([]byte("[FileInfo]\nEDSVersion=4\n[DeviceInfo]\nBaudRate_250=1\n" +
		"[1000]\nParameterName=a\nDataType=7\nAccessType=ro\n[1001]\nParameterName=b\nDataType=5\nAccessType=ro\n" +
		"[1018]\nParameterName=c\nObjectType=9\nSubNumber=1\n[1018sub0]\nParameterName=n\nDataType=5\nAccessType=ro\n" +
		"[0040]\nParameterName=t\nObjectType=6\n[2000]\nParameterName=x\nDataType=0x40\nAccessType=rw\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(h, Options{Package: "x"}); err == nil {
		t.Error("unbuildable file accepted")
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{
		"COB-ID used by RPDO":         "COBIDUsedByRPDO",
		"Pre-defined error field":     "PreDefinedErrorField",
		"Sensor Sampling Rate (Hz)":   "SensorSamplingRateHz",
		"1st value":                   "X1stValue",
		"Grüße":                       "GrE",
		"äöü":                         "",
		"valve % open":                "ValveOpen",
		"highest sub-index supported": "HighestSubIndexSupported",
	} {
		if got := Ident(in); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNameCollisions(t *testing.T) {
	src := "[FileInfo]\nEDSVersion=4\n[DeviceInfo]\nBaudRate_250=1\n" +
		"[1000]\nParameterName=Device type\nDataType=7\nAccessType=ro\n[1001]\nParameterName=\nDataType=5\nAccessType=ro\n" +
		"[1018]\nParameterName=c\nObjectType=9\nSubNumber=3\n[1018sub0]\nParameterName=\nDataType=5\nAccessType=ro\nDefaultValue=2\n" +
		"[1018sub1]\nParameterName=x\nDataType=7\nAccessType=ro\n[1018sub2]\nParameterName=x\nDataType=7\nAccessType=ro\n" +
		"[2000]\nParameterName=Device type\nDataType=7\nAccessType=rw\n[2001]\nParameterName=Device type\nDataType=7\nAccessType=rw\n" +
		"[2002]\nParameterName=Set device type\nDataType=7\nAccessType=rw\n[2003]\nParameterName=D\nDataType=7\nAccessType=rw\n" +
		"[2004]\nParameterName=-\nDataType=1\nAccessType=rw\n"
	f, err := eds.ParseBytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	code, err := Generate(f, Options{Package: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "x.go", code, 0); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, x := range names(t, f) {
		if seen[x.name] || seen["Set"+x.name] {
			t.Errorf("duplicate %s", x.name)
		}
		seen[x.name], seen["Set"+x.name] = true, true
	}
	s := string(code)
	for _, want := range []string{"func (l Local) DeviceType()", "func (l Local) DeviceType2000()", "func (l Local) DeviceType2001()",
		"IndexObject1001", "func (l Local) CSub0()", "func (l Local) CXSub2()", "func (l Local) DSub0()", "Object2004"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "func (l Local) D()") {
		t.Error("accessor named like the Local field")
	}
}

// FuzzGenerate checks that Generate never panics and, whenever it accepts
// a file, produces Go source that parses.
func FuzzGenerate(f *testing.F) {
	for _, g := range goldens {
		b, err := os.ReadFile(g.src)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ef, err := eds.ParseBytes(b)
		if err != nil {
			return
		}
		code, err := Generate(ef, Options{Package: "fuzz"})
		if err != nil {
			return
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "fuzz.go", code, 0); err != nil {
			t.Fatal(err)
		}
	})
}
