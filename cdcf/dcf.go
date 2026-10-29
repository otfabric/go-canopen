// SPDX-License-Identifier: MIT

package cdcf

import (
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// pdo identifies a PDO: transmit or receive, number 1..512.
type pdo struct {
	transmit bool
	n        int
}

// pdoOf returns the PDO whose communication or mapping parameter an index
// is, and whether it is the mapping.
func pdoOf(index uint16) (k pdo, mapping, ok bool) {
	switch {
	case index >= od.IndexRPDOComm && index < od.IndexRPDOMapping:
		return pdo{false, int(index-od.IndexRPDOComm) + 1}, false, true
	case index >= od.IndexRPDOMapping && index < od.IndexTPDOComm:
		return pdo{false, int(index-od.IndexRPDOMapping) + 1}, true, true
	case index >= od.IndexTPDOComm && index < od.IndexTPDOMapping:
		return pdo{true, int(index-od.IndexTPDOComm) + 1}, false, true
	case index >= od.IndexTPDOMapping && index < od.IndexTPDOMapping+od.MaxPDOs:
		return pdo{true, int(index-od.IndexTPDOMapping) + 1}, true, true
	}
	return pdo{}, false, false
}

func (k pdo) indices() (comm, mapping uint16) {
	if k.transmit {
		return od.IndexTPDOComm + uint16(k.n-1), od.IndexTPDOMapping + uint16(k.n-1)
	}
	return od.IndexRPDOComm + uint16(k.n-1), od.IndexRPDOMapping + uint16(k.n-1)
}

type item struct {
	sub uint8
	e   *eds.Entry
}

// Entries returns the configuration a DCF holds for a node: every
// writable sub-object with a ParameterValue (without the "refuse write on
// download" object flag), in index order, with node-ID-relative values
// evaluated for node (0: the DCF's DeviceComissioning node-ID). For a PDO
// with any configured sub-object the whole communication and mapping
// parameter (every sub-object, current values) follows the other
// entries, so that a manager can apply the PDO as the DCF describes it.
// The PDO entries are in dictionary order: use [PDOSequence] (or
// [FromFile]) to write them as they are.
func Entries(f *eds.File, node canopen.NodeID) ([]Entry, error) {
	if f == nil {
		return nil, fmt.Errorf("cdcf: nil DCF")
	}
	if node == 0 && f.DeviceComissioning != nil {
		node = canopen.NodeID(f.DeviceComissioning.NodeID)
	}
	if err := node.Check(); err != nil {
		return nil, fmt.Errorf("cdcf: DCF node-ID: %w", err)
	}
	d, err := f.Dictionary(node)
	if err != nil {
		return nil, fmt.Errorf("cdcf: DCF: %w", err)
	}
	configured := func(e *eds.Entry) bool { return e.HasParameterValue && e.ObjFlags&1 == 0 }
	var out []Entry
	touched := map[pdo]bool{}
	for _, o := range f.Objects {
		var list []item
		if len(o.Subs) == 0 {
			list = append(list, item{0, &o.Entry})
		}
		for _, s := range o.Subs {
			list = append(list, item{s.SubIndex, &s.Entry})
		}
		for _, it := range list {
			if !configured(it.e) {
				continue
			}
			if k, _, ok := pdoOf(o.Index); ok {
				touched[k] = true
				continue
			}
			v, err := d.Variable(o.Index, it.sub)
			if err != nil || !v.Access.Writable() {
				continue
			}
			data, err := d.Get(o.Index, it.sub)
			if err != nil {
				continue
			}
			out = append(out, Entry{Index: o.Index, SubIndex: it.sub, Data: data})
		}
	}
	for _, o := range d.Objects() {
		k, _, ok := pdoOf(o.Index)
		if !ok || !touched[k] {
			continue
		}
		for _, v := range o.Subs {
			data, err := d.Get(o.Index, v.SubIndex)
			if err != nil {
				continue
			}
			out = append(out, Entry{Index: o.Index, SubIndex: v.SubIndex, Data: data})
		}
	}
	return out, nil
}

// FromFile returns the configuration a DCF holds for a node ([Entries])
// with the PDO parameters in the sequence of [PDOSequence]: a concise DCF
// a manager can write entry by entry.
func FromFile(f *eds.File, node canopen.NodeID) ([]Entry, error) {
	entries, err := Entries(f, node)
	if err != nil {
		return nil, err
	}
	return PDOSequence(entries), nil
}

// PDOSequence returns entries with the PDO parameters (0x1400..0x1BFF)
// rewritten as the sequence CiA 301 requires to change a PDO, so that a
// node accepts them written one by one: the entries of other objects
// first, in their order; then, per PDO in the order the PDOs first
// appear, with the last value given for each sub-object:
//
//  1. the COB-ID (sub-index 1) with bit 31 set (PDO invalid), when the
//     entries set the COB-ID;
//  2. the other communication parameters (sub-indices 2, 3, 5, 6;
//     sub-index 0 and the compatibility entry 4 are dropped);
//  3. when the entries set the number of mapped objects: 0, the mapping
//     entries 1..n, then n (mapping entries above n are dropped);
//     otherwise the mapping entries given;
//  4. the COB-ID as given.
func PDOSequence(entries []Entry) []Entry {
	type group struct {
		comm    map[uint8][]byte
		mapping map[uint8][]byte
	}
	var out []Entry
	var order []pdo
	groups := map[pdo]*group{}
	for _, e := range entries {
		k, mapping, ok := pdoOf(e.Index)
		if !ok {
			out = append(out, e)
			continue
		}
		g := groups[k]
		if g == nil {
			g = &group{comm: map[uint8][]byte{}, mapping: map[uint8][]byte{}}
			groups[k] = g
			order = append(order, k)
		}
		if mapping {
			g.mapping[e.SubIndex] = e.Data
		} else {
			g.comm[e.SubIndex] = e.Data
		}
	}
	for _, k := range order {
		g := groups[k]
		ci, mi := k.indices()
		cob, hasCOB := g.comm[1]
		if hasCOB {
			inv := append([]byte{}, cob...)
			if len(inv) == 4 {
				inv[3] |= 0x80
			}
			out = append(out, Entry{Index: ci, SubIndex: 1, Data: inv})
		}
		for _, s := range []uint8{2, 3, 5, 6} {
			if v, ok := g.comm[s]; ok {
				out = append(out, Entry{Index: ci, SubIndex: s, Data: v})
			}
		}
		if cnt, ok := g.mapping[0]; ok && len(cnt) == 1 {
			out = append(out, Entry{Index: mi, SubIndex: 0, Data: []byte{0}})
			for s := uint8(1); s <= cnt[0] && s <= od.MaxMappedObjects; s++ {
				if v, ok := g.mapping[s]; ok {
					out = append(out, Entry{Index: mi, SubIndex: s, Data: v})
				}
			}
			out = append(out, Entry{Index: mi, SubIndex: 0, Data: cnt})
		} else {
			for s := 1; s <= od.MaxMappedObjects; s++ {
				if v, ok := g.mapping[uint8(s)]; ok {
					out = append(out, Entry{Index: mi, SubIndex: uint8(s), Data: v})
				}
			}
		}
		if hasCOB {
			out = append(out, Entry{Index: ci, SubIndex: 1, Data: cob})
		}
	}
	return out
}
