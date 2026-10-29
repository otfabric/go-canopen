// SPDX-License-Identifier: MIT

// Package od is the CANopen object dictionary (CiA 301 7.4): objects
// addressed by a 16-bit index and an 8-bit sub-index, with data types,
// access rights, PDO mappability, default values and limits.
//
// A [Dictionary] distinguishes two kinds of access. Remote accesses ([Dictionary.Read],
// [Dictionary.Write]) come from the bus (an SDO server, a received PDO):
// they obey the access rights and limits and call the hooks, and refuse
// with an [canopen.AbortCode] that an SDO server sends unchanged. Local
// accesses ([Dictionary.Get], [Dictionary.Set]) come from the application:
// they bypass access rights but still check the data type. Watchers see
// every successful change of either kind.
//
// A Dictionary is safe for concurrent use. Hooks and watchers run without
// the dictionary lock held, so they may access the dictionary.
package od

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/otfabric/go-canopen"
)

// ObjectType is the object code of an object (CiA 301 table 42).
type ObjectType uint8

// Object codes.
const (
	ObjectNull      ObjectType = 0x0
	ObjectDomain    ObjectType = 0x2
	ObjectDefType   ObjectType = 0x5
	ObjectDefStruct ObjectType = 0x6
	ObjectVar       ObjectType = 0x7
	ObjectArray     ObjectType = 0x8
	ObjectRecord    ObjectType = 0x9
)

// String returns the CiA 301 name.
func (t ObjectType) String() string {
	switch t {
	case ObjectNull:
		return "NULL"
	case ObjectDomain:
		return "DOMAIN"
	case ObjectDefType:
		return "DEFTYPE"
	case ObjectDefStruct:
		return "DEFSTRUCT"
	case ObjectVar:
		return "VAR"
	case ObjectArray:
		return "ARRAY"
	case ObjectRecord:
		return "RECORD"
	}
	return fmt.Sprintf("ObjectType(%d)", uint8(t))
}

// Access is the access right of a sub-object.
type Access uint8

// Access rights (CiA 306 AccessType).
const (
	AccessRO    Access = iota + 1 // read only; the value may change
	AccessWO                      // write only
	AccessRW                      // read and write
	AccessRWR                     // read and write, mapped in a TPDO (process input)
	AccessRWW                     // read and write, mapped in an RPDO (process output)
	AccessConst                   // read only, the value never changes
)

var accessNames = [...]string{"", "ro", "wo", "rw", "rwr", "rww", "const"}

// String returns the CiA 306 spelling.
func (a Access) String() string {
	if int(a) < len(accessNames) && a != 0 {
		return accessNames[a]
	}
	return fmt.Sprintf("Access(%d)", uint8(a))
}

// ParseAccess parses a CiA 306 AccessType, case-insensitively.
func ParseAccess(s string) (Access, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, n := range accessNames {
		if n != "" && n == s {
			return Access(i), nil
		}
	}
	return 0, fmt.Errorf("od: unknown access type %q", s)
}

// Readable reports whether the bus may read the value.
func (a Access) Readable() bool { return a != AccessWO && a != 0 }

// Writable reports whether the bus may write the value.
func (a Access) Writable() bool {
	return a == AccessWO || a == AccessRW || a == AccessRWR || a == AccessRWW
}

// Variable is one sub-object: the unit an SDO transfers and a PDO maps.
// Its fields describe it; the current value is held by the [Dictionary]
// it belongs to. Do not change the fields of a Variable once it has been
// added to a dictionary.
type Variable struct {
	Index    uint16
	SubIndex uint8
	Name     string
	DataType canopen.DataType
	Access   Access
	// PDOMappable allows mapping into a PDO.
	PDOMappable bool
	// Default is the encoded default value. When NodeIDRelative is true
	// the node-ID is added to it (EDS "$NODEID+..." defaults).
	Default        []byte
	NodeIDRelative bool
	// Low and High are optional encoded inclusive limits for numeric
	// types; nil means no limit.
	Low, High []byte
	// MaxSize bounds the length of a variable-length value (strings,
	// DOMAIN); 0 means unbounded.
	MaxSize int
}

// Key returns "IIII:SS", the index and sub-index in hexadecimal.
func (v *Variable) Key() string { return fmt.Sprintf("%04X:%02X", v.Index, v.SubIndex) }

// Object is one entry of the dictionary: a VAR (one sub-object, sub-index
// 0), an ARRAY or a RECORD (sub-index 0 holding the highest sub-index, and
// the sub-objects), or a DOMAIN.
type Object struct {
	Index uint16
	Name  string
	Type  ObjectType
	// Subs are the sub-objects in ascending sub-index order. Records may
	// have gaps.
	Subs []*Variable
}

// Sub returns the sub-object with the sub-index, or nil.
func (o *Object) Sub(sub uint8) *Variable {
	i := sort.Search(len(o.Subs), func(i int) bool { return o.Subs[i].SubIndex >= sub })
	if i < len(o.Subs) && o.Subs[i].SubIndex == sub {
		return o.Subs[i]
	}
	return nil
}

// ReadHook is called on a remote read of a sub-object with its stored
// value; it returns the value to deliver (e.g. a value computed on
// demand). Returning an error (preferably an [canopen.AbortCode]) refuses
// the read.
type ReadHook func(v *Variable, stored []byte) ([]byte, error)

// WriteHook is called on a remote write of a sub-object before the value
// is stored, after the access, length and limit checks. Returning an
// error (preferably an [canopen.AbortCode]) refuses the write.
type WriteHook func(v *Variable, data []byte) error

// Watcher is called after a value changed, by a remote or a local write.
type Watcher func(v *Variable, data []byte)

// hookKey addresses a sub-object (sub ≥ 0) or every sub-object of an
// index (sub = -1).
type hookKey struct {
	index uint16
	sub   int
}

// Dictionary is an object dictionary with the current values of its
// sub-objects.
type Dictionary struct {
	mu       sync.RWMutex
	objects  map[uint16]*Object
	values   map[*Variable][]byte
	nodeID   canopen.NodeID
	reads    map[hookKey][]hook[ReadHook]
	writes   map[hookKey][]hook[WriteHook]
	watchers map[int]Watcher
	nextW    int
}

// New returns an empty dictionary for a node; node-ID-relative defaults
// use the node-ID (which may be 0 when not known yet, see SetNodeID).
func New(node canopen.NodeID) *Dictionary {
	return &Dictionary{
		objects:  map[uint16]*Object{},
		values:   map[*Variable][]byte{},
		nodeID:   node,
		reads:    map[hookKey][]hook[ReadHook]{},
		writes:   map[hookKey][]hook[WriteHook]{},
		watchers: map[int]Watcher{},
	}
}

// NodeID returns the node-ID the defaults are evaluated for.
func (d *Dictionary) NodeID() canopen.NodeID {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.nodeID
}

// SetNodeID changes the node-ID node-ID-relative defaults use. It does not
// change current values: call [Dictionary.Reset] afterwards, as a reset
// communication does.
func (d *Dictionary) SetNodeID(node canopen.NodeID) {
	d.mu.Lock()
	d.nodeID = node
	d.mu.Unlock()
}

// Add adds an object and sets its values to their defaults. It checks the
// object: sub-objects sorted and unique, a VAR with exactly sub-index 0,
// known data types, defaults and limits of the right size.
func (d *Dictionary) Add(o *Object) error {
	if err := check(o); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.objects[o.Index]; dup {
		return fmt.Errorf("od: object 0x%04X already exists", o.Index)
	}
	d.objects[o.Index] = o
	for _, v := range o.Subs {
		d.values[v] = d.defaultLocked(v)
	}
	return nil
}

func check(o *Object) error {
	if o == nil {
		return fmt.Errorf("od: nil object")
	}
	if len(o.Subs) == 0 {
		return fmt.Errorf("od: object 0x%04X has no sub-objects", o.Index)
	}
	if o.Type == ObjectVar || o.Type == ObjectDomain {
		if len(o.Subs) != 1 || o.Subs[0].SubIndex != 0 {
			return fmt.Errorf("od: %s 0x%04X must have exactly sub-index 0", o.Type, o.Index)
		}
	}
	for i, v := range o.Subs {
		if v == nil {
			return fmt.Errorf("od: object 0x%04X: nil sub-object", o.Index)
		}
		if v.Index != o.Index {
			return fmt.Errorf("od: object 0x%04X: sub-object %s has another index", o.Index, v.Key())
		}
		if i > 0 && o.Subs[i-1].SubIndex >= v.SubIndex {
			return fmt.Errorf("od: object 0x%04X: sub-indices not ascending at %d", o.Index, v.SubIndex)
		}
		if !v.DataType.Known() || v.DataType >= canopen.PDOCommParam {
			return fmt.Errorf("od: %s: unsupported data type %s", v.Key(), v.DataType)
		}
		if v.Access == 0 || v.Access > AccessConst {
			return fmt.Errorf("od: %s: no access type", v.Key())
		}
		n := v.DataType.Size()
		for what, b := range map[string][]byte{"default": v.Default, "low limit": v.Low, "high limit": v.High} {
			if b != nil && n != 0 && len(b) != n {
				return fmt.Errorf("od: %s: %s has %d bytes, %s needs %d", v.Key(), what, len(b), v.DataType, n)
			}
		}
		if v.NodeIDRelative && !v.DataType.IsInteger() {
			return fmt.Errorf("od: %s: a node-ID-relative default needs an integer type", v.Key())
		}
	}
	return nil
}

// defaultLocked returns a copy of v's default for the current node-ID.
func (d *Dictionary) defaultLocked(v *Variable) []byte {
	n := v.DataType.Size()
	b := make([]byte, max(n, len(v.Default)))
	copy(b, v.Default)
	if n == 0 {
		return b[:len(v.Default)]
	}
	if v.NodeIDRelative {
		x := canopen.DecodeUint(b) + uint64(d.nodeID)
		for i := range b {
			b[i] = byte(x >> (8 * i))
		}
	}
	return b
}

// DefaultValue returns v's default value evaluated for the dictionary's
// node-ID.
func (d *Dictionary) DefaultValue(v *Variable) []byte {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.defaultLocked(v)
}

// Remove deletes an object; hooks registered for it stay.
func (d *Dictionary) Remove(index uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if o, ok := d.objects[index]; ok {
		for _, v := range o.Subs {
			delete(d.values, v)
		}
		delete(d.objects, index)
	}
}

// Object returns the object with the index, or nil.
func (d *Dictionary) Object(index uint16) *Object {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.objects[index]
}

// Objects returns every object in ascending index order.
func (d *Dictionary) Objects() []*Object {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*Object, 0, len(d.objects))
	for _, o := range d.objects {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b *Object) int { return int(a.Index) - int(b.Index) })
	return out
}

// Variable returns a sub-object. The error is [canopen.AbortNoObject] or
// [canopen.AbortNoSubIndex].
func (d *Dictionary) Variable(index uint16, sub uint8) (*Variable, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.variableLocked(index, sub)
}

func (d *Dictionary) variableLocked(index uint16, sub uint8) (*Variable, error) {
	o := d.objects[index]
	if o == nil {
		return nil, canopen.AbortNoObject
	}
	v := o.Sub(sub)
	if v == nil {
		return nil, canopen.AbortNoSubIndex
	}
	return v, nil
}

// Read is a remote read: it checks the access right, calls the read hooks
// and returns a copy of the value.
func (d *Dictionary) Read(index uint16, sub uint8) ([]byte, error) {
	d.mu.RLock()
	v, err := d.variableLocked(index, sub)
	if err != nil {
		d.mu.RUnlock()
		return nil, err
	}
	if !v.Access.Readable() {
		d.mu.RUnlock()
		return nil, canopen.AbortWriteOnly
	}
	data := bytes.Clone(d.values[v])
	hooks := hooksFor(d.reads, index, sub)
	d.mu.RUnlock()
	for _, h := range hooks {
		if data, err = h(v, data); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// Write is a remote write: it checks the access right, the length and the
// limits, calls the write hooks, stores the value and notifies the
// watchers.
func (d *Dictionary) Write(index uint16, sub uint8, data []byte) error {
	d.mu.RLock()
	v, err := d.variableLocked(index, sub)
	if err != nil {
		d.mu.RUnlock()
		return err
	}
	hooks := hooksFor(d.writes, index, sub)
	d.mu.RUnlock()
	if !v.Access.Writable() {
		return canopen.AbortReadOnly
	}
	if err := Validate(v, data); err != nil {
		return err
	}
	for _, h := range hooks {
		if err := h(v, data); err != nil {
			return err
		}
	}
	d.store(v, data)
	return nil
}

// Validate checks data against v's data type, size and limits, and
// returns the abort code a write of it gets.
func Validate(v *Variable, data []byte) error {
	t := v.DataType
	if n := t.Size(); n != 0 {
		switch {
		case len(data) > n:
			return canopen.AbortLengthHigh
		case len(data) < n:
			return canopen.AbortLengthLow
		}
	} else if v.MaxSize > 0 && len(data) > v.MaxSize {
		return canopen.AbortLengthHigh
	}
	if t == canopen.Boolean && data[0] > 1 {
		return canopen.AbortInvalidValue
	}
	if v.Low != nil && compare(t, data, v.Low) < 0 {
		return canopen.AbortValueLow
	}
	if v.High != nil && compare(t, data, v.High) > 0 {
		return canopen.AbortValueHigh
	}
	return nil
}

// compare orders two encoded numeric values of type t.
func compare(t canopen.DataType, a, b []byte) int {
	switch {
	case t.IsFloat():
		x, _ := canopen.Decode(t, a)
		y, _ := canopen.Decode(t, b)
		var fx, fy float64
		switch xv := x.(type) {
		case float32:
			fx, fy = float64(xv), float64(y.(float32))
		case float64:
			fx, fy = xv, y.(float64)
		}
		switch {
		case fx < fy:
			return -1
		case fx > fy:
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
	case t.IsInteger():
		x, y := canopen.DecodeUint(a), canopen.DecodeUint(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return 0
}

// Get is a local read: the current value without access check or hooks.
func (d *Dictionary) Get(index uint16, sub uint8) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, err := d.variableLocked(index, sub)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(d.values[v]), nil
}

// Set is a local write: it checks the data type length (not the access
// right, limits or hooks), stores the value and notifies the watchers.
func (d *Dictionary) Set(index uint16, sub uint8, data []byte) error {
	v, err := d.Variable(index, sub)
	if err != nil {
		return err
	}
	if n := v.DataType.Size(); n != 0 && len(data) != n {
		return fmt.Errorf("%w: %s %s needs %d bytes, got %d", canopen.ErrDataType, v.Key(), v.DataType, n, len(data))
	}
	d.store(v, data)
	return nil
}

func (d *Dictionary) store(v *Variable, data []byte) {
	data = bytes.Clone(data)
	if data == nil {
		data = []byte{}
	}
	d.mu.Lock()
	d.values[v] = data
	ws := make([]Watcher, 0, len(d.watchers))
	keys := make([]int, 0, len(d.watchers))
	for k := range d.watchers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ws = append(ws, d.watchers[k])
	}
	d.mu.Unlock()
	for _, w := range ws {
		w(v, bytes.Clone(data))
	}
}

// GetValue returns the current value decoded ([canopen.Decode]).
func (d *Dictionary) GetValue(index uint16, sub uint8) (any, error) {
	v, err := d.Variable(index, sub)
	if err != nil {
		return nil, err
	}
	b, _ := d.Get(index, sub)
	return canopen.Decode(v.DataType, b)
}

// SetValue encodes a Go value with the sub-object's data type
// ([canopen.Encode]) and stores it locally.
func (d *Dictionary) SetValue(index uint16, sub uint8, x any) error {
	v, err := d.Variable(index, sub)
	if err != nil {
		return err
	}
	b, err := canopen.Encode(v.DataType, x)
	if err != nil {
		return fmt.Errorf("%s: %w", v.Key(), err)
	}
	return d.Set(index, sub, b)
}

// GetUint returns an unsigned (or BOOLEAN) value as uint64.
func (d *Dictionary) GetUint(index uint16, sub uint8) (uint64, error) {
	b, err := d.Get(index, sub)
	if err != nil {
		return 0, err
	}
	return canopen.DecodeUint(b), nil
}

// SetUint stores an unsigned value, range-checked against the data type.
func (d *Dictionary) SetUint(index uint16, sub uint8, x uint64) error {
	return d.SetValue(index, sub, x)
}

// Reset restores the defaults of every object with an index in [from,
// to], as an NMT reset does (communication: 0x1000..0x1FFF; application:
// 0x2000..0x9FFF). Watchers are notified of values that changed.
func (d *Dictionary) Reset(from, to uint16) {
	for _, o := range d.Objects() {
		if o.Index < from || o.Index > to {
			continue
		}
		for _, v := range o.Subs {
			def := d.DefaultValue(v)
			cur, _ := d.Get(v.Index, v.SubIndex)
			if !bytes.Equal(cur, def) {
				d.store(v, def)
			}
		}
	}
}

// OnRead registers a read hook for a sub-object, or for every sub-object
// of the index when sub is -1. Hooks run in registration order; the more
// specific (sub ≥ 0) hooks run first. The returned function removes the
// hook.
func (d *Dictionary) OnRead(index uint16, sub int, h ReadHook) (remove func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := hookKey{index, sub}
	id := d.nextW
	d.nextW++
	d.reads[k] = append(d.reads[k], hook[ReadHook]{id, h})
	return func() { d.mu.Lock(); d.reads[k] = without(d.reads[k], id); d.mu.Unlock() }
}

// OnWrite registers a write hook for a sub-object, or for every
// sub-object of the index when sub is -1. The returned function removes
// the hook.
func (d *Dictionary) OnWrite(index uint16, sub int, h WriteHook) (remove func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := hookKey{index, sub}
	id := d.nextW
	d.nextW++
	d.writes[k] = append(d.writes[k], hook[WriteHook]{id, h})
	return func() { d.mu.Lock(); d.writes[k] = without(d.writes[k], id); d.mu.Unlock() }
}

type hook[H any] struct {
	id int
	h  H
}

func without[H any](hs []hook[H], id int) []hook[H] {
	out := hs[:0:0]
	for _, x := range hs {
		if x.id != id {
			out = append(out, x)
		}
	}
	return out
}

// hooksFor returns the hooks of a sub-object, the specific ones first.
func hooksFor[H any](m map[hookKey][]hook[H], index uint16, sub uint8) []H {
	var out []H
	for _, x := range m[hookKey{index, int(sub)}] {
		out = append(out, x.h)
	}
	for _, x := range m[hookKey{index, -1}] {
		out = append(out, x.h)
	}
	return out
}

// Watch registers a watcher for every value change; the returned function
// removes it.
func (d *Dictionary) Watch(w Watcher) (cancel func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.nextW
	d.nextW++
	d.watchers[id] = w
	return func() {
		d.mu.Lock()
		delete(d.watchers, id)
		d.mu.Unlock()
	}
}
