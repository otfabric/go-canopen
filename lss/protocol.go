// SPDX-License-Identifier: MIT

package lss

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// CAN identifiers of the LSS messages (CiA 305): the master sends on
// MasterID, slaves answer on SlaveID.
const (
	MasterID = canopen.LSSMasterID // 0x7E5, master to slaves
	SlaveID  = canopen.LSSSlaveID  // 0x7E4, slave to master
)

// Unconfigured is the node-ID of a device whose node-ID has not been
// configured yet (CiA 305: 0xFF, "invalid"). Such a device takes part in
// Fastscan and answers "identify non-configured remote slave".
const Unconfigured canopen.NodeID = 0xFF

// Command is an LSS command specifier: byte 0 of every LSS message.
type Command uint8

// LSS command specifiers. The values are those of CANopenNode's CO_LSS.h,
// python canopen's lss.py and Lely's lss.c, which agree on all of them.
const (
	CmdSwitchGlobal         Command = 0x04 // switch state global (unconfirmed)
	CmdConfigureNodeID      Command = 0x11 // configure node-ID
	CmdConfigureBitTiming   Command = 0x13 // configure bit timing parameters
	CmdActivateBitTiming    Command = 0x15 // activate bit timing parameters (unconfirmed)
	CmdStore                Command = 0x17 // store configuration
	CmdSelectVendor         Command = 0x40 // switch state selective: vendor-ID
	CmdSelectProduct        Command = 0x41 // switch state selective: product code
	CmdSelectRevision       Command = 0x42 // switch state selective: revision number
	CmdSelectSerial         Command = 0x43 // switch state selective: serial number
	CmdSelectResponse       Command = 0x44 // switch state selective: slave response
	CmdIdentifyVendor       Command = 0x46 // identify remote slave: vendor-ID
	CmdIdentifyProduct      Command = 0x47 // identify remote slave: product code
	CmdIdentifyRevisionLow  Command = 0x48 // identify remote slave: revision number, low bound
	CmdIdentifyRevisionHigh Command = 0x49 // identify remote slave: revision number, high bound
	CmdIdentifySerialLow    Command = 0x4A // identify remote slave: serial number, low bound
	CmdIdentifySerialHigh   Command = 0x4B // identify remote slave: serial number, high bound
	CmdIdentifyNonConfig    Command = 0x4C // identify non-configured remote slave
	CmdIdentifySlave        Command = 0x4F // slave response to identify remote slave and Fastscan
	CmdNonConfigSlave       Command = 0x50 // slave response to identify non-configured remote slave
	CmdFastscan             Command = 0x51 // Fastscan
	CmdInquireVendor        Command = 0x5A // inquire identity: vendor-ID
	CmdInquireProduct       Command = 0x5B // inquire identity: product code
	CmdInquireRevision      Command = 0x5C // inquire identity: revision number
	CmdInquireSerial        Command = 0x5D // inquire identity: serial number
	CmdInquireNodeID        Command = 0x5E // inquire node-ID
)

var commandNames = map[Command]string{
	CmdSwitchGlobal:         "switch state global",
	CmdConfigureNodeID:      "configure node-ID",
	CmdConfigureBitTiming:   "configure bit timing",
	CmdActivateBitTiming:    "activate bit timing",
	CmdStore:                "store configuration",
	CmdSelectVendor:         "switch state selective vendor-ID",
	CmdSelectProduct:        "switch state selective product code",
	CmdSelectRevision:       "switch state selective revision number",
	CmdSelectSerial:         "switch state selective serial number",
	CmdSelectResponse:       "switch state selective response",
	CmdIdentifyVendor:       "identify remote slave vendor-ID",
	CmdIdentifyProduct:      "identify remote slave product code",
	CmdIdentifyRevisionLow:  "identify remote slave revision number low",
	CmdIdentifyRevisionHigh: "identify remote slave revision number high",
	CmdIdentifySerialLow:    "identify remote slave serial number low",
	CmdIdentifySerialHigh:   "identify remote slave serial number high",
	CmdIdentifyNonConfig:    "identify non-configured remote slave",
	CmdIdentifySlave:        "identify slave",
	CmdNonConfigSlave:       "identify non-configured slave",
	CmdFastscan:             "fastscan",
	CmdInquireVendor:        "inquire vendor-ID",
	CmdInquireProduct:       "inquire product code",
	CmdInquireRevision:      "inquire revision number",
	CmdInquireSerial:        "inquire serial number",
	CmdInquireNodeID:        "inquire node-ID",
}

// String names the command.
func (c Command) String() string {
	if s, ok := commandNames[c]; ok {
		return s
	}
	return fmt.Sprintf("LSS command 0x%02X", uint8(c))
}

// State is the state of an LSS slave's finite state automaton. The values
// are the mode byte of "switch state global".
type State uint8

// LSS slave states.
const (
	Waiting       State = 0 // LSS waiting: only selection and identification services
	Configuration State = 1 // LSS configuration: configuration and inquiry services
)

// String names the state.
func (s State) String() string {
	switch s {
	case Waiting:
		return "waiting"
	case Configuration:
		return "configuration"
	default:
		return fmt.Sprintf("State(%d)", uint8(s))
	}
}

// Part is one of the four 32-bit numbers of an LSS address, in the order
// of the identity object 0x1018 sub-indices 1..4 and of the Fastscan
// LSSSub/LSSNext values 0..3.
type Part uint8

// The parts of an LSS address.
const (
	PartVendor   Part = 0 // vendor-ID (0x1018:1)
	PartProduct  Part = 1 // product code (0x1018:2)
	PartRevision Part = 2 // revision number (0x1018:3)
	PartSerial   Part = 3 // serial number (0x1018:4)
)

// String names the part.
func (p Part) String() string {
	switch p {
	case PartVendor:
		return "vendor-ID"
	case PartProduct:
		return "product code"
	case PartRevision:
		return "revision number"
	case PartSerial:
		return "serial number"
	default:
		return fmt.Sprintf("Part(%d)", uint8(p))
	}
}

// Address is the 128-bit LSS address of a device: the four numbers of its
// identity object 0x1018. It is unique on a network.
type Address struct {
	Vendor   uint32 `json:"vendorId"`
	Product  uint32 `json:"productCode"`
	Revision uint32 `json:"revisionNumber"`
	Serial   uint32 `json:"serialNumber"`
}

// Part returns one of the four numbers (0 for an unknown part).
func (a Address) Part(p Part) uint32 {
	switch p {
	case PartVendor:
		return a.Vendor
	case PartProduct:
		return a.Product
	case PartRevision:
		return a.Revision
	case PartSerial:
		return a.Serial
	default:
		return 0
	}
}

// SetPart sets one of the four numbers (an unknown part is ignored).
func (a *Address) SetPart(p Part, v uint32) {
	switch p {
	case PartVendor:
		a.Vendor = v
	case PartProduct:
		a.Product = v
	case PartRevision:
		a.Revision = v
	case PartSerial:
		a.Serial = v
	default: // not a part
	}
}

// String formats the address as vendor:product:revision:serial in
// hexadecimal.
func (a Address) String() string {
	return fmt.Sprintf("%08X:%08X:%08X:%08X", a.Vendor, a.Product, a.Revision, a.Serial)
}

// AddressFromDictionary reads the LSS address from the identity object
// 0x1018 of a dictionary.
func AddressFromDictionary(d *od.Dictionary) (Address, error) {
	var a Address
	for p := PartVendor; p <= PartSerial; p++ {
		v, err := d.GetUint(od.IndexIdentity, uint8(p)+1)
		if err != nil {
			return Address{}, fmt.Errorf("lss: identity 0x1018:%d: %w", p+1, err)
		}
		a.SetPart(p, uint32(v))
	}
	return a, nil
}

// BitTiming is an index into the CiA 301 bit timing table (table selector
// 0), as "configure bit timing parameters" carries it.
type BitTiming uint8

// The CiA 301 bit timing table, as CANopenNode (CO_LSS.h) and Lely
// (lss.c) define it. Index 5 is reserved there; python canopen's
// documentation lists it as 100 kbit/s. This package passes index 5 to
// the slave's application like any other index and reports Rate 0 for it.
const (
	BitTiming1000 BitTiming = 0 // 1000 kbit/s
	BitTiming800  BitTiming = 1 // 800 kbit/s
	BitTiming500  BitTiming = 2 // 500 kbit/s
	BitTiming250  BitTiming = 3 // 250 kbit/s
	BitTiming125  BitTiming = 4 // 125 kbit/s
	BitTiming50   BitTiming = 6 // 50 kbit/s
	BitTiming20   BitTiming = 7 // 20 kbit/s
	BitTiming10   BitTiming = 8 // 10 kbit/s
	BitTimingAuto BitTiming = 9 // automatic bit rate detection
)

var bitRates = [...]int{1000, 800, 500, 250, 125, 0, 50, 20, 10, 0}

// Rate returns the bit rate in kbit/s; 0 for automatic detection, the
// reserved index 5 and indices beyond the table.
func (b BitTiming) Rate() int {
	if int(b) < len(bitRates) {
		return bitRates[b]
	}
	return 0
}

// Valid reports whether b is an index of the table (0..9, including the
// reserved 5).
func (b BitTiming) Valid() bool { return b <= BitTimingAuto }

// String formats the bit timing.
func (b BitTiming) String() string {
	switch {
	case b == BitTimingAuto:
		return "auto"
	case b.Rate() != 0:
		return fmt.Sprintf("%d kbit/s", b.Rate())
	default:
		return fmt.Sprintf("BitTiming(%d)", uint8(b))
	}
}

// BitTimingFor returns the table index of a bit rate in kbit/s (0 for
// automatic detection); ok is false for a rate the table lacks.
func BitTimingFor(kbit int) (b BitTiming, ok bool) {
	if kbit == 0 {
		return BitTimingAuto, true
	}
	for i, r := range bitRates {
		if r == kbit {
			return BitTiming(i), true
		}
	}
	return 0, false
}

// Error codes of the confirmed configuration services (byte 1 of the
// response). CiA 305 defines 0 (success), 1 and, for store configuration,
// 2; 0xFF is implementation-specific, with the detail in the
// "specific error" byte 2.
const (
	CodeOK            uint8 = 0x00 // success
	CodeOutOfRange    uint8 = 0x01 // node-ID out of range; bit timing not supported; store not supported
	CodeStoreFailed   uint8 = 0x02 // store configuration: storage media access error
	CodeImplementSpec uint8 = 0xFF // implementation-specific error, detail in Spec
)

// ConfigError is a refusal of a configuration service (configure node-ID,
// configure bit timing, store configuration) by a slave, or the error a
// slave's application returns to choose the error code it answers with.
type ConfigError struct {
	Command Command // the service
	Code    uint8   // error code (CodeOutOfRange, CodeStoreFailed, CodeImplementSpec, ...)
	Spec    uint8   // specific error, meaningful with CodeImplementSpec
}

// Error describes the refusal.
func (e *ConfigError) Error() string {
	var what string
	switch {
	case e.Code == CodeOutOfRange && e.Command == CmdConfigureNodeID:
		what = "node-ID out of range"
	case e.Code == CodeOutOfRange && e.Command == CmdConfigureBitTiming:
		what = "bit timing not supported"
	case e.Code == CodeOutOfRange && e.Command == CmdStore:
		what = "store configuration not supported"
	case e.Code == CodeStoreFailed && e.Command == CmdStore:
		what = "storage media access error"
	case e.Code == CodeImplementSpec:
		what = fmt.Sprintf("implementation-specific error 0x%02X", e.Spec)
	default:
		what = fmt.Sprintf("error code 0x%02X", e.Code)
	}
	return fmt.Sprintf("lss: %s refused: %s", e.Command, what)
}

// Fastscan constants (byte 5, BitCheck): 0..31 is the lowest bit that
// must match, 0x80 resets the slaves' Fastscan and asks every candidate to
// answer.
const fastscanConfirm = 0x80

// frame builds an 8-byte LSS frame (CiA 305: every LSS message has 8
// bytes; unused bytes are 0).
func frame(id canbus.ID, cs Command, data ...byte) canbus.Frame {
	f := canbus.Frame{ID: id, Len: 8}
	f.Data[0] = byte(cs)
	copy(f.Data[1:8], data)
	return f
}

// frame32 builds an LSS frame carrying a 32-bit little-endian value in
// bytes 1..4.
func frame32(id canbus.ID, cs Command, v uint32, tail ...byte) canbus.Frame {
	f := frame(id, cs)
	binary.LittleEndian.PutUint32(f.Data[1:5], v)
	copy(f.Data[5:8], tail)
	return f
}

// fastscanFrame builds a Fastscan request.
func fastscanFrame(id uint32, bitCheck uint8, sub, next Part) canbus.Frame {
	return frame32(MasterID, CmdFastscan, id, bitCheck, byte(sub), byte(next))
}

// u32 reads bytes 1..4 of an LSS frame.
func u32(f *canbus.Frame) uint32 { return binary.LittleEndian.Uint32(f.Data[1:5]) }

// lssFrame reports whether f is a classical data frame on id.
func lssFrame(f *canbus.Frame, id canbus.ID) bool {
	return f.ID == id && f.Flags&(canbus.FlagExtended|canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) == 0
}

// delayMs converts a switch delay to the 16-bit millisecond value of
// "activate bit timing parameters".
func delayMs(d time.Duration) (uint16, error) {
	ms := d.Milliseconds()
	if d < 0 || ms > 0xFFFF {
		return 0, fmt.Errorf("lss: switch delay %v outside 0..65535 ms", d)
	}
	return uint16(ms), nil
}
