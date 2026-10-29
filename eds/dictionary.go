// SPDX-License-Identifier: MIT

package eds

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// BuildOptions control [File.BuildDictionary].
type BuildOptions struct {
	// Lenient skips the sub-objects (and objects) a dictionary cannot hold
	// — unknown or structured data types, missing access types, values
	// that do not parse — and reports them as warnings, instead of
	// failing.
	Lenient bool
}

// Compact storage names (CiA 306 4.5.2.3): sub-index 0 of an ARRAY with
// CompactSubObj, and the name pattern of its elements when the object has
// no [XXXXName] entry for them (the convention of python canopen:
// ParameterName, '_', sub-index in lower-case hexadecimal).
const (
	CompactSub0Name   = "NrOfObjects"
	compactNameFormat = "%s_%x"
)

// Dictionary builds an object dictionary for a node from the object
// sections. It fails on the first object it cannot represent; see
// [File.BuildDictionary] for the rules and a lenient mode.
func (f *File) Dictionary(node canopen.NodeID) (*od.Dictionary, error) {
	d, _, err := f.BuildDictionary(node, BuildOptions{})
	return d, err
}

// BuildDictionary builds an object dictionary for a node.
//
//   - The value of a sub-object is its ParameterValue when present (DCF),
//     else its DefaultValue; an empty value is zero (empty for strings).
//   - A value with $NODEID ("$NODEID+0x180", "0x180+$NODEID") becomes a
//     node-ID-relative default: Default holds the offset and
//     NodeIDRelative is set, so the dictionary adds its node-ID, now and
//     after SetNodeID and a reset (an LSS node-ID change). $NODEID in a
//     limit is evaluated with node.
//   - An ARRAY with CompactSubObj=n is expanded: sub-index 0 (UNSIGNED8,
//     ro, value n, named [CompactSub0Name]) and sub-indices 1..n with the
//     object's DataType, AccessType, PDOMapping and value, named from
//     [XXXXName] (or ParameterName_<sub>), valued from [XXXXValue] when
//     present.
//   - Objects below 0x1000 (the data type area: DEFTYPE and DEFSTRUCT
//     definitions) and NULL objects are not part of the dictionary and
//     are skipped silently. A variable whose data type is defined that way
//     (or otherwise unknown or structured) cannot be held.
//   - A DOMAIN object (or sub-object) without DataType has type DOMAIN.
//     DOMAIN and OCTET_STRING values are hexadecimal digit pairs.
//   - Limits apply to integer and float types and are ignored otherwise.
//   - MaxSize of a variable-length value is 0 (unbounded), unless the
//     section has the CANopenNode key StringLengthMin: then it is the
//     larger of that and the length of the value. The length of the
//     default alone is no bound, since writable strings (names,
//     descriptions) commonly default to a shorter text than a device
//     accepts.
//
// In lenient mode the sub-objects that break these rules are skipped
// (and objects left without sub-objects) and reported as warnings.
func (f *File) BuildDictionary(node canopen.NodeID, opts BuildOptions) (*od.Dictionary, []Diagnostic, error) {
	objs, ds, err := f.odObjects(node, opts)
	if err != nil {
		return nil, ds, err
	}
	d := od.New(node)
	for _, o := range objs {
		if err := d.Add(o); err != nil {
			return nil, ds, fmt.Errorf("eds: %w", err)
		}
	}
	return d, ds, nil
}

// builder collects the problems of a build.
type builder struct {
	node    canopen.NodeID
	lenient bool
	ds      []Diagnostic
}

// fail records a problem: a warning in lenient mode, else the error.
func (b *builder) fail(sec, key, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if b.lenient {
		b.ds = append(b.ds, Diagnostic{Warning, sec, key, msg + "; skipped"})
		return nil
	}
	d := Diagnostic{Error, sec, key, msg}
	return fmt.Errorf("eds: %s", strings.TrimPrefix(d.String(), "error: "))
}

// odObjects converts the object sections to dictionary objects.
func (f *File) odObjects(node canopen.NodeID, opts BuildOptions) ([]*od.Object, []Diagnostic, error) {
	b := &builder{node: node, lenient: opts.Lenient}
	var out []*od.Object
	for _, o := range f.Objects {
		obj, err := b.object(o)
		if err != nil {
			return nil, b.ds, err
		}
		if obj != nil {
			out = append(out, obj)
		}
	}
	return out, b.ds, nil
}

func (b *builder) object(o *Object) (*od.Object, error) {
	sec := sectionName(o.Index, -1)
	if o.Index < 0x1000 || (!o.implicit && o.ObjectType == od.ObjectNull) {
		return nil, nil
	}
	if o.implicit {
		return nil, b.fail(sec, "", "sub-object sections without the object section")
	}
	obj := &od.Object{Index: o.Index, Name: o.ParameterName, Type: o.ObjectType}
	add := func(v *od.Variable, err error) error {
		if v != nil {
			obj.Subs = append(obj.Subs, v)
		}
		return err
	}
	switch o.ObjectType {
	case od.ObjectVar, od.ObjectDomain:
		if err := add(b.variable(sec, o.Index, 0, o.ParameterName, &o.Entry, o.ObjectType, o.Value())); err != nil {
			return nil, err
		}
	case od.ObjectArray, od.ObjectRecord:
		if o.CompactSubObj > 0 {
			n := o.CompactSubObj
			obj.Subs = append(obj.Subs, &od.Variable{Index: o.Index, SubIndex: 0, Name: CompactSub0Name,
				DataType: canopen.Unsigned8, Access: od.AccessRO, Default: []byte{n}})
			for i := uint8(1); i <= n && i != 0; i++ {
				name := fmt.Sprintf(compactNameFormat, o.ParameterName, i)
				if o.Names != nil {
					if s, ok := o.Names.Entries[i]; ok {
						name = s
					}
				}
				val := o.Value()
				if o.Values != nil {
					if s, ok := o.Values.Entries[i]; ok {
						val = s
					}
				}
				if err := add(b.variable(sectionName(o.Index, int(i)), o.Index, i, name, &o.Entry, od.ObjectVar, val)); err != nil {
					return nil, err
				}
			}
			break
		}
		for _, s := range o.Subs {
			ss := sectionName(o.Index, int(s.SubIndex))
			if s.ObjectType != od.ObjectVar && s.ObjectType != od.ObjectDomain {
				if err := b.fail(ss, "ObjectType", "a sub-object is a VAR or a DOMAIN, not %s", s.ObjectType); err != nil {
					return nil, err
				}
				continue
			}
			if err := add(b.variable(ss, o.Index, s.SubIndex, s.ParameterName, &s.Entry, s.ObjectType, s.Value())); err != nil {
				return nil, err
			}
		}
	default:
		return nil, b.fail(sec, "ObjectType", "object code 0x%X cannot be in a dictionary", uint8(o.ObjectType))
	}
	if len(obj.Subs) == 0 {
		return nil, b.fail(sec, "", "the object has no sub-objects")
	}
	return obj, nil
}

// variable converts one sub-object; a nil variable with a nil error is a
// skipped one.
func (b *builder) variable(sec string, index uint16, sub uint8, name string, e *Entry, ot od.ObjectType, value string) (*od.Variable, error) {
	t := e.DataType
	if t == 0 && ot == od.ObjectDomain {
		t = canopen.Domain
	}
	if !supported(t) {
		return nil, b.fail(sec, "DataType", "data type 0x%04X cannot be held in a dictionary", uint16(t))
	}
	if e.AccessType == 0 || e.AccessType > od.AccessConst {
		return nil, b.fail(sec, "AccessType", "missing")
	}
	v := &od.Variable{Index: index, SubIndex: sub, Name: name, DataType: t, Access: e.AccessType, PDOMappable: e.PDOMapping}
	var err error
	if v.Default, v.NodeIDRelative, err = parseLiteral(t, value, 0); err != nil {
		return nil, b.fail(sec, "DefaultValue", "%v", err)
	}
	if t.IsInteger() || t.IsFloat() {
		for _, l := range []struct {
			key, lit string
			dst      *[]byte
		}{{"LowLimit", e.LowLimit, &v.Low}, {"HighLimit", e.HighLimit, &v.High}} {
			if l.lit == "" {
				continue
			}
			if *l.dst, _, err = parseLiteral(t, l.lit, b.node); err != nil {
				return nil, b.fail(sec, l.key, "%v", err)
			}
		}
	}
	if t.IsVariable() {
		for _, kv := range e.Extra {
			if strings.EqualFold(kv.Key, StringLengthMinKey) {
				if n, err := canopen.ParseUint(kv.Value, 31); err == nil {
					v.MaxSize = max(int(n), len(v.Default))
				}
			}
		}
	}
	return v, nil
}

// StringLengthMinKey is the CANopenNode key giving the allocated length
// of a string or DOMAIN; [File.BuildDictionary] takes it as MaxSize and
// [FromDictionary] writes MaxSize to it.
const StringLengthMinKey = "StringLengthMin"

// ExportOptions control [FromDictionary].
type ExportOptions struct {
	// FileInfo and DeviceInfo are copied into the file. EDSVersion
	// defaults to "4.0"; NrOfRXPDO and NrOfTXPDO are counted from the
	// dictionary.
	FileInfo   FileInfo
	DeviceInfo DeviceInfo
	// DCF makes a device configuration file: every sub-object gets its
	// current value as ParameterValue, and the file gets a
	// [DeviceComissioning] section from Comissioning (NodeID defaults to
	// the dictionary's node-ID).
	DCF          bool
	Comissioning DeviceComissioning
}

// FromDictionary describes a dictionary as an EDS, or as a DCF holding
// its current values, for exporting a node's description or
// configuration. Every object is written expanded (no compact storage);
// node-ID-relative defaults are written as "$NODEID+0x..."; MaxSize goes
// to StringLengthMin. The object lists are built with
// [File.UpdateObjectLists].
func FromDictionary(d *od.Dictionary, opts ExportOptions) *File {
	f := &File{FileInfo: opts.FileInfo, DeviceInfo: opts.DeviceInfo}
	if f.FileInfo.EDSVersion == "" {
		f.FileInfo.EDSVersion = "4.0"
	}
	f.DeviceInfo.NrOfRXPDO, f.DeviceInfo.NrOfTXPDO = 0, 0
	if opts.DCF {
		c := opts.Comissioning
		if c.NodeID == 0 {
			c.NodeID = uint8(d.NodeID())
		}
		f.DeviceComissioning = &c
	}
	for _, o := range d.Objects() {
		switch {
		case o.Index >= od.IndexRPDOComm && o.Index < od.IndexRPDOComm+od.MaxPDOs:
			f.DeviceInfo.NrOfRXPDO++
		case o.Index >= od.IndexTPDOComm && o.Index < od.IndexTPDOComm+od.MaxPDOs:
			f.DeviceInfo.NrOfTXPDO++
		}
		eo := &Object{Index: o.Index}
		eo.ParameterName, eo.ObjectType = o.Name, o.Type
		entry := func(e *Entry, v *od.Variable) {
			e.DataType, e.AccessType, e.PDOMapping = v.DataType, v.Access, v.PDOMappable
			e.DefaultValue = FormatLiteral(v.DataType, v.Default, v.NodeIDRelative)
			if v.Low != nil {
				e.LowLimit = FormatLiteral(v.DataType, v.Low, false)
			}
			if v.High != nil {
				e.HighLimit = FormatLiteral(v.DataType, v.High, false)
			}
			if v.MaxSize > 0 {
				e.Extra = append(e.Extra, KeyValue{StringLengthMinKey, strconv.Itoa(v.MaxSize)})
			}
			if opts.DCF {
				cur, _ := d.Get(v.Index, v.SubIndex)
				e.ParameterValue, e.HasParameterValue = FormatLiteral(v.DataType, cur, false), true
			}
		}
		if o.Type == od.ObjectVar || o.Type == od.ObjectDomain {
			entry(&eo.Entry, o.Subs[0])
		} else {
			eo.SubNumber = uint8(len(o.Subs))
			for _, v := range o.Subs {
				s := &SubObject{SubIndex: v.SubIndex}
				s.ParameterName, s.ObjectType = v.Name, od.ObjectVar
				if v.DataType == canopen.Domain {
					s.ObjectType = od.ObjectDomain
				}
				entry(&s.Entry, v)
				eo.Subs = append(eo.Subs, s)
			}
		}
		f.Objects = append(f.Objects, eo)
	}
	f.UpdateObjectLists()
	return f
}

// FormatLiteral formats an encoded value of type t as an EDS value
// literal that [canopen.ParseValue] reads back to the same bytes:
// unsigned integers in 0x hexadecimal, signed integers in decimal, floats
// in the shortest decimal form (NaNs as their 0x bit pattern), BOOLEAN as
// 0/1, VISIBLE_STRING and UNICODE_STRING as text, OCTET_STRING, DOMAIN and
// the TIME types as hexadecimal digits. With nodeRelative the value is an
// offset written as "$NODEID+0x...".
func FormatLiteral(t canopen.DataType, b []byte, nodeRelative bool) string {
	if nodeRelative && t.IsInteger() {
		return fmt.Sprintf("$NODEID+0x%X", canopen.DecodeUint(b))
	}
	if n := t.Size(); n != 0 && len(b) != n {
		return hex.EncodeToString(b)
	}
	switch {
	case t == canopen.Boolean:
		if b[0] != 0 {
			return "1"
		}
		return "0"
	case t == canopen.Real32:
		x := math.Float32frombits(uint32(canopen.DecodeUint(b)))
		if x != x {
			return fmt.Sprintf("0x%08X", canopen.DecodeUint(b))
		}
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case t == canopen.Real64:
		x := math.Float64frombits(canopen.DecodeUint(b))
		if x != x {
			return fmt.Sprintf("0x%016X", canopen.DecodeUint(b))
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case t.IsSigned():
		return strconv.FormatInt(canopen.DecodeInt(b), 10)
	case t.IsInteger():
		return fmt.Sprintf("0x%0*X", 2*t.Size(), canopen.DecodeUint(b))
	case t == canopen.VisibleString:
		return string(b)
	case t == canopen.UnicodeString:
		s, _ := canopen.Decode(t, b)
		return s.(string)
	case t == canopen.TimeOfDay || t == canopen.TimeDifference:
		return "0x" + strings.ToUpper(hex.EncodeToString(b))
	}
	return strings.ToUpper(hex.EncodeToString(b))
}
