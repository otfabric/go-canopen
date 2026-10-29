// SPDX-License-Identifier: MIT

package mpdo

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Values of sub-index 00h of a PDO mapping parameter that make the PDO an
// MPDO (Lely CO_PDO_MAP_SAM_MPDO, CO_PDO_MAP_DAM_MPDO).
const (
	MappingSAM uint8 = 0xFE // source address mode
	MappingDAM uint8 = 0xFF // destination address mode
)

// Mode is the address mode of an MPDO.
type Mode uint8

// Address modes.
const (
	ModeNone Mode = iota // not an MPDO
	ModeSAM              // source address mode (mapping sub 00h 0xFE)
	ModeDAM              // destination address mode (mapping sub 00h 0xFF)
)

// String names the mode.
func (m Mode) String() string {
	switch m {
	case ModeSAM:
		return "SAM"
	case ModeDAM:
		return "DAM"
	case ModeNone:
		return "none"
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// modeOf maps a mapping sub-index 00h value to a mode.
func modeOf(n uint64) Mode {
	switch n {
	case uint64(MappingSAM):
		return ModeSAM
	case uint64(MappingDAM):
		return ModeDAM
	}
	return ModeNone
}

// ModeOf returns the MPDO mode of TPDO (transmit) or RPDO n of d from the
// sub-index 00h of its mapping parameter (local read); ModeNone when the
// PDO is not an MPDO, n is out of range or the object is missing.
func ModeOf(d *od.Dictionary, transmit bool, n int) Mode {
	if n < 1 || n > od.MaxPDOs {
		return ModeNone
	}
	x, err := d.GetUint(pdo.MappingIndex(transmit, n), 0)
	if err != nil {
		return ModeNone
	}
	return modeOf(x)
}

// Numbers returns the numbers of the TPDOs and RPDOs of d whose
// communication and mapping parameters exist and whose mapping sub-index
// 00h is 0xFE or 0xFF: those a node runs as a [Producer] or a [Consumer].
func Numbers(d *od.Dictionary) (tpdos, rpdos []int) {
	for n := 1; n <= od.MaxPDOs; n++ {
		for _, transmit := range []bool{true, false} {
			if d.Object(pdo.CommIndex(transmit, n)) == nil || ModeOf(d, transmit, n) == ModeNone {
				continue
			}
			if transmit {
				tpdos = append(tpdos, n)
			} else {
				rpdos = append(rpdos, n)
			}
		}
	}
	return tpdos, rpdos
}

// ErrNumber reports a PDO number outside 1..512 or a dictionary without
// the PDO's parameter objects.
var ErrNumber = errors.New("mpdo: no such PDO")

func checkPDO(d *od.Dictionary, transmit bool, n int) error {
	if n < 1 || n > od.MaxPDOs {
		return fmt.Errorf("%w: number %d out of range 1..%d", ErrNumber, n, od.MaxPDOs)
	}
	ci, mi := pdo.CommIndex(transmit, n), pdo.MappingIndex(transmit, n)
	if d.Object(ci) == nil || d.Object(mi) == nil {
		return fmt.Errorf("%w: dictionary lacks 0x%04X or 0x%04X", ErrNumber, ci, mi)
	}
	return nil
}

// config is what a service reads from its PDO parameters.
type config struct {
	cob     canopen.COBID
	tt      pdo.TransmissionType
	inhibit time.Duration
	mode    Mode
}

// active reports whether the PDO is a usable MPDO.
func (c config) active() bool {
	return c.cob.Valid() && c.mode != ModeNone && c.tt.EventDriven()
}

// load reads the configuration with local reads; a missing inhibit time
// reads as 0.
func load(d *od.Dictionary, transmit bool, n int) config {
	ci := pdo.CommIndex(transmit, n)
	var c config
	if x, err := d.GetUint(ci, 1); err == nil {
		c.cob = canopen.COBID(x)
	} else {
		c.cob = canopen.COBInvalid
	}
	if x, err := d.GetUint(ci, 2); err == nil {
		c.tt = pdo.TransmissionType(x)
	}
	if x, err := d.GetUint(ci, 3); err == nil && transmit {
		c.inhibit = time.Duration(x) * 100 * time.Microsecond
	}
	c.mode = ModeOf(d, transmit, n)
	return c
}

// ProtectPDO installs write hooks on TPDO (transmit) or RPDO n of d with
// the MPDO rules, which complement those of pdo.Protect:
//
//   - mapping sub-index 00h: setting 0xFE or 0xFF while the COB-ID is
//     valid and the value differs, or while the transmission type is not
//     event-driven: [canopen.AbortUnsupported] and
//     [canopen.AbortInvalidValue] respectively; changing it away from
//     0xFE/0xFF while the COB-ID is valid: [canopen.AbortUnsupported].
//   - mapping sub-indices 01h..: any write while sub 00h is 0xFE or 0xFF:
//     [canopen.AbortUnsupported] (Lely: entries change only while sub 00h
//     is 0).
//   - communication sub-index 02h: a type other than 254/255 while the PDO
//     is an MPDO: [canopen.AbortInvalidValue] (MPDOs are event-driven
//     only).
//
// Note that pdo.Protect itself (v0.2.0) refuses 0xFE/0xFF in sub 00h with
// [canopen.AbortPDOLength]; where both are installed, a PDO can only be
// made an MPDO by a local write until pdo accepts the two values. The
// returned function removes the hooks (later calls do nothing).
func ProtectPDO(d *od.Dictionary, transmit bool, n int) (unprotect func(), err error) {
	if err := checkPDO(d, transmit, n); err != nil {
		return nil, err
	}
	ci, mi := pdo.CommIndex(transmit, n), pdo.MappingIndex(transmit, n)
	valid := func() bool {
		x, err := d.GetUint(ci, 1)
		return err == nil && canopen.COBID(x).Valid()
	}
	rm1 := d.OnWrite(mi, -1, func(v *od.Variable, data []byte) error {
		cur := ModeOf(d, transmit, n)
		if v.SubIndex != 0 {
			if cur != ModeNone {
				return canopen.AbortUnsupported
			}
			return nil
		}
		x := canopen.DecodeUint(data)
		next := modeOf(x)
		switch {
		case next == ModeNone && cur == ModeNone:
			return nil
		case next != cur && valid():
			return canopen.AbortUnsupported
		case next != ModeNone && !load(d, transmit, n).tt.EventDriven():
			return canopen.AbortInvalidValue
		}
		return nil
	})
	rm2 := d.OnWrite(ci, 2, func(_ *od.Variable, data []byte) error {
		if ModeOf(d, transmit, n) != ModeNone && !pdo.TransmissionType(canopen.DecodeUint(data)).EventDriven() {
			return canopen.AbortInvalidValue
		}
		return nil
	})
	var once sync.Once
	return func() { once.Do(func() { rm1(); rm2() }) }, nil
}
