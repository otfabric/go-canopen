// SPDX-License-Identifier: MIT

package cia402

import "fmt"

// Mode is a mode of operation (objects 0x6060 and 0x6061). Negative values
// are manufacturer-specific modes; 0 means no mode.
type Mode int8

// Modes of operation.
const (
	ModeNone Mode = 0
	ModePP   Mode = 1  // profile position
	ModeVL   Mode = 2  // velocity
	ModePV   Mode = 3  // profile velocity
	ModePT   Mode = 4  // profile torque
	ModeHM   Mode = 6  // homing
	ModeIP   Mode = 7  // interpolated position
	ModeCSP  Mode = 8  // cyclic synchronous position
	ModeCSV  Mode = 9  // cyclic synchronous velocity
	ModeCST  Mode = 10 // cyclic synchronous torque
)

var modeNames = map[Mode][2]string{
	ModeNone: {"none", "No mode"},
	ModePP:   {"PP", "Profile position"},
	ModeVL:   {"VL", "Velocity"},
	ModePV:   {"PV", "Profile velocity"},
	ModePT:   {"PT", "Profile torque"},
	ModeHM:   {"HM", "Homing"},
	ModeIP:   {"IP", "Interpolated position"},
	ModeCSP:  {"CSP", "Cyclic synchronous position"},
	ModeCSV:  {"CSV", "Cyclic synchronous velocity"},
	ModeCST:  {"CST", "Cyclic synchronous torque"},
}

// String returns the abbreviation (PP, PV, CSP...), "manufacturer(-n)" for
// manufacturer-specific modes and "Mode(n)" for reserved values.
func (m Mode) String() string {
	if n, ok := modeNames[m]; ok {
		return n[0]
	}
	if m < 0 {
		return fmt.Sprintf("manufacturer(%d)", int8(m))
	}
	return fmt.Sprintf("Mode(%d)", int8(m))
}

// Name returns the full CiA 402 name ("Profile position"...), or String for
// modes without one.
func (m Mode) Name() string {
	if n, ok := modeNames[m]; ok {
		return n[1]
	}
	return m.String()
}

// Manufacturer reports a manufacturer-specific mode (negative value).
func (m Mode) Manufacturer() bool { return m < 0 }

// supportedBits maps the standard modes to their bit in 0x6502.
var supportedBits = map[Mode]uint32{
	ModePP: 1 << 0, ModeVL: 1 << 1, ModePV: 1 << 2, ModePT: 1 << 3,
	ModeHM: 1 << 5, ModeIP: 1 << 6, ModeCSP: 1 << 7, ModeCSV: 1 << 8, ModeCST: 1 << 9,
}

// SupportedBit returns the bit of the mode in the supported drive modes
// object 0x6502, or 0 for modes without one (none, reserved and
// manufacturer-specific modes, whose bits 16..31 the manufacturer assigns).
func (m Mode) SupportedBit() uint32 { return supportedBits[m] }

// SupportedModes encodes modes as an 0x6502 value; modes without a bit are
// ignored.
func SupportedModes(modes ...Mode) uint32 {
	var x uint32
	for _, m := range modes {
		x |= m.SupportedBit()
	}
	return x
}

// DecodeSupportedModes returns the standard modes an 0x6502 value lists, in
// ascending order.
func DecodeSupportedModes(x uint32) []Mode {
	var out []Mode
	for m := Mode(1); m <= ModeCST; m++ {
		if b := m.SupportedBit(); b != 0 && x&b != 0 {
			out = append(out, m)
		}
	}
	return out
}
