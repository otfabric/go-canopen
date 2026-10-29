// SPDX-License-Identifier: MIT

// Package codegen generates typed Go code from an EDS or DCF ([eds.File]):
// index and sub-index constants for every object, a NewDictionary
// function that builds the object dictionary without parsing anything at
// run time, a typed local accessor over an [od.Dictionary] and a typed
// remote accessor over an SDO client.
//
// The generated package declares
//
//	const IndexDeviceType uint16 = 0x1000 // one per object
//	const SubIdentityObjectVendorID uint8 = 0x01 // one per sub-object of an ARRAY or RECORD
//	func Objects() []*od.Object
//	func NewDictionary(node canopen.NodeID) (*od.Dictionary, error)
//	type Local struct{ D *od.Dictionary }   // DeviceType() (uint32, error), SetDeviceType(uint32) error, ...
//	type SDO interface { Upload(...); Download(...) }
//	type Remote struct{ SDO SDO }           // DeviceType(ctx) (uint32, error), SetX(ctx, v) error, ...
//
// Each sub-object gets the natural Go type of its data type: bool,
// int8..int64, uint8..uint64 (24-bit types as 32-bit and 40..56-bit as
// 64-bit Go types, range-checked on set), float32, float64, string for
// VISIBLE_STRING and UNICODE_STRING, []byte for OCTET_STRING, DOMAIN and
// the TIME types. The remote accessor has getters for the readable and
// setters for the writable sub-objects only; the local accessor has both
// for every sub-object (local access bypasses access rights).
//
// The output is deterministic (no timestamps, index order), gofmt'd and
// carries its provenance: the source name, the SHA-256 of the source and
// the generator version. It imports only the standard library and
// go-canopen's root and od packages.
package codegen

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"strconv"
	"strings"
	"unicode"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// Version identifies the generator in the provenance header. It changes
// when the generated code changes.
const Version = "1"

// Options control generation.
type Options struct {
	// Package is the Go package name (required).
	Package string
	// Source names the EDS file in the provenance header.
	Source string
	// SourceSHA256 is the hex SHA-256 of the EDS file, if known.
	SourceSHA256 string
}

// Generate returns the Go source for f. The file must validate without
// errors ([eds.File.Validate]) and build a dictionary
// ([eds.File.Dictionary]); limits with $NODEID are not supported (the
// generated objects do not depend on the node-ID except through
// node-ID-relative defaults).
func Generate(f *eds.File, opts Options) ([]byte, error) {
	if !token.IsIdentifier(opts.Package) || opts.Package == "_" {
		return nil, fmt.Errorf("codegen: invalid package name %q", opts.Package)
	}
	for _, d := range f.Validate() {
		if d.Severity == eds.Error {
			return nil, fmt.Errorf("codegen: the file has errors: %s", d)
		}
	}
	if err := noRelativeLimits(f); err != nil {
		return nil, err
	}
	d, err := f.Dictionary(0)
	if err != nil {
		return nil, fmt.Errorf("codegen: %w", err)
	}
	g := &gen{f: f, opts: opts, objs: d.Objects(), helpers: map[string]bool{}}
	g.name()
	g.file()
	out, err := format.Source(g.buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("codegen: generated code does not compile (generator bug): %v\n%s", err, g.buf.String())
	}
	return out, nil
}

func noRelativeLimits(f *eds.File) error {
	check := func(index uint16, e *eds.Entry) error {
		for _, l := range []string{e.LowLimit, e.HighLimit} {
			if strings.Contains(strings.ToUpper(l), "$NODEID") {
				return fmt.Errorf("codegen: object 0x%04X: limits with $NODEID are not supported", index)
			}
		}
		return nil
	}
	for _, o := range f.Objects {
		if err := check(o.Index, &o.Entry); err != nil {
			return err
		}
		for _, s := range o.Subs {
			if err := check(o.Index, &s.Entry); err != nil {
				return err
			}
		}
	}
	return nil
}

// variable is a sub-object with its generated names.
type variable struct {
	v    *od.Variable
	name string // accessor name
	sub  string // sub-index constant name; empty for a VAR or DOMAIN
	idx  string // index constant name
}

type gen struct {
	f       *eds.File
	opts    Options
	objs    []*od.Object
	objName map[uint16]string
	vars    []variable
	helpers map[string]bool
	buf     bytes.Buffer
}

func (g *gen) p(format string, args ...any) {
	fmt.Fprintf(&g.buf, format, args...)
	g.buf.WriteByte('\n')
}

// reserved are names a variable accessor must not take: the fields of
// Local and Remote.
var reserved = map[string]bool{"D": true, "SDO": true}

// name assigns collision-free Go names to the objects and variables.
func (g *gen) name() {
	g.objName = map[uint16]string{}
	objUsed := map[string]bool{}
	varUsed := map[string]bool{}
	for k := range reserved {
		varUsed[k] = true
	}
	for _, o := range g.objs {
		base := Ident(o.Name)
		if base == "" {
			base = fmt.Sprintf("Object%04X", o.Index)
		}
		n := unique(base, fmt.Sprintf("%04X", o.Index), func(s string) bool { return objUsed[s] })
		objUsed[n] = true
		g.objName[o.Index] = n
		for _, v := range o.Subs {
			vn := n
			single := o.Type == od.ObjectVar || o.Type == od.ObjectDomain
			if !single {
				s := Ident(v.Name)
				if s == "" {
					s = fmt.Sprintf("Sub%d", v.SubIndex)
				}
				vn = n + s
				if strings.HasPrefix(s, n) && len(s) > len(n) {
					vn = s // "Pre-defined error field_1": no stutter
				}
			}
			vn = unique(vn, fmt.Sprintf("Sub%d", v.SubIndex), func(s string) bool { return varUsed[s] || varUsed["Set"+s] })
			varUsed[vn], varUsed["Set"+vn] = true, true
			x := variable{v: v, name: vn, idx: "Index" + n}
			if !single {
				x.sub = "Sub" + vn
			}
			g.vars = append(g.vars, x)
		}
	}
	// A setter name may equal a later getter name ("X" and "Set X"): the
	// check above reserves both forms, so the names are distinct.
}

// unique returns base, or base+suffix, base+suffix+"2", ... until taken
// reports false.
func unique(base, suffix string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	n := base + suffix
	for i := 2; taken(n); i++ {
		n = base + suffix + strconv.Itoa(i)
	}
	return n
}

// Ident derives an exported Go identifier from an object name: the ASCII
// letters and digits of each word, each word capitalised ("COB-ID used
// by RPDO" → "COBIDUsedByRPDO"), with "X" in front of a leading digit.
// It returns "" when the name has no ASCII letter or digit.
func Ident(name string) string {
	var b strings.Builder
	up := true
	for _, r := range name {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r)) {
			up = true
			continue
		}
		if up {
			r = unicode.ToUpper(r)
			up = false
		}
		b.WriteRune(r)
	}
	s := b.String()
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		s = "X" + s
	}
	return s
}

// goType returns the Go type of a data type and the helper decoding it.
func goType(t canopen.DataType) (typ, helper string) {
	switch t {
	case canopen.Boolean:
		return "bool", "asBool"
	case canopen.Integer8:
		return "int8", "asInt"
	case canopen.Integer16:
		return "int16", "asInt"
	case canopen.Integer24, canopen.Integer32:
		return "int32", "asInt"
	case canopen.Integer40, canopen.Integer48, canopen.Integer56, canopen.Integer64:
		return "int64", "asInt"
	case canopen.Unsigned8:
		return "uint8", "asUint"
	case canopen.Unsigned16:
		return "uint16", "asUint"
	case canopen.Unsigned24, canopen.Unsigned32:
		return "uint32", "asUint"
	case canopen.Unsigned40, canopen.Unsigned48, canopen.Unsigned56, canopen.Unsigned64:
		return "uint64", "asUint"
	case canopen.Real32:
		return "float32", "asFloat32"
	case canopen.Real64:
		return "float64", "asFloat64"
	case canopen.VisibleString, canopen.UnicodeString:
		return "string", "asString"
	default: // no special case
	}
	return "[]byte", "asBytes"
}

// typeNames are the Go expressions of the data types.
var typeNames = map[canopen.DataType]string{
	canopen.Boolean: "Boolean", canopen.Integer8: "Integer8", canopen.Integer16: "Integer16", canopen.Integer24: "Integer24",
	canopen.Integer32: "Integer32", canopen.Integer40: "Integer40", canopen.Integer48: "Integer48", canopen.Integer56: "Integer56",
	canopen.Integer64: "Integer64", canopen.Unsigned8: "Unsigned8", canopen.Unsigned16: "Unsigned16", canopen.Unsigned24: "Unsigned24",
	canopen.Unsigned32: "Unsigned32", canopen.Unsigned40: "Unsigned40", canopen.Unsigned48: "Unsigned48", canopen.Unsigned56: "Unsigned56",
	canopen.Unsigned64: "Unsigned64", canopen.Real32: "Real32", canopen.Real64: "Real64", canopen.VisibleString: "VisibleString",
	canopen.OctetString: "OctetString", canopen.UnicodeString: "UnicodeString", canopen.TimeOfDay: "TimeOfDay",
	canopen.TimeDifference: "TimeDifference", canopen.Domain: "Domain",
}

func typeExpr(t canopen.DataType) string { return "canopen." + typeNames[t] }

var objectTypeNames = map[od.ObjectType]string{
	od.ObjectVar: "od.ObjectVar", od.ObjectDomain: "od.ObjectDomain", od.ObjectArray: "od.ObjectArray", od.ObjectRecord: "od.ObjectRecord",
}

var accessNames = map[od.Access]string{
	od.AccessRO: "od.AccessRO", od.AccessWO: "od.AccessWO", od.AccessRW: "od.AccessRW",
	od.AccessRWR: "od.AccessRWR", od.AccessRWW: "od.AccessRWW", od.AccessConst: "od.AccessConst",
}

// comment makes text safe for a line comment.
func comment(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7F || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
}

func byteLit(b []byte) string {
	if b == nil {
		return "nil"
	}
	var s strings.Builder
	s.WriteString("[]byte{")
	for i, x := range b {
		if i > 0 {
			s.WriteString(", ")
		}
		fmt.Fprintf(&s, "0x%02X", x)
	}
	s.WriteString("}")
	return s.String()
}

func (g *gen) file() {
	src := g.opts.Source
	if src == "" {
		src = "an EDS file"
	}
	sum := ""
	if g.opts.SourceSHA256 != "" {
		sum = " (sha256 " + g.opts.SourceSHA256 + ")"
	}
	g.p("// Code generated by go-canopen codegen %s from %s%s. DO NOT EDIT.", Version, comment(src), sum)
	g.p("")
	di := &g.f.DeviceInfo
	g.p("// Package %s is the object dictionary of %s, generated from %s.", g.opts.Package, comment(deviceName(di)), comment(src))
	g.p("//")
	g.p("// NewDictionary builds it, Local reads and writes it with Go types, and")
	g.p("// Remote does the same on a remote node over an SDO client.")
	g.p("package %s", g.opts.Package)
	g.p("")
	g.p(`import (
	"context"
	"fmt"
	"math"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)`)
	g.p("")
	g.p("// Identity of the device ([DeviceInfo] of the EDS).")
	g.p("const (")
	g.p("VendorNumber uint32 = 0x%08X", di.VendorNumber)
	g.p("ProductNumber uint32 = 0x%08X", di.ProductNumber)
	g.p("RevisionNumber uint32 = 0x%08X", di.RevisionNumber)
	g.p(")")
	g.p("")
	g.p("// Object indices.")
	g.p("const (")
	for _, o := range g.objs {
		g.p("// %s is 0x%04X %s (%s).", "Index"+g.objName[o.Index], o.Index, comment(o.Name), o.Type)
		g.p("Index%s uint16 = 0x%04X", g.objName[o.Index], o.Index)
	}
	g.p(")")
	g.p("")
	g.p("// Sub-indices of the ARRAY and RECORD objects.")
	g.p("const (")
	for _, x := range g.vars {
		if x.sub != "" {
			g.p("// %s is %s %s.", x.sub, x.v.Key(), comment(x.v.Name))
			g.p("%s uint8 = 0x%02X", x.sub, x.v.SubIndex)
		}
	}
	g.p(")")
	g.p("")
	g.objects()
	g.local()
	g.remote()
	g.helperFuncs()
}

func deviceName(di *eds.DeviceInfo) string {
	switch {
	case di.ProductName != "" && di.VendorName != "":
		return di.ProductName + " (" + di.VendorName + ")"
	case di.ProductName != "":
		return di.ProductName
	}
	return "a CANopen device"
}

func (x variable) subExpr() string {
	if x.sub == "" {
		return "0"
	}
	return x.sub
}

func (g *gen) objects() {
	g.p("// Objects returns new descriptions of the objects of the dictionary,")
	g.p("// in index order, with their default values. Node-ID-relative defaults")
	g.p("// hold the offset the node-ID is added to.")
	g.p("func Objects() []*od.Object {")
	g.p("return []*od.Object{")
	i := 0
	for _, o := range g.objs {
		g.p("{Index: %s, Name: %s, Type: %s, Subs: []*od.Variable{", "Index"+g.objName[o.Index], strconv.Quote(o.Name), objectTypeNames[o.Type])
		for _, v := range o.Subs {
			x := g.vars[i]
			i++
			var f []string
			f = append(f, "Index: "+x.idx, "SubIndex: "+x.subExpr(), "Name: "+strconv.Quote(v.Name),
				"DataType: "+typeExpr(v.DataType), "Access: "+accessNames[v.Access])
			if v.PDOMappable {
				f = append(f, "PDOMappable: true")
			}
			def := v.Default
			if def == nil {
				def = []byte{}
			}
			f = append(f, "Default: "+byteLit(def))
			if v.NodeIDRelative {
				f = append(f, "NodeIDRelative: true")
			}
			if v.Low != nil {
				f = append(f, "Low: "+byteLit(v.Low))
			}
			if v.High != nil {
				f = append(f, "High: "+byteLit(v.High))
			}
			if v.MaxSize != 0 {
				f = append(f, "MaxSize: "+strconv.Itoa(v.MaxSize))
			}
			g.p("{%s},", strings.Join(f, ", "))
		}
		g.p("}},")
	}
	g.p("}")
	g.p("}")
	g.p("")
	g.p("// NewDictionary returns the object dictionary for a node, every value at")
	g.p("// its default.")
	g.p("func NewDictionary(node canopen.NodeID) (*od.Dictionary, error) {")
	g.p("d := od.New(node)")
	g.p("for _, o := range Objects() {")
	g.p("if err := d.Add(o); err != nil {")
	g.p("return nil, err")
	g.p("}")
	g.p("}")
	g.p("return d, nil")
	g.p("}")
	g.p("")
}

func describe(v *od.Variable) string {
	return fmt.Sprintf("%s %s (%s, %s)", v.Key(), comment(v.Name), v.DataType, v.Access)
}

func (g *gen) local() {
	g.p("// Local reads and writes the dictionary of this node with Go types.")
	g.p("// Local accesses bypass the access rights (see od.Dictionary.Get and")
	g.p("// Set); setters check the range of the data type.")
	g.p("type Local struct{ D *od.Dictionary }")
	g.p("")
	for _, x := range g.vars {
		typ, helper := goType(x.v.DataType)
		g.helpers[helper] = true
		conv := typ
		if typ == "[]byte" || typ == "string" || typ == "bool" || typ == "float32" || typ == "float64" {
			conv = ""
		}
		g.p("// %s returns %s.", x.name, describe(x.v))
		g.p("func (l Local) %s() (%s, error) {", x.name, typ)
		g.p("b, err := l.D.Get(%s, %s)", x.idx, x.subExpr())
		g.ret(conv, helper, x.v.DataType)
		g.p("}")
		g.p("")
		g.p("// Set%s stores %s.", x.name, describe(x.v))
		g.p("func (l Local) Set%s(v %s) error {", x.name, typ)
		g.p("return l.D.SetValue(%s, %s, v)", x.idx, x.subExpr())
		g.p("}")
		g.p("")
	}
}

// ret writes the decoding return of a getter.
func (g *gen) ret(conv, helper string, t canopen.DataType) {
	if conv == "" {
		g.p("return %s(%s, b, err)", helper, typeExpr(t))
		return
	}
	g.p("x, err := %s(%s, b, err)", helper, typeExpr(t))
	g.p("return %s(x), err", conv)
}

func (g *gen) remote() {
	g.p("// SDO is the SDO client Remote uses; a go-canopen sdo.Client satisfies")
	g.p("// it.")
	g.p("type SDO interface {")
	g.p("Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error)")
	g.p("Download(ctx context.Context, index uint16, sub uint8, data []byte) error")
	g.p("}")
	g.p("")
	g.p("// Remote reads and writes the dictionary of a remote node with Go types,")
	g.p("// over SDO: getters for the readable and setters for the writable")
	g.p("// sub-objects.")
	g.p("type Remote struct{ SDO SDO }")
	g.p("")
	for _, x := range g.vars {
		typ, helper := goType(x.v.DataType)
		conv := typ
		if typ == "[]byte" || typ == "string" || typ == "bool" || typ == "float32" || typ == "float64" {
			conv = ""
		}
		if x.v.Access.Readable() {
			g.p("// %s uploads %s.", x.name, describe(x.v))
			g.p("func (r Remote) %s(ctx context.Context) (%s, error) {", x.name, typ)
			g.p("b, err := r.SDO.Upload(ctx, %s, %s)", x.idx, x.subExpr())
			g.ret(conv, helper, x.v.DataType)
			g.p("}")
			g.p("")
		}
		if x.v.Access.Writable() {
			g.p("// Set%s downloads %s.", x.name, describe(x.v))
			g.p("func (r Remote) Set%s(ctx context.Context, v %s) error {", x.name, typ)
			g.p("b, err := canopen.Encode(%s, v)", typeExpr(x.v.DataType))
			g.p("if err != nil {")
			g.p("return err")
			g.p("}")
			g.p("return r.SDO.Download(ctx, %s, %s, b)", x.idx, x.subExpr())
			g.p("}")
			g.p("")
		}
	}
}

// helperSrc are the decoding helpers the getters use.
var helperSrc = map[string]string{
	"asUint": `func asUint(t canopen.DataType, b []byte, err error) (uint64, error) {
	if err != nil {
		return 0, err
	}
	if len(b) != t.Size() {
		return 0, lengthError(t, b)
	}
	return canopen.DecodeUint(b), nil
}`,
	"asInt": `func asInt(t canopen.DataType, b []byte, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	if len(b) != t.Size() {
		return 0, lengthError(t, b)
	}
	return canopen.DecodeInt(b), nil
}`,
	"asBool": `func asBool(t canopen.DataType, b []byte, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	if len(b) != t.Size() {
		return false, lengthError(t, b)
	}
	return b[0] != 0, nil
}`,
	"asFloat32": `func asFloat32(t canopen.DataType, b []byte, err error) (float32, error) {
	if err != nil {
		return 0, err
	}
	if len(b) != t.Size() {
		return 0, lengthError(t, b)
	}
	return math.Float32frombits(uint32(canopen.DecodeUint(b))), nil
}`,
	"asFloat64": `func asFloat64(t canopen.DataType, b []byte, err error) (float64, error) {
	if err != nil {
		return 0, err
	}
	if len(b) != t.Size() {
		return 0, lengthError(t, b)
	}
	return math.Float64frombits(canopen.DecodeUint(b)), nil
}`,
	"asString": `func asString(t canopen.DataType, b []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	v, err := canopen.Decode(t, b)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}`,
	"asBytes": `func asBytes(t canopen.DataType, b []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if n := t.Size(); n != 0 && len(b) != n {
		return nil, lengthError(t, b)
	}
	return b, nil
}`,
}

func (g *gen) helperFuncs() {
	g.p("func lengthError(t canopen.DataType, b []byte) error {")
	g.p("return fmt.Errorf(\"%%w: %%s needs %%d bytes, got %%d\", canopen.ErrDataType, t, t.Size(), len(b))")
	g.p("}")
	g.p("")
	for _, h := range []string{"asUint", "asInt", "asBool", "asFloat32", "asFloat64", "asString", "asBytes"} {
		if g.helpers[h] {
			g.p("%s", helperSrc[h])
			g.p("")
		}
	}
	// math and fmt are always imported; keep them used.
	if !g.helpers["asFloat32"] && !g.helpers["asFloat64"] {
		g.p("var _ = math.Float32frombits")
	}
}
