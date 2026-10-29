// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/mpdo"
	"github.com/otfabric/go-canopen/pdo"
)

// ConfigureMPDO makes TPDO (transmit) or RPDO n of a node a multiplexed PDO
// (CiA 301, package mpdo): mode mpdo.MappingSAM (0xFE) or mpdo.MappingDAM
// (0xFF), on COB-ID cob, event-driven (transmission type 254), with the
// inhibit time inhibit (100 µs, TPDO). The PDO is written with the
// sequence of pdo.Configure; the node (package node) creates its MPDO
// producer or consumer when the mapping becomes an MPDO.
func (m *Master) ConfigureMPDO(ctx context.Context, node canopen.NodeID, transmit bool, n int, mode uint8, cob canopen.COBID, inhibit uint16) error {
	if mode != mpdo.MappingSAM && mode != mpdo.MappingDAM {
		return fmt.Errorf("master: MPDO mode 0x%02X: %w", mode, canopen.AbortInvalidValue)
	}
	cfg := pdo.Config{COBID: cob, TransmissionType: pdo.TransEventManufacturer, InhibitTime: inhibit, MPDO: mode}
	return m.ConfigurePDO(ctx, node, transmit, n, cfg)
}

// WriteScannerList writes object scanner list number list (1..48,
// 0x1FA0+list-1) of a node: entries to sub-indices 1.., then the number
// of entries to sub-index 00h (skipped when the node's list has a
// constant size, abort "read only").
func (m *Master) WriteScannerList(ctx context.Context, node canopen.NodeID, list int, entries []mpdo.ScannerEntry) error {
	raw := make([][]byte, len(entries))
	for i, e := range entries {
		raw[i] = binary.LittleEndian.AppendUint32(nil, e.Uint32())
	}
	return m.writeList(ctx, node, mpdo.IndexScanner, list, raw)
}

// WriteDispatcherList writes object dispatcher list number list (1..48,
// 0x1FD0+list-1) of a node as [Master.WriteScannerList] does.
func (m *Master) WriteDispatcherList(ctx context.Context, node canopen.NodeID, list int, entries []mpdo.DispatcherEntry) error {
	raw := make([][]byte, len(entries))
	for i, e := range entries {
		raw[i] = binary.LittleEndian.AppendUint64(nil, e.Uint64())
	}
	return m.writeList(ctx, node, mpdo.IndexDispatcher, list, raw)
}

func (m *Master) writeList(ctx context.Context, node canopen.NodeID, first uint16, list int, raw [][]byte) error {
	if list < 1 || list > mpdo.MaxLists || len(raw) > mpdo.MaxListEntries {
		return fmt.Errorf("master: MPDO list %d with %d entries: %w", list, len(raw), canopen.AbortInvalidValue)
	}
	c, err := m.remote(node)
	if err != nil {
		return err
	}
	index := first + uint16(list-1)
	for i, b := range raw {
		if err := c.Download(ctx, index, uint8(i+1), b); err != nil {
			return fmt.Errorf("master: node %d: write %04X:%02X: %w", node, index, i+1, err)
		}
	}
	err = c.Download(ctx, index, 0, []byte{uint8(len(raw))})
	if errors.Is(err, canopen.AbortReadOnly) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("master: node %d: write %04X:00: %w", node, index, err)
	}
	return nil
}
