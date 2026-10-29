// SPDX-License-Identifier: MIT

package pdo

import (
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Protect installs write hooks on the communication and mapping
// parameters of TPDO (transmit) or RPDO n of a dictionary, so that remote
// writes (from an SDO server) follow the reconfiguration rules of CiA 301
// 7.5.2.35..38 as CANopenNode enforces them. "Active" below is
// [Config.Active]: COB-ID valid and at least one mapped object.
//
// Communication parameter:
//   - sub 01h (COB-ID): bits 11..28 of an 11-bit COB-ID set, a valid
//     COB-ID with a restricted identifier ([Restricted]), changing the
//     identifier (or its format) of an active PDO, or making the COB-ID
//     valid while nothing is mapped: [canopen.AbortInvalidValue].
//   - sub 02h (transmission type): reserved types (241..251, and 252/253
//     for an RPDO): [canopen.AbortInvalidValue].
//   - sub 03h (inhibit time) and 06h (SYNC start value) of a TPDO while
//     it is active, and a SYNC start value above 240:
//     [canopen.AbortInvalidValue].
//
// Mapping parameter:
//   - any write while the PDO is active, and a write of an entry (sub ≥ 1)
//     while sub 00h is not 0: [canopen.AbortUnsupported] (CANopenNode's
//     ODR_UNSUPP_ACCESS).
//   - an entry other than 0 that [ValidateEntry] refuses: its code
//     ([canopen.AbortNoObject] or [canopen.AbortNotMappable]).
//   - sub 00h above the number of entries or [MaxEntries], or entries
//     longer than [MaxBits] in total: [canopen.AbortPDOLength]; an entry
//     1..n that is invalid: its [ValidateEntry] code.
//
// Local writes ([od.Dictionary.Set]) are not checked. The hooks cannot be
// removed; installing them twice is harmless. [NewTPDO] and [NewRPDO]
// call Protect.
func Protect(d *od.Dictionary, transmit bool, n int) error {
	if err := checkNumber(n); err != nil {
		return err
	}
	ci, mi := CommIndex(transmit, n), MappingIndex(transmit, n)
	if d.Object(ci) == nil || d.Object(mi) == nil {
		return fmt.Errorf("pdo: %s: dictionary lacks 0x%04X or 0x%04X: %w", kind(transmit, n), ci, mi, canopen.AbortNoObject)
	}
	d.OnWrite(ci, -1, func(v *od.Variable, data []byte) error {
		return checkCommWrite(d, transmit, n, v.SubIndex, canopen.DecodeUint(data))
	})
	d.OnWrite(mi, -1, func(v *od.Variable, data []byte) error {
		return checkMappingWrite(d, transmit, n, v.SubIndex, canopen.DecodeUint(data))
	})
	return nil
}

// active reports whether the PDO is active in the dictionary now.
func active(d *od.Dictionary, transmit bool, n int) bool {
	cob, err1 := d.GetUint(CommIndex(transmit, n), 1)
	cnt, err2 := d.GetUint(MappingIndex(transmit, n), 0)
	return err1 == nil && err2 == nil && canopen.COBID(cob).Valid() && cnt > 0
}

// checkCommWrite applies the communication parameter rules.
func checkCommWrite(d *od.Dictionary, transmit bool, n int, sub uint8, x uint64) error {
	switch sub {
	case 1:
		c := canopen.COBID(x)
		if err := checkCOBID(c); err != nil {
			return err
		}
		if !c.Valid() {
			return nil
		}
		cnt, _ := d.GetUint(MappingIndex(transmit, n), 0)
		if cnt == 0 {
			return canopen.AbortInvalidValue
		}
		cur, _ := d.GetUint(CommIndex(transmit, n), 1)
		old := canopen.COBID(cur)
		if old.Valid() && (old.ID() != c.ID() || old.Extended() != c.Extended()) {
			return canopen.AbortInvalidValue
		}
	case 2:
		if !TransmissionType(x).ValidFor(transmit) {
			return canopen.AbortInvalidValue
		}
	case 3:
		if transmit && active(d, transmit, n) {
			return canopen.AbortInvalidValue
		}
	case 6:
		if transmit && (x > uint64(TransSyncMax) || active(d, transmit, n)) {
			return canopen.AbortInvalidValue
		}
	}
	return nil
}

// checkMappingWrite applies the mapping parameter rules.
func checkMappingWrite(d *od.Dictionary, transmit bool, n int, sub uint8, x uint64) error {
	if active(d, transmit, n) {
		return canopen.AbortUnsupported
	}
	mi := MappingIndex(transmit, n)
	if sub > 0 {
		if cnt, _ := d.GetUint(mi, 0); cnt != 0 {
			return canopen.AbortUnsupported
		}
		if x == 0 {
			return nil
		}
		return ValidateEntry(d, ParseEntry(uint32(x)), transmit)
	}
	o := d.Object(mi)
	if x == 0 {
		return nil
	}
	if x > MaxEntries || o.Sub(uint8(x)) == nil {
		return canopen.AbortPDOLength
	}
	m := make(Mapping, 0, x)
	for i := 1; i <= int(x); i++ {
		e, err := d.GetUint(mi, uint8(i))
		if err != nil {
			return canopen.AbortPDOLength
		}
		if e == 0 {
			return canopen.AbortNotMappable
		}
		m = append(m, ParseEntry(uint32(e)))
	}
	return m.Validate(d, transmit)
}
