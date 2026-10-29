// SPDX-License-Identifier: MIT

// Package store keeps the parameters of a CANopen device across restarts:
// the persistence behind the store parameters (0x1010) and restore
// default parameters (0x1011) objects of CiA 301.
//
// A [Store] saves and loads a list of values (index, sub-index, encoded
// value). [Memory] keeps it in memory (a restart of the device in the
// same process); [File] keeps it in a file, as a concise DCF (package
// cdcf), replaced atomically on every save. The node package (option
// node.WithStore) implements the objects on top of a Store:
//
//   - writing the signature "save" ([SignatureSave]) to 0x1010 sub-index 1
//     (all parameters), 2 (communication, 0x1000..0x1FFF), 3 (application,
//     0x6000..0x9FFF) or 4 (manufacturer, 0x2000..0x5FFF) saves the
//     current values of the parameters of that group;
//   - writing "load" ([SignatureLoad]) to the same sub-index of 0x1011
//     deletes the saved values of the group, so that the defaults apply
//     from the next reset on;
//   - at its creation and at each NMT reset the node restores the
//     defaults of the areas the reset concerns and then loads the saved
//     values of those areas.
//
// [AddObjects] adds 0x1010, 0x1011 and the verify configuration object
// 0x1020 to a dictionary.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/od"
)

// Objects of CiA 301 this package is about.
const (
	// IndexStore is the store parameters object (ARRAY of UNSIGNED32).
	IndexStore uint16 = 0x1010
	// IndexRestore is the restore default parameters object (ARRAY of
	// UNSIGNED32).
	IndexRestore uint16 = 0x1011
	// IndexVerifyConfiguration is the verify configuration object
	// (RECORD: sub-index 1 configuration date, 2 configuration time),
	// written by a configuration tool or manager with the configuration
	// and compared by a CiA 302 manager with its expected values
	// (0x1F26, 0x1F27).
	IndexVerifyConfiguration uint16 = 0x1020
)

// Signatures written to 0x1010 and 0x1011 (CiA 301): the ASCII codes of
// "save" and "load" as an UNSIGNED32, least significant byte first.
const (
	SignatureSave uint32 = 0x65766173
	SignatureLoad uint32 = 0x64616F6C
)

// Sub-indices of 0x1010 and 0x1011 (CiA 301), each a group of parameters.
const (
	All           uint8 = 1 // all parameters (0x1000..0x9FFF)
	Communication uint8 = 2 // communication parameters (0x1000..0x1FFF)
	Application   uint8 = 3 // standardised profile parameters (0x6000..0x9FFF)
	Manufacturer  uint8 = 4 // manufacturer-specific parameters (0x2000..0x5FFF)
)

// Range returns the index range of a group (sub-index 1..4 of 0x1010 and
// 0x1011); ok is false for other sub-indices (manufacturer-specific
// groups, which CiA 301 leaves to the device).
func Range(group uint8) (first, last uint16, ok bool) {
	switch group {
	case All:
		return 0x1000, 0x9FFF, true
	case Communication:
		return 0x1000, 0x1FFF, true
	case Application:
		return 0x6000, 0x9FFF, true
	case Manufacturer:
		return 0x2000, 0x5FFF, true
	}
	return 0, 0, false
}

// Excluded reports objects that are never saved although writable: 0x1010
// and 0x1011 themselves, the program download objects 0x1F50 (program
// data) and 0x1F51 (program control), and the manager's requests 0x1F82
// (request NMT) and 0x1F83 (request node guarding), which are commands,
// not parameters.
func Excluded(index uint16) bool {
	switch index {
	case IndexStore, IndexRestore, 0x1F50, 0x1F51, 0x1F82, 0x1F83:
		return true
	}
	return false
}

// Parameter reports whether a sub-object is a parameter that is saved:
// access rw (not the process data of rww and rwr objects, not read-only,
// constant or write-only), and not [Excluded].
func Parameter(v *od.Variable) bool {
	return v.Access == od.AccessRW && !Excluded(v.Index)
}

// Snapshot returns the current values of the parameters ([Parameter]) of
// d with an index in [first, last], in index order.
func Snapshot(d *od.Dictionary, first, last uint16) []cdcf.Entry {
	var out []cdcf.Entry
	for _, o := range d.Objects() {
		if o.Index < first || o.Index > last {
			continue
		}
		for _, v := range o.Subs {
			if !Parameter(v) {
				continue
			}
			data, _ := d.Get(v.Index, v.SubIndex) // v is in d
			out = append(out, cdcf.Entry{Index: v.Index, SubIndex: v.SubIndex, Data: data})
		}
	}
	return out
}

// Merge returns saved with the entries in [first, last] replaced by
// current (which should only hold entries in that range).
func Merge(saved, current []cdcf.Entry, first, last uint16) []cdcf.Entry {
	out := Without(saved, first, last)
	return append(out, current...)
}

// Without returns the entries outside [first, last].
func Without(entries []cdcf.Entry, first, last uint16) []cdcf.Entry {
	out := make([]cdcf.Entry, 0, len(entries))
	for _, e := range entries {
		if e.Index < first || e.Index > last {
			out = append(out, e)
		}
	}
	return out
}

// Apply sets the entries in [first, last] into d locally (no access check
// or hooks; watchers see the changes) and returns the entries it could
// not set (an object the dictionary no longer has, a value of the wrong
// size), joined in the error.
func Apply(d *od.Dictionary, entries []cdcf.Entry, first, last uint16) error {
	var errs []error
	for _, e := range entries {
		if e.Index < first || e.Index > last {
			continue
		}
		if err := d.Set(e.Index, e.SubIndex, e.Data); err != nil {
			errs = append(errs, fmt.Errorf("%04X:%02X: %w", e.Index, e.SubIndex, err))
		}
	}
	return errors.Join(errs...)
}

// Store keeps a device's saved parameter values.
type Store interface {
	// Load returns the saved values (none when nothing was saved).
	Load() ([]cdcf.Entry, error)
	// Save replaces the saved values.
	Save(entries []cdcf.Entry) error
}

// Memory is a [Store] in memory, safe for concurrent use. Its zero value
// is empty.
type Memory struct {
	mu    sync.Mutex
	data  []byte
	saves int
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{} }

// Load implements [Store].
func (m *Memory) Load() ([]cdcf.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cdcf.Decode(m.data)
}

// Save implements [Store].
func (m *Memory) Save(entries []cdcf.Entry) error {
	b, err := cdcf.Encode(entries)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.data = b
	m.saves++
	m.mu.Unlock()
	return nil
}

// Saves returns how many times Save succeeded.
func (m *Memory) Saves() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saves
}

// File is a [Store] in a file holding a concise DCF. Save writes a
// temporary file in the same directory, syncs it and renames it over the
// file, so that the file always holds a complete set of values. A
// missing file is an empty store. File is safe for concurrent use within
// one process.
type File struct {
	path string
	mu   sync.Mutex
}

// NewFile returns a store kept in the file at path.
func NewFile(path string) *File { return &File{path: path} }

// Path returns the file's path.
func (f *File) Path() string { return f.path }

// Load implements [Store].
func (f *File) Load() ([]cdcf.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("store: %s: %w: empty file", f.path, cdcf.ErrFormat)
	}
	entries, err := cdcf.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", f.path, err)
	}
	return entries, nil
}

// Save implements [Store].
func (f *File) Save(entries []cdcf.Entry) error {
	b, err := cdcf.Encode(entries)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, f.path)
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// AddObjects adds to d, where missing, the store parameters (0x1010) and
// restore default parameters (0x1011) objects with sub-indices 1..4
// (UNSIGNED32, rw) and the verify configuration object 0x1020
// (sub-index 1 configuration date, 2 configuration time, UNSIGNED32,
// rw).
func AddObjects(d *od.Dictionary) error {
	u32 := canopen.Unsigned32
	objs := []*od.Object{
		od.NewArray(IndexStore, "Store parameters", u32, od.AccessRW, 4, nil, od.AccessConst),
		od.NewArray(IndexRestore, "Restore default parameters", u32, od.AccessRW, 4, nil, od.AccessConst),
		od.NewRecord(IndexVerifyConfiguration, "Verify configuration",
			od.Sub(1, "Configuration date", u32, od.AccessRW, nil),
			od.Sub(2, "Configuration time", u32, od.AccessRW, nil)),
	}
	names := []string{"Save all parameters", "Save communication parameters", "Save application parameters", "Save manufacturer defined parameters"}
	for i, n := range names {
		objs[0].Subs[i+1].Name = n
		objs[1].Subs[i+1].Name = "Restore " + n[len("Save "):]
	}
	for _, o := range objs {
		if d.Object(o.Index) != nil {
			continue
		}
		if err := d.Add(o); err != nil {
			return err
		}
	}
	return nil
}
