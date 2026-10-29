// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
)

// dcfItem is a sub-object section of a DCF object.
type dcfItem struct {
	sub uint8
	e   *eds.Entry
}

// DCFEntries returns the configuration a DCF holds for a node, as entries
// for [Master.Configure]: every writable sub-object with a ParameterValue
// (without the "refuse write on download" object flag), in index order,
// with node-ID-relative values evaluated for node (0: the DCF's
// DeviceComissioning node-ID). For a PDO with any configured sub-object
// the whole communication and mapping parameter is included, so that
// Configure applies the PDO as the DCF describes it.
func DCFEntries(f *eds.File, node canopen.NodeID) ([]Entry, error) {
	if node == 0 && f.DeviceComissioning != nil {
		node = canopen.NodeID(f.DeviceComissioning.NodeID)
	}
	if err := node.Check(); err != nil {
		return nil, fmt.Errorf("master: DCF node-ID: %w", err)
	}
	d, err := f.Dictionary(node)
	if err != nil {
		return nil, fmt.Errorf("master: DCF: %w", err)
	}
	configured := func(e *eds.Entry) bool { return e.HasParameterValue && e.ObjFlags&1 == 0 }
	var out []Entry
	touched := map[pdoKey]bool{}
	for _, o := range f.Objects {
		var list []dcfItem
		if len(o.Subs) == 0 {
			list = append(list, dcfItem{0, &o.Entry})
		}
		for _, s := range o.Subs {
			list = append(list, dcfItem{s.SubIndex, &s.Entry})
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
	// The touched PDOs, whole, in index order.
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

// ConfigureFromDCF writes the configuration of a DCF to a node
// ([DCFEntries], [Master.Configure]). node 0 takes the DCF's node-ID.
func (m *Master) ConfigureFromDCF(ctx context.Context, node canopen.NodeID, f *eds.File) error {
	entries, err := DCFEntries(f, node)
	if err != nil {
		return err
	}
	if node == 0 {
		node = canopen.NodeID(f.DeviceComissioning.NodeID)
	}
	return m.Configure(ctx, node, entries)
}
