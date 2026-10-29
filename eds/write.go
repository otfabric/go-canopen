// SPDX-License-Identifier: MIT

package eds

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/otfabric/go-canopen/od"
)

// Write writes f, an EDS or a DCF (a DCF differs only by its
// [DeviceComissioning] section and ParameterValue keys), in a canonical
// layout: [FileInfo], [DeviceInfo], [DeviceComissioning],
// [DummyUsage], [Comments], then each object list followed by its
// objects, the objects no list names, and the unknown sections. Objects
// are written in index order, each with its [XXXXName], [XXXXValue],
// [XXXXDenotation], sub-object and [XXXXObjectLinks] sections. Lines end
// in LF.
//
// Write fails when a key or value cannot be written so that Parse reads
// it back: a line break in a value, an empty or malformed key or section
// name, an unknown section with a standard name, or an access type out of
// range. Leading and trailing blanks of values are not kept (Parse trims
// them).
func Write(w io.Writer, f *File) error {
	wr := &writer{w: bufio.NewWriter(w)}
	wr.fields("FileInfo", f.FileInfo.fields(), f.FileInfo.Extra)
	wr.fields("DeviceInfo", f.DeviceInfo.fields(), f.DeviceInfo.Extra)
	if c := f.DeviceComissioning; c != nil {
		wr.fields("DeviceComissioning", c.fields(), c.Extra)
	}
	if d := f.DummyUsage; d != nil {
		wr.header("DummyUsage")
		for _, t := range sortedKeys(d.Dummy) {
			v := "0"
			if d.Dummy[t] {
				v = "1"
			}
			wr.kv(fmt.Sprintf("Dummy%04X", t), v)
		}
		wr.extra(d.Extra)
	}
	if c := f.Comments; c != nil {
		wr.header("Comments")
		ex := extraKeys(c.Extra)
		if !ex["lines"] {
			wr.kv("Lines", strconv.Itoa(len(c.Lines)))
		}
		for i, l := range c.Lines {
			wr.kv("Line"+strconv.Itoa(i+1), l)
		}
		wr.extra(c.Extra)
	}
	written := map[uint16]bool{}
	for i, o := range f.Objects {
		if i > 0 && f.Objects[i-1].Index >= o.Index {
			wr.fail(fmt.Errorf("eds: objects not in ascending index order at 0x%04X (call SortObjects)", o.Index))
		}
	}
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
		wr.list(l.name, "SupportedObjects", l.list)
		for _, index := range l.list.Indices {
			if o := f.Object(index); o != nil && !written[index] {
				written[index] = true
				wr.object(o)
			}
		}
	}
	for _, o := range f.Objects {
		if !written[o.Index] {
			written[o.Index] = true
			wr.object(o)
		}
	}
	seen := map[string]bool{}
	for _, s := range f.Unknown {
		if k, _, _ := classify(strings.TrimSpace(s.Name)); k != kindUnknown {
			wr.fail(fmt.Errorf("eds: unknown section [%s] has the name of a standard section", s.Name))
		}
		if seen[strings.ToLower(s.Name)] {
			wr.fail(fmt.Errorf("eds: section [%s] appears twice", s.Name))
		}
		seen[strings.ToLower(s.Name)] = true
		wr.header(s.Name)
		wr.extra(s.Keys)
	}
	if wr.err != nil {
		return wr.err
	}
	return wr.w.Flush()
}

// writer accumulates the first error; bufio.Writer errors are sticky and
// reported by the final Flush, so the individual writes ignore them.
type writer struct {
	w     *bufio.Writer
	err   error
	first bool
}

func (w *writer) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *writer) header(name string) {
	if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "\r\n") {
		w.fail(fmt.Errorf("eds: invalid section name %q", name))
	}
	if w.first {
		_, _ = w.w.WriteString("\n")
	}
	w.first = true
	_, _ = w.w.WriteString("[" + name + "]\n")
}

func (w *writer) kv(key, val string) {
	if key == "" || key != strings.TrimSpace(key) || strings.ContainsAny(key, "=\r\n") || key[0] == ';' || key[0] == '[' {
		w.fail(fmt.Errorf("eds: invalid key %q", key))
	}
	if strings.ContainsAny(val, "\r\n") {
		w.fail(fmt.Errorf("eds: value of %s contains a line break", key))
	}
	_, _ = w.w.WriteString(key + "=" + strings.TrimSpace(val) + "\n")
}

func (w *writer) extra(kvs []KeyValue) {
	seen := map[string]bool{}
	for _, kv := range kvs {
		lk := strings.ToLower(kv.Key)
		if seen[lk] {
			w.fail(fmt.Errorf("eds: key %s appears twice", kv.Key))
		}
		seen[lk] = true
		w.kv(kv.Key, kv.Value)
	}
}

func extraKeys(kvs []KeyValue) map[string]bool {
	m := map[string]bool{}
	for _, kv := range kvs {
		m[strings.ToLower(kv.Key)] = true
	}
	return m
}

// fields writes a standard section: the typed fields, except those an
// Extra key of the same name replaces, then the extras.
func (w *writer) fields(name string, fs []field, extra []KeyValue) {
	w.header(name)
	w.body(fs, extra)
}

func (w *writer) body(fs []field, extra []KeyValue) {
	ex := extraKeys(extra)
	for _, f := range fs {
		if ex[strings.ToLower(f.key)] {
			continue
		}
		if v, ok := f.format(); ok {
			w.kv(f.key, v)
		}
	}
	w.extra(extra)
}

func (w *writer) list(name, countKey string, l *ObjectList) {
	w.header(name)
	ex := extraKeys(l.Extra)
	if !ex[strings.ToLower(countKey)] {
		w.kv(countKey, strconv.Itoa(l.Count))
	}
	n := 1
	for _, index := range l.Indices {
		for ex[strconv.Itoa(n)] {
			n++
		}
		w.kv(strconv.Itoa(n), fmt.Sprintf("0x%04X", index))
		n++
	}
	w.extra(l.Extra)
}

func (w *writer) table(name string, t *SubTable) {
	w.header(name)
	if !extraKeys(t.Extra)["nrofentries"] {
		w.kv("NrOfEntries", strconv.Itoa(t.NrOfEntries))
	}
	for _, s := range sortedKeys(t.Entries) {
		w.kv(strconv.Itoa(int(s)), t.Entries[s])
	}
	w.extra(t.Extra)
}

func (w *writer) entry(name string, e *Entry) {
	if e.AccessType > od.AccessConst {
		w.fail(fmt.Errorf("eds: [%s]: invalid access type %d", name, e.AccessType))
	}
	w.fields(name, e.fields(), e.Extra)
}

func (w *writer) object(o *Object) {
	x := fmt.Sprintf("%04X", o.Index)
	if !o.implicit {
		w.entry(x, &o.Entry)
	}
	if o.Names != nil {
		w.table(x+"Name", o.Names)
	}
	if o.Values != nil {
		w.table(x+"Value", o.Values)
	}
	if o.Denotations != nil {
		w.table(x+"Denotation", o.Denotations)
	}
	for i, s := range o.Subs {
		if i > 0 && o.Subs[i-1].SubIndex >= s.SubIndex {
			w.fail(fmt.Errorf("eds: object 0x%04X: sub-objects not in ascending order at %d (call SortObjects)", o.Index, s.SubIndex))
		}
		w.entry(fmt.Sprintf("%ssub%X", x, s.SubIndex), &s.Entry)
	}
	if o.Links != nil {
		w.list(x+"ObjectLinks", "ObjectLinks", o.Links)
	}
}
