// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/otfabric/go-canopen/od"
)

// ErrSyntax reports a line Parse cannot read; the error is a
// [*SyntaxError].
var ErrSyntax = errors.New("eds: syntax error")

// SyntaxError is a line Parse cannot read.
type SyntaxError struct {
	Line int    // 1-based line number
	Msg  string // what is wrong
}

// Error implements error.
func (e *SyntaxError) Error() string { return fmt.Sprintf("eds: line %d: %s", e.Line, e.Msg) }

// Unwrap returns [ErrSyntax].
func (e *SyntaxError) Unwrap() error { return ErrSyntax }

// ParseFile parses an EDS or DCF file.
func ParseFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBytes(b)
}

// Parse parses an EDS or DCF file. It fails only on lines it cannot read;
// use [File.Validate] for the consistency checks.
func Parse(r io.Reader) (*File, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ParseBytes(b)
}

// ParseBytes parses an EDS or DCF file held in memory.
func ParseBytes(b []byte) (*File, error) {
	secs, dups, err := scan(b)
	if err != nil {
		return nil, err
	}
	f := &File{dups: dups}
	objs := map[uint16]*Object{}
	for _, s := range secs {
		f.section(s, objs)
	}
	for _, o := range objs {
		f.Objects = append(f.Objects, o)
	}
	f.SortObjects()
	return f, nil
}

// rawSection is a section as read, duplicate keys and sections merged.
type rawSection struct {
	name  string
	kind  kind
	index uint16
	sub   uint8
	keys  []KeyValue
	pos   map[string]int // lower-case key → position in keys
}

// scan splits the file into sections. Repeated sections are merged and a
// repeated key replaces the earlier value; both are reported.
func scan(b []byte) ([]*rawSection, []Diagnostic, error) {
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	var (
		secs []*rawSection
		byID = map[string]*rawSection{}
		cur  *rawSection
		dups []Diagnostic
	)
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n") // classic Mac OS line ends
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			if line[len(line)-1] != ']' {
				return nil, nil, &SyntaxError{n + 1, "section header without ]"}
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return nil, nil, &SyntaxError{n + 1, "empty section name"}
			}
			k, index, sub := classify(name)
			id := strings.ToLower(name)
			if k != kindUnknown {
				id = fmt.Sprintf("%d/%04X/%02X", k, index, sub)
			}
			if s, ok := byID[id]; ok {
				dups = append(dups, Diagnostic{Warning, name, "", "section appears more than once; the sections are merged"})
				cur = s
				continue
			}
			cur = &rawSection{name: name, kind: k, index: index, sub: sub, pos: map[string]int{}}
			byID[id] = cur
			secs = append(secs, cur)
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, nil, &SyntaxError{n + 1, fmt.Sprintf("%q is neither a section, a key=value pair nor a comment", trunc(line))}
		}
		key, val := strings.TrimSpace(line[:eq]), strings.TrimSpace(line[eq+1:])
		if key == "" {
			return nil, nil, &SyntaxError{n + 1, "empty key"}
		}
		if cur == nil {
			return nil, nil, &SyntaxError{n + 1, fmt.Sprintf("key %q outside a section", trunc(key))}
		}
		lk := strings.ToLower(key)
		if i, ok := cur.pos[lk]; ok {
			dups = append(dups, Diagnostic{Warning, cur.name, key, "key appears more than once; the last value is used"})
			cur.keys[i].Value = val
			continue
		}
		cur.pos[lk] = len(cur.keys)
		cur.keys = append(cur.keys, KeyValue{key, val})
	}
	return secs, dups, nil
}

func trunc(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// kind classifies a section.
type kind int

const (
	kindUnknown kind = iota
	kindFileInfo
	kindDeviceInfo
	kindComm
	kindDummy
	kindComments
	kindMandatory
	kindOptional
	kindManufacturer
	kindObject
	kindSub
	kindName
	kindValue
	kindDenotation
	kindLinks
)

var namedKinds = map[string]kind{
	"fileinfo":            kindFileInfo,
	"deviceinfo":          kindDeviceInfo,
	"devicecomissioning":  kindComm,
	"devicecommissioning": kindComm,
	"dummyusage":          kindDummy,
	"comments":            kindComments,
	"mandatoryobjects":    kindMandatory,
	"optionalobjects":     kindOptional,
	"manufacturerobjects": kindManufacturer,
}

var suffixKinds = map[string]kind{
	"":            kindObject,
	"name":        kindName,
	"value":       kindValue,
	"denotation":  kindDenotation,
	"objectlinks": kindLinks,
}

// classify recognises a section name: a standard section, or one of
// XXXX, XXXXsubY, XXXXName, XXXXValue, XXXXDenotation, XXXXObjectLinks
// with XXXX four hexadecimal digits and Y one or two.
func classify(name string) (k kind, index uint16, sub uint8) {
	lower := strings.ToLower(name)
	if k, ok := namedKinds[lower]; ok {
		return k, 0, 0
	}
	if len(lower) < 4 {
		return kindUnknown, 0, 0
	}
	x, err := strconv.ParseUint(lower[:4], 16, 16)
	if err != nil {
		return kindUnknown, 0, 0
	}
	rest := lower[4:]
	if k, ok := suffixKinds[rest]; ok {
		return k, uint16(x), 0
	}
	if strings.HasPrefix(rest, "sub") && len(rest) >= 4 && len(rest) <= 5 {
		s, err := strconv.ParseUint(rest[3:], 16, 8)
		if err == nil {
			return kindSub, uint16(x), uint8(s)
		}
	}
	return kindUnknown, 0, 0
}

// applyFields stores the keys of a standard section in its fields; the
// keys that are not fields or do not parse are returned as extras.
func applyFields(fs []field, keys []KeyValue) (extra []KeyValue) {
	m := fieldMap(fs)
	for _, kv := range keys {
		if f, ok := m[strings.ToLower(kv.Key)]; ok && f.parse(kv.Value) {
			continue
		}
		extra = append(extra, kv)
	}
	return extra
}

// section interprets one section into f.
func (f *File) section(s *rawSection, objs map[uint16]*Object) {
	switch s.kind {
	case kindFileInfo:
		f.FileInfo.Extra = applyFields(f.FileInfo.fields(), s.keys)
	case kindDeviceInfo:
		f.DeviceInfo.Extra = applyFields(f.DeviceInfo.fields(), s.keys)
	case kindComm:
		c := &DeviceComissioning{}
		c.Extra = applyFields(c.fields(), s.keys)
		f.DeviceComissioning = c
	case kindDummy:
		f.DummyUsage = parseDummy(s.keys)
	case kindComments:
		f.Comments = parseComments(s.keys)
	case kindMandatory:
		f.MandatoryObjects = parseList("SupportedObjects", s.keys)
	case kindOptional:
		f.OptionalObjects = parseList("SupportedObjects", s.keys)
	case kindManufacturer:
		f.ManufacturerObjects = parseList("SupportedObjects", s.keys)
	case kindObject, kindSub, kindName, kindValue, kindDenotation, kindLinks:
		o := objs[s.index]
		if o == nil {
			o = &Object{Index: s.index, implicit: true}
			o.ObjectType = od.ObjectVar
			objs[s.index] = o
		}
		switch s.kind {
		case kindObject:
			o.implicit = false
			o.Extra = applyFields(o.fields(), s.keys)
		case kindSub:
			so := &SubObject{SubIndex: s.sub}
			so.ObjectType = od.ObjectVar
			so.Extra = applyFields(so.fields(), s.keys)
			o.Subs = append(o.Subs, so)
		case kindName:
			o.Names = parseTable(s.keys)
		case kindValue:
			o.Values = parseTable(s.keys)
		case kindDenotation:
			o.Denotations = parseTable(s.keys)
		case kindLinks:
			o.Links = parseList("ObjectLinks", s.keys)
		default: // no special case
		}
	default:
		f.Unknown = append(f.Unknown, &Section{Name: s.name, Keys: s.keys})
	}
}

func parseDummy(keys []KeyValue) *DummyUsage {
	d := &DummyUsage{Dummy: map[uint16]bool{}}
	for _, kv := range keys {
		lk := strings.ToLower(kv.Key)
		if len(lk) == 9 && strings.HasPrefix(lk, "dummy") {
			t, err := strconv.ParseUint(lk[5:], 16, 16)
			var b bool
			if err == nil && boolField("", &b, false).parse(kv.Value) {
				d.Dummy[uint16(t)] = b
				continue
			}
		}
		d.Extra = append(d.Extra, kv)
	}
	return d
}

func parseComments(keys []KeyValue) *Comments {
	c := &Comments{}
	lines := map[int]string{}
	for _, kv := range keys {
		lk := strings.ToLower(kv.Key)
		if lk == "lines" {
			if _, err := strconv.ParseUint(kv.Value, 10, 16); err == nil {
				continue
			}
		}
		if strings.HasPrefix(lk, "line") {
			if n, err := strconv.Atoi(lk[4:]); err == nil && n > 0 && lk[4] != '+' {
				lines[n] = kv.Value
				continue
			}
		}
		c.Extra = append(c.Extra, kv)
	}
	for _, n := range sortedKeys(lines) {
		c.Lines = append(c.Lines, lines[n])
	}
	return c
}

func parseList(countKey string, keys []KeyValue) *ObjectList {
	l := &ObjectList{}
	idx := map[int]uint16{}
	lc := strings.ToLower(countKey)
	for _, kv := range keys {
		lk := strings.ToLower(kv.Key)
		if lk == lc {
			var n uint16
			if uintField("", &n, "", false).parse(kv.Value) {
				l.Count = int(n)
				continue
			}
		} else if n, err := strconv.ParseUint(lk, 10, 16); err == nil && lk[0] != '+' {
			var x uint16
			if uintField("", &x, "", false).parse(kv.Value) && kv.Value != "" {
				idx[int(n)] = x
				continue
			}
		}
		l.Extra = append(l.Extra, kv)
	}
	for _, n := range sortedKeys(idx) {
		l.Indices = append(l.Indices, idx[n])
	}
	return l
}

func parseTable(keys []KeyValue) *SubTable {
	t := &SubTable{Entries: map[uint8]string{}}
	for _, kv := range keys {
		lk := strings.ToLower(kv.Key)
		if lk == "nrofentries" {
			var n uint16
			if uintField("", &n, "", false).parse(kv.Value) {
				t.NrOfEntries = int(n)
				continue
			}
		} else if n, err := strconv.ParseUint(lk, 10, 8); err == nil && lk[0] != '+' {
			t.Entries[uint8(n)] = kv.Value
			continue
		}
		t.Extra = append(t.Extra, kv)
	}
	return t
}

func sortedKeys[K int | uint8 | uint16, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
