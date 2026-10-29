// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Entry is one value to write into a node's dictionary: the neutral form
// of a node configuration (from a DCF, a concise DCF, a tool, or code).
// It is the entry of a concise DCF (package cdcf).
type Entry = cdcf.Entry

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
// valid) is kept however the entries are ordered. An EMCY (0x1014), SYNC
// (0x1005) or TIME (0x1012) COB-ID that changes the CAN-ID of an object in
// use is preceded by a write of the current COB-ID without its in-use bits,
// since devices refuse that change (CiA 301). The first failure stops
// the configuration; the error names the entry or the PDO.
func (m *Master) Configure(ctx context.Context, node canopen.NodeID, entries []Entry) error {
	c, err := m.remote(node)
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
		if err := releaseCOBID(ctx, c, e); err != nil {
			return fmt.Errorf("master: node %d: release %04X:%02X: %w", node, e.Index, e.SubIndex, err)
		}
		if err := c.Download(ctx, e.Index, e.SubIndex, e.Data); err != nil {
			return fmt.Errorf("master: node %d: write %04X:%02X: %w", node, e.Index, e.SubIndex, err)
		}
	}
	for _, k := range order {
		base, err := pdo.ReadConfig(ctx, c, k.transmit, k.n)
		if err != nil {
			return fmt.Errorf("master: node %d: %s: %w", node, pdoName(k), err)
		}
		cfg, err := Overlay(base, groups[k])
		if err != nil {
			return fmt.Errorf("master: node %d: %s: %w", node, pdoName(k), err)
		}
		if err := pdo.Configure(ctx, c, k.transmit, k.n, cfg); err != nil {
			return fmt.Errorf("master: node %d: %w", node, err)
		}
	}
	return nil
}

// cobidInUse returns the bits of a COB-ID object (EMCY 0x1014, SYNC
// 0x1005, TIME 0x1012) under which a device refuses a change of its CAN-ID
// (CiA 301), and whether they are set: for EMCY a valid COB-ID (bit 31
// clear), for SYNC bit 30 (generate), for TIME bits 31 and 30 (consume,
// produce). ok is false for other objects.
func cobidInUse(index uint16, cob canopen.COBID) (inUse, ok bool) {
	switch index {
	case od.IndexCOBIDEmcy:
		return cob.Valid(), true
	case od.IndexCOBIDSync:
		return cob&canopen.COBGenerate != 0, true
	case od.IndexCOBIDTime:
		return cob&(canopen.COBGenerate|canopen.COBConsume) != 0, true
	}
	return false, false
}

// released returns the COB-ID with its in-use bits changed so that the
// CAN-ID may change: EMCY invalid, SYNC not generated, TIME neither
// consumed nor produced.
func released(index uint16, cob canopen.COBID) canopen.COBID {
	switch index {
	case od.IndexCOBIDEmcy:
		return cob | canopen.COBInvalid
	case od.IndexCOBIDSync:
		return cob &^ canopen.COBGenerate
	}
	return cob &^ (canopen.COBGenerate | canopen.COBConsume)
}

// releaseCOBID prepares the write of an EMCY, SYNC or TIME COB-ID that
// changes the CAN-ID of one in use: a device refuses that change (CiA
// 301: the CAN-ID may only change while the object is not in use), so
// the current COB-ID is first written back without its in-use bits, and
// the entry, written next, sets the new CAN-ID and bits together. Other
// entries need nothing. When the current value cannot be read, nothing
// is done and the entry's own write reports the device's answer.
func releaseCOBID(ctx context.Context, c remote, e Entry) error {
	if e.SubIndex != 0 || len(e.Data) != 4 {
		return nil
	}
	nw := canopen.COBID(canopen.DecodeUint(e.Data))
	if _, ok := cobidInUse(e.Index, nw); !ok {
		return nil
	}
	b, err := c.Upload(ctx, e.Index, 0)
	if err != nil || len(b) != 4 {
		return nil //nolint:nilerr // the entry's write reports the device's answer
	}
	cur := canopen.COBID(canopen.DecodeUint(b))
	curUse, _ := cobidInUse(e.Index, cur)
	nwUse, _ := cobidInUse(e.Index, nw)
	if !curUse || !nwUse || (cur.ID() == nw.ID() && cur.Extended() == nw.Extended()) {
		return nil
	}
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], uint32(released(e.Index, cur)))
	return c.Download(ctx, e.Index, 0, data[:])
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
