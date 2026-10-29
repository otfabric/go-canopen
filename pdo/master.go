// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-canopen"
)

// SDO is the access to a remote node's dictionary that [Configure] and
// [ReadConfig] need. The sdo package's client takes extra call options,
// so it needs an adapter (as cia402.SDOFuncs, whose method set is this
// interface's).
type SDO interface {
	Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error)
	Download(ctx context.Context, index uint16, sub uint8, data []byte) error
}

// ReadConfig reads the configuration of TPDO (transmit) or RPDO n of a
// remote node. Inhibit time, event timer and SYNC start value read as 0
// when the node does not have them.
func ReadConfig(ctx context.Context, sdo SDO, transmit bool, n int) (Config, error) {
	if err := checkNumber(n); err != nil {
		return Config{}, err
	}
	return readConfig(func(index uint16, sub uint8) ([]byte, error) { return sdo.Upload(ctx, index, sub) }, transmit, n)
}

// Configure writes the configuration of TPDO (transmit) or RPDO n of a
// remote node with the sequence of CiA 301 7.5.2.36/38:
//
//  1. invalidate: COB-ID with bit 31 set (and the new identifier)
//  2. disable the mapping: sub 00h of the mapping = 0
//  3. write the mapping entries
//  4. enable the mapping: sub 00h = number of entries (or cfg.MPDO, FEh
//     or FFh, for a multiplexed PDO without entries)
//  5. transmission type, inhibit time (TPDO), event timer, SYNC start
//     value (TPDO)
//  6. if cfg.COBID is valid: the COB-ID, which makes the PDO valid
//
// Inhibit time, event timer and SYNC start value are skipped when they
// are 0 and the node lacks them. The first failure stops the sequence;
// the error names the object and wraps the SDO error (an
// [canopen.AbortCode] for a refused write).
func Configure(ctx context.Context, sdo SDO, transmit bool, n int, cfg Config) error {
	if err := checkNumber(n); err != nil {
		return err
	}
	if len(cfg.Mapping) > MaxEntries {
		return fmt.Errorf("pdo: %s: %d mapping entries: %w", kind(transmit, n), len(cfg.Mapping), canopen.AbortPDOLength)
	}
	ci, mi := CommIndex(transmit, n), MappingIndex(transmit, n)
	w := func(index uint16, sub uint8, data []byte, optional bool) error {
		err := sdo.Download(ctx, index, sub, data)
		if err != nil && optional && isMissing(err) && canopen.DecodeUint(data) == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pdo: configure %s: write 0x%04X:%02X: %w", kind(transmit, n), index, sub, err)
		}
		return nil
	}
	u8 := func(x uint8) []byte { return []byte{x} }
	u16 := func(x uint16) []byte { return binary.LittleEndian.AppendUint16(nil, x) }
	u32 := func(x uint32) []byte { return binary.LittleEndian.AppendUint32(nil, x) }
	if err := w(ci, 1, u32(uint32(cfg.COBID|canopen.COBInvalid)), false); err != nil {
		return err
	}
	if err := w(mi, 0, u8(0), false); err != nil {
		return err
	}
	for i, e := range cfg.Mapping {
		if err := w(mi, uint8(i+1), u32(e.Uint32()), false); err != nil {
			return err
		}
	}
	switch {
	case len(cfg.Mapping) > 0:
		if err := w(mi, 0, u8(uint8(len(cfg.Mapping))), false); err != nil {
			return err
		}
	case cfg.MPDO != 0:
		if err := w(mi, 0, u8(cfg.MPDO), false); err != nil {
			return err
		}
	}
	if err := w(ci, 2, u8(uint8(cfg.TransmissionType)), false); err != nil {
		return err
	}
	if transmit {
		if err := w(ci, 3, u16(cfg.InhibitTime), true); err != nil {
			return err
		}
	}
	if err := w(ci, 5, u16(cfg.EventTimer), true); err != nil {
		return err
	}
	if transmit {
		if err := w(ci, 6, u8(cfg.SyncStart), true); err != nil {
			return err
		}
	}
	if cfg.COBID.Valid() {
		return w(ci, 1, u32(uint32(cfg.COBID)), false)
	}
	return nil
}
