// SPDX-License-Identifier: MIT

package eds

import (
	"fmt"
	"math"
	"strings"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Severity grades a [Diagnostic].
type Severity uint8

// Severities.
const (
	// Error is a violation of CiA 306 or CiA 301 a tool may refuse the
	// file for, or that prevents building a dictionary.
	Error Severity = iota + 1
	// Warning is a questionable but usable construct.
	Warning
)

// String returns "error" or "warning".
func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	}
	return fmt.Sprintf("Severity(%d)", uint8(s))
}

// Diagnostic is one finding of [File.Validate].
type Diagnostic struct {
	Severity Severity
	// Section is the section name ("1018sub1", "DeviceInfo"); empty for
	// findings about the file as a whole.
	Section string
	// Key is the key concerned, if any.
	Key     string
	Message string
}

// String formats the diagnostic as "error: [1018sub1] DefaultValue: ...".
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.Severity.String())
	b.WriteString(": ")
	if d.Section != "" {
		b.WriteString("[" + d.Section + "] ")
	}
	if d.Key != "" {
		b.WriteString(d.Key + ": ")
	}
	b.WriteString(d.Message)
	return b.String()
}

// HasErrors reports whether any diagnostic is an [Error].
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// validator collects diagnostics.
type validator struct {
	f  *File
	ds []Diagnostic
}

func (v *validator) add(s Severity, section, key, format string, args ...any) {
	v.ds = append(v.ds, Diagnostic{s, section, key, fmt.Sprintf(format, args...)})
}

// Validate checks the file against CiA 306 and CiA 301: the mandatory
// objects 0x1000, 0x1001 and 0x1018; the object lists against the object
// sections and their declared counts; ObjectType, DataType and
// AccessType; SubNumber against the sub-object sections and sub-index 0;
// DefaultValue, ParameterValue and the limits against the data type, and
// LowLimit ≤ HighLimit; PDOMapping only on mappable types; the PDO
// objects against NrOfRXPDO and NrOfTXPDO; the compact storage rules;
// DCF commissioning values; keys whose values do not parse; duplicate
// keys and sections. A file without [Error] diagnostics builds a
// dictionary.
func (f *File) Validate() []Diagnostic {
	v := &validator{f: f}
	v.ds = append(v.ds, f.dups...)
	v.extras("FileInfo", f.FileInfo.Extra, knownFileInfo)
	if f.FileInfo.EDSVersion == "" {
		v.add(Warning, "FileInfo", "EDSVersion", "missing")
	}
	v.deviceInfo()
	if c := f.DeviceComissioning; c != nil {
		v.extras("DeviceComissioning", c.Extra, knownComm)
		if !canopen.NodeID(c.NodeID).Valid() {
			v.add(Error, "DeviceComissioning", "NodeID", "%d is not a node-ID (1..127)", c.NodeID)
		}
		if !validBaud(int(c.Baudrate)) {
			v.add(Warning, "DeviceComissioning", "Baudrate", "%d kbit/s is not a CiA 301 bit rate", c.Baudrate)
		}
	}
	if d := f.DummyUsage; d != nil {
		for _, t := range sortedKeys(d.Dummy) {
			if t < 0x0001 || t > 0x0007 {
				v.add(Warning, "DummyUsage", fmt.Sprintf("Dummy%04X", t), "CiA 306 defines dummy usage for data types 0x0001..0x0007 only")
			}
		}
		for _, kv := range d.Extra {
			v.add(Warning, "DummyUsage", kv.Key, "unknown key or invalid value %q", kv.Value)
		}
	}
	v.lists()
	for _, index := range []uint16{0x1000, 0x1001, 0x1018} {
		if f.Object(index) == nil {
			v.add(Error, "", "", "mandatory object 0x%04X is missing", index)
		}
	}
	for _, o := range f.Objects {
		v.object(o)
	}
	return v.ds
}

func validBaud(kbit int) bool {
	for _, b := range BaudRates {
		if b == kbit {
			return true
		}
	}
	return false
}

// extras reports the Extra keys that are standard keys with values that
// did not parse.
func (v *validator) extras(section string, extra []KeyValue, known map[string]field) {
	for _, kv := range extra {
		if _, ok := known[strings.ToLower(kv.Key)]; ok {
			v.add(Error, section, kv.Key, "invalid value %q", kv.Value)
		}
	}
}

func (v *validator) deviceInfo() {
	d := &v.f.DeviceInfo
	v.extras("DeviceInfo", d.Extra, knownDeviceInfo)
	if d.Granularity > 64 {
		v.add(Error, "DeviceInfo", "Granularity", "%d is above 64", d.Granularity)
	}
	if len(d.SupportedBaudRates()) == 0 {
		v.add(Warning, "DeviceInfo", "", "no bit rate supported (BaudRate_xxx)")
	}
	count := func(from, to uint16) (n int) {
		for _, o := range v.f.Objects {
			if o.Index >= from && o.Index <= to && !o.implicit {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		key      string
		declared uint16
		from     uint16
		what     string
	}{
		{"NrOfRXPDO", d.NrOfRXPDO, od.IndexRPDOComm, "RPDO communication"},
		{"NrOfRXPDO", d.NrOfRXPDO, od.IndexRPDOMapping, "RPDO mapping"},
		{"NrOfTXPDO", d.NrOfTXPDO, od.IndexTPDOComm, "TPDO communication"},
		{"NrOfTXPDO", d.NrOfTXPDO, od.IndexTPDOMapping, "TPDO mapping"},
	} {
		if n := count(c.from, c.from+od.MaxPDOs-1); n != int(c.declared) {
			// A warning: Lely CANopen and python canopen accept such files,
			// and the PDO objects present are what a dictionary is built from.
			v.add(Warning, "DeviceInfo", c.key, "%d declared, but the file has %d %s parameter objects", c.declared, n, c.what)
		}
	}
}

func (v *validator) lists() {
	f := v.f
	listed := map[uint16]bool{}
	for _, l := range []struct {
		name string
		list *ObjectList
	}{
		{"MandatoryObjects", f.MandatoryObjects},
		{"OptionalObjects", f.OptionalObjects},
		{"ManufacturerObjects", f.ManufacturerObjects},
	} {
		if l.list == nil {
			continue
		}
		v.list(l.name, "SupportedObjects", l.list)
		for _, index := range l.list.Indices {
			if listed[index] {
				v.add(Warning, l.name, "", "0x%04X is listed more than once", index)
			}
			listed[index] = true
			if o := f.Object(index); o == nil || o.implicit {
				v.add(Error, l.name, "", "0x%04X is listed but has no object section", index)
			}
			var want string
			switch {
			case isMandatory(index):
				want = "MandatoryObjects"
			case index >= 0x2000 && index <= 0x5FFF:
				want = "ManufacturerObjects"
			default:
				want = "OptionalObjects"
			}
			if want != l.name {
				v.add(Warning, l.name, "", "0x%04X belongs in [%s]", index, want)
			}
		}
	}
	for _, o := range f.Objects {
		if !listed[o.Index] && !o.implicit {
			v.add(Warning, sectionName(o.Index, -1), "", "the object is in no object list")
		}
	}
}

// list checks a list's declared count and keys.
func (v *validator) list(section, countKey string, l *ObjectList) {
	if l.Count != len(l.Indices) {
		v.add(Error, section, countKey, "%d declared, %d listed", l.Count, len(l.Indices))
	}
	for _, kv := range l.Extra {
		v.add(Error, section, kv.Key, "unknown key or invalid value %q", kv.Value)
	}
}

// sectionName returns "XXXX" (sub < 0) or "XXXXsubY".
func sectionName(index uint16, sub int) string {
	if sub < 0 {
		return fmt.Sprintf("%04X", index)
	}
	return fmt.Sprintf("%04Xsub%X", index, sub)
}

func validObjectType(t od.ObjectType) bool {
	switch t {
	case od.ObjectNull, od.ObjectDomain, od.ObjectDefType, od.ObjectDefStruct, od.ObjectVar, od.ObjectArray, od.ObjectRecord:
		return true
	}
	return false
}

func (v *validator) object(o *Object) {
	sec := sectionName(o.Index, -1)
	if o.implicit {
		v.add(Error, sec, "", "sub-object or auxiliary sections without the object section")
		return
	}
	v.extras(sec, o.Extra, knownEntry)
	if o.ParameterName == "" {
		v.add(Warning, sec, "ParameterName", "missing")
	}
	if !validObjectType(o.ObjectType) {
		v.add(Error, sec, "ObjectType", "invalid object code 0x%X", uint8(o.ObjectType))
		return
	}
	if (o.ObjectType == od.ObjectDefType || o.ObjectType == od.ObjectDefStruct) && o.Index > 0x025F {
		v.add(Warning, sec, "ObjectType", "%s outside the data type area 0x0001..0x025F", o.ObjectType)
	}
	if o.Index == 0 {
		v.add(Error, sec, "", "index 0x0000 is not used")
	}
	if o.Links != nil {
		v.list(sec+"ObjectLinks", "ObjectLinks", o.Links)
		for _, l := range o.Links.Indices {
			if v.f.Object(l) == nil {
				v.add(Warning, sec+"ObjectLinks", "", "linked object 0x%04X does not exist", l)
			}
		}
	}
	compact := o.CompactSubObj > 0
	if !compact {
		for _, t := range []struct {
			name string
			t    *SubTable
		}{{"Name", o.Names}, {"Value", o.Values}, {"Denotation", o.Denotations}} {
			if t.t != nil {
				v.add(Warning, sec+t.name, "", "only objects with CompactSubObj have this section")
			}
		}
	}
	switch o.ObjectType {
	case od.ObjectVar, od.ObjectDomain:
		if len(o.Subs) > 0 {
			v.add(Error, sec, "", "a %s has no sub-object sections", o.ObjectType)
		}
		if o.SubNumber != 0 {
			v.add(Error, sec, "SubNumber", "a %s has no sub-objects", o.ObjectType)
		}
		if compact {
			v.add(Error, sec, "CompactSubObj", "only an ARRAY can have compact storage")
		}
		v.variable(sec, &o.Entry, o.ObjectType)
	case od.ObjectArray, od.ObjectRecord:
		if compact {
			v.compact(sec, o)
			return
		}
		v.subs(sec, o)
	default:
		if len(o.Subs) > 0 && o.ObjectType != od.ObjectDefStruct {
			v.add(Error, sec, "", "a %s has no sub-object sections", o.ObjectType)
		}
	}
}

func (v *validator) compact(sec string, o *Object) {
	if o.ObjectType != od.ObjectArray {
		v.add(Error, sec, "CompactSubObj", "only an ARRAY can have compact storage")
	}
	if len(o.Subs) > 0 {
		v.add(Error, sec, "CompactSubObj", "an object with compact storage has no sub-object sections")
	}
	if o.SubNumber != 0 {
		v.add(Warning, sec, "SubNumber", "ignored with CompactSubObj")
	}
	v.variable(sec, &o.Entry, od.ObjectVar)
	for _, t := range []struct {
		name string
		t    *SubTable
	}{{"Name", o.Names}, {"Value", o.Values}, {"Denotation", o.Denotations}} {
		if t.t == nil {
			continue
		}
		s := sec + t.name
		if t.t.NrOfEntries != len(t.t.Entries) {
			v.add(Warning, s, "NrOfEntries", "%d declared, %d present", t.t.NrOfEntries, len(t.t.Entries))
		}
		for _, kv := range t.t.Extra {
			v.add(Warning, s, kv.Key, "unknown key")
		}
		for _, sub := range sortedKeys(t.t.Entries) {
			if sub == 0 || sub > o.CompactSubObj {
				v.add(Error, s, fmt.Sprint(sub), "sub-index %d outside 1..%d", sub, o.CompactSubObj)
				continue
			}
			if t.name == "Value" && supported(o.DataType) {
				v.literal(s, fmt.Sprint(sub), o.DataType, t.t.Entries[sub])
			}
		}
	}
}

func (v *validator) subs(sec string, o *Object) {
	if o.SubNumber == 0 {
		v.add(Error, sec, "SubNumber", "missing (required for %s)", o.ObjectType)
	} else if int(o.SubNumber) != len(o.Subs) {
		v.add(Error, sec, "SubNumber", "%d declared, %d sub-object sections present", o.SubNumber, len(o.Subs))
	}
	if len(o.Subs) == 0 {
		return
	}
	s0 := o.Sub(0)
	if s0 == nil {
		v.add(Error, sec, "", "sub-index 0 is missing")
	} else if s0.DataType != canopen.Unsigned8 {
		v.add(Error, sectionName(o.Index, 0), "DataType", "sub-index 0 must be UNSIGNED8")
	}
	var elem canopen.DataType
	for _, s := range o.Subs {
		ss := sectionName(o.Index, int(s.SubIndex))
		v.extras(ss, s.Extra, knownEntry)
		if s.ObjectType != od.ObjectVar && s.ObjectType != od.ObjectDomain {
			v.add(Error, ss, "ObjectType", "a sub-object is a VAR or a DOMAIN, not %s", s.ObjectType)
			continue
		}
		if s.SubNumber != 0 || s.CompactSubObj != 0 {
			v.add(Error, ss, "SubNumber", "a sub-object has no sub-objects")
		}
		v.variable(ss, &s.Entry, s.ObjectType)
		if o.ObjectType == od.ObjectArray && s.SubIndex > 0 {
			if elem == 0 {
				elem = s.DataType
			} else if s.DataType != elem {
				v.add(Warning, ss, "DataType", "ARRAY elements have one data type (%s), not %s", elem, s.DataType)
			}
		}
	}
}

// hasKey reports whether the keys contain key, case-insensitively.
func hasKey(kvs []KeyValue, key string) bool {
	for _, kv := range kvs {
		if strings.EqualFold(kv.Key, key) {
			return true
		}
	}
	return false
}

// supported reports whether a dictionary can hold a variable of type t.
func supported(t canopen.DataType) bool { return t.Known() && t < canopen.PDOCommParam }

// definedType reports whether the file defines the data type with a
// DEFTYPE or DEFSTRUCT object.
func (v *validator) definedType(t canopen.DataType) bool {
	o := v.f.Object(uint16(t))
	return o != nil && (o.ObjectType == od.ObjectDefType || o.ObjectType == od.ObjectDefStruct)
}

// variable checks the fields of a VAR, a DOMAIN, a sub-object or a
// compact array's element description.
func (v *validator) variable(sec string, e *Entry, ot od.ObjectType) {
	t := e.DataType
	if t == 0 && ot == od.ObjectDomain {
		t = canopen.Domain
	}
	if e.AccessType == 0 && !hasKey(e.Extra, "AccessType") {
		v.add(Error, sec, "AccessType", "missing")
	}
	switch {
	case t == 0 && hasKey(e.Extra, "DataType"):
		return
	case t == 0:
		v.add(Error, sec, "DataType", "missing")
		return
	case !supported(t) && v.definedType(t):
		v.add(Warning, sec, "DataType", "type 0x%04X is defined in the file but not supported in a dictionary", uint16(t))
		return
	case !supported(t):
		v.add(Error, sec, "DataType", "unknown or structured data type 0x%04X", uint16(t))
		return
	}
	if e.PDOMapping && t.IsVariable() {
		v.add(Error, sec, "PDOMapping", "%s cannot be mapped into a PDO", t)
	}
	if (e.UploadFile != "" || e.DownloadFile != "") && t != canopen.Domain {
		v.add(Warning, sec, "UploadFile", "UploadFile and DownloadFile apply to DOMAIN objects")
	}
	def, defRel := v.literal(sec, "DefaultValue", t, e.DefaultValue)
	if e.HasParameterValue {
		v.literal(sec, "ParameterValue", t, e.ParameterValue)
	}
	if e.LowLimit == "" && e.HighLimit == "" {
		return
	}
	if !t.IsInteger() && !t.IsFloat() {
		v.add(Warning, sec, "LowLimit", "limits of a %s are ignored", t)
		return
	}
	lo, _ := v.literal(sec, "LowLimit", t, e.LowLimit)
	hi, _ := v.literal(sec, "HighLimit", t, e.HighLimit)
	if e.LowLimit != "" && e.HighLimit != "" && lo != nil && hi != nil && cmp(t, lo, hi) > 0 {
		v.add(Error, sec, "LowLimit", "%s is above HighLimit %s", e.LowLimit, e.HighLimit)
	}
	if def != nil && !defRel {
		if e.LowLimit != "" && lo != nil && cmp(t, def, lo) < 0 {
			v.add(Warning, sec, "DefaultValue", "%q is below LowLimit %s", e.DefaultValue, e.LowLimit)
		}
		if e.HighLimit != "" && hi != nil && cmp(t, def, hi) > 0 {
			v.add(Warning, sec, "DefaultValue", "%q is above HighLimit %s", e.DefaultValue, e.HighLimit)
		}
	}
}

// literal checks that a value literal parses for t and returns its
// encoding ($NODEID counted as 0) and whether it is node-ID-relative.
func (v *validator) literal(sec, key string, t canopen.DataType, lit string) ([]byte, bool) {
	b, rel, err := parseLiteral(t, lit, 0)
	if err != nil {
		v.add(Error, sec, key, "%v", err)
		return nil, rel
	}
	return b, rel
}

// cmp orders two encoded numeric values of type t.
func cmp(t canopen.DataType, a, b []byte) int {
	switch {
	case t.IsFloat():
		x, y := toFloat(t, a), toFloat(t, b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case t.IsSigned():
		x, y := canopen.DecodeInt(a), canopen.DecodeInt(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	x, y := canopen.DecodeUint(a), canopen.DecodeUint(b)
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func toFloat(t canopen.DataType, b []byte) float64 {
	if t == canopen.Real32 {
		return float64(math.Float32frombits(uint32(canopen.DecodeUint(b))))
	}
	return math.Float64frombits(canopen.DecodeUint(b))
}
