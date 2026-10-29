// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Entry is one value to write into a node's dictionary: the neutral form
// of a node configuration (from a DCF, a tool, or code).
type Entry struct {
	Index    uint16
	SubIndex uint8
	// Data is the value in the encoding of the object's data type
	// (canopen.Encode).
	Data []byte
}

// String formats the entry as "IIII:SS=hex".
func (e Entry) String() string { return fmt.Sprintf("%04X:%02X=% X", e.Index, e.SubIndex, e.Data) }

// pdoKey identifies a PDO of a node.
type pdoKey struct {
	transmit bool
	n        int
}

// pdoOf returns the PDO whose communication or mapping parameter an index
// is, and whether it is the mapping.
func pdoOf(index uint16) (k pdoKey, mapping, ok bool) {
	switch {
	case index >= od.IndexRPDOComm && index < od.IndexRPDOMapping:
		return pdoKey{false, int(index-od.IndexRPDOComm) + 1}, false, true
	case index >= od.IndexRPDOMapping && index < od.IndexTPDOComm:
		return pdoKey{false, int(index-od.IndexRPDOMapping) + 1}, true, true
	case index >= od.IndexTPDOComm && index < od.IndexTPDOMapping:
		return pdoKey{true, int(index-od.IndexTPDOComm) + 1}, false, true
	case index >= od.IndexTPDOMapping && index < od.IndexTPDOMapping+od.MaxPDOs:
		return pdoKey{true, int(index-od.IndexTPDOMapping) + 1}, true, true
	}
	return pdoKey{}, false, false
}

// Configure writes a configuration to a node. Entries outside the PDO
// parameters (0x1400..0x1BFF) are written first, in the given order.
// Entries of PDO parameters are grouped per PDO, applied over the PDO's
// current configuration (read from the node) and written with
// pdo.Configure, in the order the PDOs first appear: so the sequence
// CiA 301 requires (invalidate, clear the mapping, map, enable, make
// valid) is kept however the entries are ordered. The first failure stops
// the configuration; the error names the entry or the PDO.
func (m *Master) Configure(ctx context.Context, node canopen.NodeID, entries []Entry) error {
	c, err := m.SDO(node)
	if err != nil {
		return err
	}
	var order []pdoKey
	groups := map[pdoKey][]Entry{}
	for _, e := range entries {
		if k, _, ok := pdoOf(e.Index); ok {
			if _, seen := groups[k]; !seen {
				order = append(order, k)
			}
			groups[k] = append(groups[k], e)
			continue
		}
		if err := c.Download(ctx, e.Index, e.SubIndex, e.Data); err != nil {
			return fmt.Errorf("master: node %d: write %04X:%02X: %w", node, e.Index, e.SubIndex, err)
		}
	}
	for _, k := range order {
		base, err := pdo.ReadConfig(ctx, client{c}, k.transmit, k.n)
		if err != nil {
			return fmt.Errorf("master: node %d: %s: %w", node, pdoName(k), err)
		}
		cfg, err := Overlay(base, groups[k])
		if err != nil {
			return fmt.Errorf("master: node %d: %s: %w", node, pdoName(k), err)
		}
		if err := pdo.Configure(ctx, client{c}, k.transmit, k.n, cfg); err != nil {
			return fmt.Errorf("master: node %d: %w", node, err)
		}
	}
	return nil
}

func pdoName(k pdoKey) string {
	if k.transmit {
		return fmt.Sprintf("TPDO %d", k.n)
	}
	return fmt.Sprintf("RPDO %d", k.n)
}

// Overlay applies entries of one PDO's communication and mapping
// parameters to a configuration: sub-indices 1 (COB-ID), 2 (transmission
// type), 3 (inhibit time), 5 (event timer) and 6 (SYNC start value) of
// the communication parameter; sub-index 0 (number of entries) and 1..64
// (entries) of the mapping. Entries of other objects, and sub-indices 0
// and 4 of the communication parameter, are ignored.
func Overlay(base pdo.Config, entries []Entry) (pdo.Config, error) {
	cfg := base
	var raw [pdo.MaxEntries]uint32
	for i, e := range base.Mapping {
		raw[i] = e.Uint32()
	}
	count := len(base.Mapping)
	for _, e := range entries {
		_, mapping, ok := pdoOf(e.Index)
		if !ok {
			continue
		}
		x := canopen.DecodeUint(e.Data)
		if mapping {
			switch {
			case e.SubIndex == 0:
				count = int(x)
			case int(e.SubIndex) <= pdo.MaxEntries:
				raw[e.SubIndex-1] = uint32(x)
			default:
				return cfg, fmt.Errorf("mapping entry %d: %w", e.SubIndex, canopen.AbortNoSubIndex)
			}
			continue
		}
		switch e.SubIndex {
		case 1:
			cfg.COBID = canopen.COBID(x)
		case 2:
			cfg.TransmissionType = pdo.TransmissionType(x)
		case 3:
			cfg.InhibitTime = uint16(x)
		case 5:
			cfg.EventTimer = uint16(x)
		case 6:
			cfg.SyncStart = uint8(x)
		}
	}
	if count > pdo.MaxEntries {
		return cfg, fmt.Errorf("%d mapping entries: %w", count, canopen.AbortPDOLength)
	}
	cfg.Mapping = make(pdo.Mapping, count)
	for i := range cfg.Mapping {
		if raw[i] == 0 {
			return cfg, fmt.Errorf("mapping entry %d is empty", i+1)
		}
		cfg.Mapping[i] = pdo.ParseEntry(raw[i])
	}
	return cfg, nil
}

// ConfigurePDO writes the configuration of TPDO (transmit) or RPDO n of a
// node with pdo.Configure.
func (m *Master) ConfigurePDO(ctx context.Context, node canopen.NodeID, transmit bool, n int, cfg pdo.Config) error {
	if err := node.Check(); err != nil {
		return err
	}
	return pdo.Configure(ctx, sdoFuncs{m, node}, transmit, n, cfg)
}

// ReadPDO reads the configuration of TPDO (transmit) or RPDO n of a node.
func (m *Master) ReadPDO(ctx context.Context, node canopen.NodeID, transmit bool, n int) (pdo.Config, error) {
	if err := node.Check(); err != nil {
		return pdo.Config{}, err
	}
	return pdo.ReadConfig(ctx, sdoFuncs{m, node}, transmit, n)
}
