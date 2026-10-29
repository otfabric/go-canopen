// SPDX-License-Identifier: MIT

package pdo

import (
	"errors"
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// TransmissionType is the transmission type of a PDO (sub-index 02h of
// the communication parameter, CiA 301 7.5.2.35 and 7.5.2.37).
type TransmissionType uint8

// Transmission types. 1..240 are cyclic synchronous: a TPDO is sent on
// every nth SYNC, an RPDO is applied on the next SYNC. 241..251 are
// reserved.
const (
	// TransSyncAcyclic (0): a TPDO is sent on the SYNC after an event; an
	// RPDO is applied on the next SYNC.
	TransSyncAcyclic TransmissionType = 0
	// TransSyncMax (240) is the largest cyclic synchronous type.
	TransSyncMax TransmissionType = 240
	// TransRTRSync (252, TPDO only): the data are sampled on SYNC and sent
	// on a remote transmission request.
	TransRTRSync TransmissionType = 252
	// TransRTR (253, TPDO only): the data are sampled and sent on a remote
	// transmission request.
	TransRTR TransmissionType = 253
	// TransEventManufacturer (254): event-driven, the event being
	// manufacturer-specific.
	TransEventManufacturer TransmissionType = 254
	// TransEventProfile (255): event-driven, the event being defined by
	// the device or application profile.
	TransEventProfile TransmissionType = 255
)

// Synchronous reports whether the type is 0..240.
func (t TransmissionType) Synchronous() bool { return t <= TransSyncMax }

// EventDriven reports whether the type is 254 or 255.
func (t TransmissionType) EventDriven() bool { return t >= TransEventManufacturer }

// ValidFor reports whether a TPDO (transmit) or an RPDO may have the
// type: 241..251 are reserved, and 252 and 253 are for TPDOs only.
func (t TransmissionType) ValidFor(transmit bool) bool {
	switch {
	case t <= TransSyncMax, t.EventDriven():
		return true
	case t == TransRTRSync, t == TransRTR:
		return transmit
	}
	return false
}

// String describes the type.
func (t TransmissionType) String() string {
	switch {
	case t == TransSyncAcyclic:
		return "synchronous acyclic (0)"
	case t <= TransSyncMax:
		return fmt.Sprintf("synchronous cyclic (%d)", uint8(t))
	case t == TransRTRSync:
		return "RTR-only synchronous (252)"
	case t == TransRTR:
		return "RTR-only event-driven (253)"
	case t == TransEventManufacturer:
		return "event-driven manufacturer-specific (254)"
	case t == TransEventProfile:
		return "event-driven profile-specific (255)"
	}
	return fmt.Sprintf("reserved (%d)", uint8(t))
}

// Config is the configuration of a PDO: its communication parameter
// (0x1400.. for RPDOs, 0x1800.. for TPDOs) and its mapping (0x1600..,
// 0x1A00..).
type Config struct {
	// COBID is sub-index 01h: the CAN identifier, bit 31 invalid, bit 30
	// no RTR allowed (TPDO), bit 29 29-bit identifier.
	COBID canopen.COBID
	// TransmissionType is sub-index 02h.
	TransmissionType TransmissionType
	// InhibitTime is sub-index 03h, in units of 100 µs (TPDO).
	InhibitTime uint16
	// EventTimer is sub-index 05h in ms: the TPDO event timer, or the
	// RPDO deadline (0 disables it).
	EventTimer uint16
	// SyncStart is sub-index 06h of a TPDO: the SYNC counter value of the
	// first transmission of a cyclic synchronous TPDO (0 disables it).
	SyncStart uint8
	// Mapping lists the mapped objects (sub-index 00h is their number).
	Mapping Mapping
}

// Active reports whether a PDO with this configuration exists on the bus:
// the COB-ID is valid and at least one object is mapped. (A valid COB-ID
// with an empty mapping is how CiA 301 default configurations leave
// unused PDOs; like CANopenNode, the PDO then sends and receives nothing.)
func (c Config) Active() bool { return c.COBID.Valid() && len(c.Mapping) > 0 }

// ErrNumber reports a PDO number outside 1..512.
var ErrNumber = errors.New("pdo: PDO number out of range 1..512")

// checkNumber checks a PDO number.
func checkNumber(n int) error {
	if n < 1 || n > od.MaxPDOs {
		return fmt.Errorf("%w: %d", ErrNumber, n)
	}
	return nil
}

// CommIndex returns the index of the communication parameter of TPDO
// (transmit) or RPDO n (1..512).
func CommIndex(transmit bool, n int) uint16 {
	if transmit {
		return od.IndexTPDOComm + uint16(n-1)
	}
	return od.IndexRPDOComm + uint16(n-1)
}

// MappingIndex returns the index of the mapping parameter of TPDO
// (transmit) or RPDO n (1..512).
func MappingIndex(transmit bool, n int) uint16 {
	if transmit {
		return od.IndexTPDOMapping + uint16(n-1)
	}
	return od.IndexRPDOMapping + uint16(n-1)
}

// kind names a PDO for messages.
func kind(transmit bool, n int) string {
	if transmit {
		return fmt.Sprintf("TPDO%d", n)
	}
	return fmt.Sprintf("RPDO%d", n)
}

// Restricted reports whether an 11-bit CAN identifier is one CiA 301
// 7.3.5 reserves (NMT, SYNC/EMCY of node 0..., default SDO, LSS, NMT
// error control), which a PDO cannot use.
func Restricted(id canbus.ID) bool {
	return id <= 0x7F ||
		id >= 0x101 && id <= 0x180 ||
		id >= 0x581 && id <= 0x5FF ||
		id >= 0x601 && id <= 0x67F ||
		id >= 0x6E0 && id <= 0x6FF ||
		id >= 0x701 && id <= 0x77F ||
		id >= 0x780 && id <= 0x7FF
}

// checkCOBID checks the form of a PDO COB-ID: an 11-bit identifier must
// not have bits 11..28 set, and a valid COB-ID must not use a restricted
// identifier.
func checkCOBID(c canopen.COBID) error {
	if !c.Extended() && c&0x1FFFF800 != 0 {
		return canopen.AbortInvalidValue
	}
	if c.Valid() && !c.Extended() && Restricted(c.ID()) {
		return canopen.AbortInvalidValue
	}
	return nil
}

// Load reads the configuration of TPDO (transmit) or RPDO n from a
// dictionary with local reads (no access checks or hooks). Optional
// sub-indices (inhibit time, event timer, SYNC start value) that the
// dictionary lacks read as 0. It does not validate the mapping.
func Load(d *od.Dictionary, transmit bool, n int) (Config, error) {
	if err := checkNumber(n); err != nil {
		return Config{}, err
	}
	return readConfig(func(index uint16, sub uint8) ([]byte, error) { return d.Get(index, sub) }, transmit, n)
}

// readConfig reads a configuration through a read function.
func readConfig(read func(index uint16, sub uint8) ([]byte, error), transmit bool, n int) (Config, error) {
	ci, mi := CommIndex(transmit, n), MappingIndex(transmit, n)
	var c Config
	get := func(index uint16, sub uint8, optional bool) (uint64, error) {
		b, err := read(index, sub)
		if err != nil {
			if optional && isMissing(err) {
				return 0, nil
			}
			return 0, fmt.Errorf("pdo: %s: read 0x%04X:%02X: %w", kind(transmit, n), index, sub, err)
		}
		return canopen.DecodeUint(b[:min(len(b), 8)]), nil
	}
	x, err := get(ci, 1, false)
	if err != nil {
		return c, err
	}
	c.COBID = canopen.COBID(x)
	if x, err = get(ci, 2, false); err != nil {
		return c, err
	}
	c.TransmissionType = TransmissionType(x)
	if x, err = get(ci, 3, true); err != nil {
		return c, err
	}
	c.InhibitTime = uint16(x)
	if x, err = get(ci, 5, true); err != nil {
		return c, err
	}
	c.EventTimer = uint16(x)
	if transmit {
		if x, err = get(ci, 6, true); err != nil {
			return c, err
		}
		c.SyncStart = uint8(x)
	}
	if x, err = get(mi, 0, false); err != nil {
		return c, err
	}
	if x > MaxEntries {
		return c, fmt.Errorf("pdo: %s: %d mapping entries: %w", kind(transmit, n), x, canopen.AbortPDOLength)
	}
	c.Mapping = make(Mapping, 0, x)
	for i := 1; i <= int(x); i++ {
		e, err := get(mi, uint8(i), false)
		if err != nil {
			return c, err
		}
		c.Mapping = append(c.Mapping, ParseEntry(uint32(e)))
	}
	return c, nil
}

// isMissing reports an error saying that an object or sub-object does not
// exist.
func isMissing(err error) bool {
	var a canopen.AbortCode
	return errors.As(err, &a) && (a == canopen.AbortNoObject || a == canopen.AbortNoSubIndex)
}
