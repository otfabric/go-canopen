// SPDX-License-Identifier: MIT

package canopen

import (
	"fmt"

	"github.com/otfabric/go-canbus"
)

// NodeID is a CANopen node-ID: 1..127. 0 addresses all nodes in NMT
// commands and is not a valid node-ID otherwise.
type NodeID uint8

// Node-ID limits.
const (
	BroadcastNodeID NodeID = 0   // all nodes, in NMT commands only
	MinNodeID       NodeID = 1   // lowest valid node-ID
	MaxNodeID       NodeID = 127 // highest valid node-ID
)

// Valid reports whether id is a node-ID a device can have (1..127).
func (id NodeID) Valid() bool { return id >= MinNodeID && id <= MaxNodeID }

// Check returns ErrInvalidNodeID unless id is valid.
func (id NodeID) Check() error {
	if !id.Valid() {
		return fmt.Errorf("%w: %d", ErrInvalidNodeID, id)
	}
	return nil
}

// Function codes of the predefined connection set (CiA 301 7.3.5): the
// CAN identifier of a node's default COB is the function code plus the
// node-ID.
const (
	FuncNMT       canbus.ID = 0x000 // NMT module control (no node-ID)
	FuncSYNC      canbus.ID = 0x080 // SYNC (no node-ID)
	FuncEMCY      canbus.ID = 0x080 // EMCY, + node-ID
	FuncTIME      canbus.ID = 0x100 // TIME stamp (no node-ID)
	FuncTPDO1     canbus.ID = 0x180
	FuncRPDO1     canbus.ID = 0x200
	FuncTPDO2     canbus.ID = 0x280
	FuncRPDO2     canbus.ID = 0x300
	FuncTPDO3     canbus.ID = 0x380
	FuncRPDO3     canbus.ID = 0x400
	FuncTPDO4     canbus.ID = 0x480
	FuncRPDO4     canbus.ID = 0x500
	FuncSDOTx     canbus.ID = 0x580 // SDO server to client, + node-ID
	FuncSDORx     canbus.ID = 0x600 // SDO client to server, + node-ID
	FuncHeartbeat canbus.ID = 0x700 // NMT error control (boot-up, heartbeat, guarding), + node-ID
	LSSMasterID   canbus.ID = 0x7E5 // LSS master to slaves (CiA 305)
	LSSSlaveID    canbus.ID = 0x7E4 // LSS slave to master (CiA 305)
)

// DefaultTPDO returns the default CAN identifier of TPDO n (1..4) of a
// node; 0 when n is out of range.
func DefaultTPDO(n int, id NodeID) canbus.ID {
	if n < 1 || n > 4 {
		return 0
	}
	return FuncTPDO1 + canbus.ID(n-1)*0x100 + canbus.ID(id)
}

// DefaultRPDO returns the default CAN identifier of RPDO n (1..4) of a
// node; 0 when n is out of range.
func DefaultRPDO(n int, id NodeID) canbus.ID {
	if n < 1 || n > 4 {
		return 0
	}
	return FuncRPDO1 + canbus.ID(n-1)*0x100 + canbus.ID(id)
}

// COBID is the value of a COB-ID object (0x1005, 0x1014, 0x1200 sub 1/2,
// 0x1400 and 0x1800 sub 1, ...): the CAN identifier in bits 0..28 and flag
// bits above it.
type COBID uint32

// COB-ID flag bits (CiA 301 7.5.2).
const (
	// COBInvalid (bit 31) disables the object: a PDO or EMCY that does not
	// exist or is not valid, an SDO channel that is not used.
	COBInvalid COBID = 1 << 31
	// COBNoRTR (bit 30) of a PDO COB-ID: no remote transmission request
	// allowed. Of the SYNC COB-ID (0x1005) this bit is "generate": the
	// device is the SYNC producer.
	COBNoRTR COBID = 1 << 30
	// COBGenerate is bit 30 of the SYNC (0x1005) and TIME (0x1012) COB-IDs:
	// the device produces the object.
	COBGenerate COBID = 1 << 30
	// COBConsume is bit 31 of the TIME COB-ID (0x1012): the device consumes
	// TIME stamps. (For SYNC, bit 31 has no meaning.)
	COBConsume COBID = 1 << 31
	// COBExtended (bit 29) selects a 29-bit CAN identifier.
	COBExtended COBID = 1 << 29
)

// NewCOBID returns the COB-ID of a CAN identifier (extended when id does
// not fit in 11 bits).
func NewCOBID(id canbus.ID) COBID {
	c := COBID(id) & COBID(canbus.MaxExtendedID)
	if id > canbus.MaxStandardID {
		c |= COBExtended
	}
	return c
}

// ID returns the CAN identifier.
func (c COBID) ID() canbus.ID {
	if c&COBExtended != 0 {
		return canbus.ID(c) & canbus.MaxExtendedID
	}
	return canbus.ID(c) & canbus.MaxStandardID
}

// Extended reports whether the identifier is a 29-bit one.
func (c COBID) Extended() bool { return c&COBExtended != 0 }

// Valid reports whether bit 31 (invalid) is clear.
func (c COBID) Valid() bool { return c&COBInvalid == 0 }

// Matches reports whether the frame carries this COB-ID's identifier in
// the same format.
func (c COBID) Matches(f *canbus.Frame) bool {
	return f.ID == c.ID() && f.IsExtended() == c.Extended()
}

// Filter returns the go-canbus receive filter for this COB-ID.
func (c COBID) Filter() canbus.Filter {
	if c.Extended() {
		return canbus.ExactExtended(c.ID())
	}
	return canbus.ExactStandard(c.ID())
}

// String formats the COB-ID in hexadecimal, with its flags.
func (c COBID) String() string {
	s := fmt.Sprintf("0x%X", uint32(c.ID()))
	if c.Extended() {
		s += " ext"
	}
	if c&COBInvalid != 0 {
		s += " invalid"
	}
	if c&COBNoRTR != 0 {
		s += " bit30"
	}
	return s
}

// Service is the CANopen service a CAN identifier belongs to in the
// predefined connection set.
type Service uint8

// Services of the predefined connection set.
const (
	ServiceUnknown Service = iota
	ServiceNMT
	ServiceSYNC
	ServiceEMCY
	ServiceTIME
	ServiceTPDO
	ServiceRPDO
	ServiceSDOTx // server to client (response)
	ServiceSDORx // client to server (request)
	ServiceHeartbeat
	ServiceLSS
)

var serviceNames = [...]string{"unknown", "NMT", "SYNC", "EMCY", "TIME", "TPDO", "RPDO", "SDO tx", "SDO rx", "NMT error control", "LSS"}

// String names the service.
func (s Service) String() string {
	if int(s) < len(serviceNames) {
		return serviceNames[s]
	}
	return fmt.Sprintf("Service(%d)", s)
}

// Classify maps an 11-bit CAN identifier to its service in the predefined
// connection set, the node-ID it carries (0 for NMT, SYNC, TIME and LSS)
// and, for PDOs, the PDO number 1..4. Devices may move any COB to another
// identifier, so the answer is the default interpretation only.
func Classify(id canbus.ID) (s Service, node NodeID, pdo int) {
	if id > canbus.MaxStandardID {
		return ServiceUnknown, 0, 0
	}
	switch id {
	case FuncNMT:
		return ServiceNMT, 0, 0
	case FuncSYNC:
		return ServiceSYNC, 0, 0
	case FuncTIME:
		return ServiceTIME, 0, 0
	case LSSMasterID, LSSSlaveID:
		return ServiceLSS, 0, 0
	default: // no special case
	}
	fc, n := id&0x780, NodeID(id&0x7F)
	if !n.Valid() {
		return ServiceUnknown, 0, 0
	}
	switch fc {
	case FuncEMCY:
		return ServiceEMCY, n, 0
	case FuncTPDO1, FuncTPDO2, FuncTPDO3, FuncTPDO4:
		return ServiceTPDO, n, int((fc-FuncTPDO1)/0x100) + 1
	case FuncRPDO1, FuncRPDO2, FuncRPDO3, FuncRPDO4:
		return ServiceRPDO, n, int((fc-FuncRPDO1)/0x100) + 1
	case FuncSDOTx:
		return ServiceSDOTx, n, 0
	case FuncSDORx:
		return ServiceSDORx, n, 0
	case FuncHeartbeat:
		return ServiceHeartbeat, n, 0
	default: // no special case
	}
	return ServiceUnknown, 0, 0
}
