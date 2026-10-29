// SPDX-License-Identifier: MIT

package emcy

import (
	"fmt"
	"strings"
)

// Emergency error codes of CiA 301 (table 21). The low byte of a generic
// code (xx00) is free for device profiles and manufacturers; [CodeText]
// falls back to the class of such codes.
const (
	CodeNoError           uint16 = 0x0000 // error reset or no error
	CodeGeneric           uint16 = 0x1000
	CodeCurrent           uint16 = 0x2000
	CodeCurrentInput      uint16 = 0x2100 // current, device input side
	CodeCurrentInside     uint16 = 0x2200 // current inside the device
	CodeCurrentOutput     uint16 = 0x2300 // current, device output side
	CodeVoltage           uint16 = 0x3000
	CodeVoltageMains      uint16 = 0x3100
	CodeVoltageInside     uint16 = 0x3200
	CodeVoltageOutput     uint16 = 0x3300
	CodeTemperature       uint16 = 0x4000
	CodeTemperatureAmb    uint16 = 0x4100 // ambient temperature
	CodeTemperatureDevice uint16 = 0x4200
	CodeHardware          uint16 = 0x5000
	CodeSoftware          uint16 = 0x6000
	CodeSoftwareInternal  uint16 = 0x6100
	CodeSoftwareUser      uint16 = 0x6200
	CodeDataSet           uint16 = 0x6300
	CodeAdditionalModules uint16 = 0x7000
	CodeMonitoring        uint16 = 0x8000
	CodeCommunication     uint16 = 0x8100
	CodeCANOverrun        uint16 = 0x8110 // CAN overrun (objects lost)
	CodeCANErrorPassive   uint16 = 0x8120 // CAN in error passive mode
	CodeHeartbeat         uint16 = 0x8130 // life guard error or heartbeat error
	CodeBusOffRecovered   uint16 = 0x8140 // recovered from bus off
	CodeCANIDCollision    uint16 = 0x8150
	CodeProtocol          uint16 = 0x8200 // protocol error, generic
	CodePDOLength         uint16 = 0x8210 // PDO not processed due to length error
	CodePDOLengthExceeded uint16 = 0x8220 // PDO length exceeded
	CodeDAMMPDO           uint16 = 0x8230 // DAM MPDO not processed, destination object not available
	CodeSYNCLength        uint16 = 0x8240 // unexpected SYNC data length
	CodeRPDOTimeout       uint16 = 0x8250
	CodeExternal          uint16 = 0x9000
	CodeAdditionalFuncs   uint16 = 0xF000
	CodeDeviceSpecific    uint16 = 0xFF00
)

var codeText = map[uint16]string{
	CodeNoError:           "error reset or no error",
	CodeGeneric:           "generic error",
	CodeCurrent:           "current, generic error",
	CodeCurrentInput:      "current, device input side",
	CodeCurrentInside:     "current inside the device",
	CodeCurrentOutput:     "current, device output side",
	CodeVoltage:           "voltage, generic error",
	CodeVoltageMains:      "mains voltage",
	CodeVoltageInside:     "voltage inside the device",
	CodeVoltageOutput:     "output voltage",
	CodeTemperature:       "temperature, generic error",
	CodeTemperatureAmb:    "ambient temperature",
	CodeTemperatureDevice: "device temperature",
	CodeHardware:          "device hardware, generic error",
	CodeSoftware:          "device software, generic error",
	CodeSoftwareInternal:  "internal software",
	CodeSoftwareUser:      "user software",
	CodeDataSet:           "data set",
	CodeAdditionalModules: "additional modules, generic error",
	CodeMonitoring:        "monitoring, generic error",
	CodeCommunication:     "communication, generic error",
	CodeCANOverrun:        "CAN overrun (objects lost)",
	CodeCANErrorPassive:   "CAN in error passive mode",
	CodeHeartbeat:         "life guard error or heartbeat error",
	CodeBusOffRecovered:   "recovered from bus off",
	CodeCANIDCollision:    "CAN-ID collision",
	CodeProtocol:          "protocol error, generic",
	CodePDOLength:         "PDO not processed due to length error",
	CodePDOLengthExceeded: "PDO length exceeded",
	CodeDAMMPDO:           "DAM MPDO not processed, destination object not available",
	CodeSYNCLength:        "unexpected SYNC data length",
	CodeRPDOTimeout:       "RPDO timeout",
	CodeExternal:          "external error, generic error",
	CodeAdditionalFuncs:   "additional functions, generic error",
	CodeDeviceSpecific:    "device specific, generic error",
}

// CodeText describes an emergency error code after CiA 301 table 21. A
// code the table does not list is described by its class (0x8113 as
// "CAN overrun (objects lost)", 0x2310 as "current, device output side",
// 0x5432 as "device hardware, generic error"), with the code in
// parentheses when it is not exactly the listed one.
func CodeText(code uint16) string {
	if t, ok := codeText[code]; ok {
		return t
	}
	for _, mask := range []uint16{0xFFF0, 0xFF00, 0xF000} {
		if code&0xF000 != 0x8000 && mask == 0xFFF0 {
			continue // only the 0x81xx and 0x82xx codes have sub-classes
		}
		if t, ok := codeText[code&mask]; ok && code&mask != 0 {
			return fmt.Sprintf("%s (0x%04X)", t, code)
		}
	}
	return fmt.Sprintf("unknown error code 0x%04X", code)
}

// ErrorRegister is the error register (object 0x1001), sent in every
// emergency message.
type ErrorRegister uint8

// Error register bits (CiA 301 7.5.2.2). RegGeneric is set whenever any
// error is active.
const (
	RegGeneric       ErrorRegister = 1 << 0
	RegCurrent       ErrorRegister = 1 << 1
	RegVoltage       ErrorRegister = 1 << 2
	RegTemperature   ErrorRegister = 1 << 3
	RegCommunication ErrorRegister = 1 << 4 // overrun, error state
	RegDeviceProfile ErrorRegister = 1 << 5 // device profile specific
	RegReserved      ErrorRegister = 1 << 6 // always 0
	RegManufacturer  ErrorRegister = 1 << 7 // manufacturer-specific
)

var registerNames = [8]string{"generic", "current", "voltage", "temperature", "communication", "device-profile", "reserved", "manufacturer"}

// Names returns the names of the set bits, lowest first.
func (r ErrorRegister) Names() []string {
	var out []string
	for i, n := range registerNames {
		if r&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// String lists the set bits ("generic|current"), or "none".
func (r ErrorRegister) String() string {
	if r == 0 {
		return "none"
	}
	return strings.Join(r.Names(), "|")
}

// ClassRegister returns the error register bits CiA 301 associates with
// the class of an error code: RegGeneric, plus RegCurrent (0x2xxx),
// RegVoltage (0x3xxx), RegTemperature (0x4xxx) or RegCommunication
// (0x81xx, 0x82xx). It returns 0 for [CodeNoError].
func ClassRegister(code uint16) ErrorRegister {
	if code == CodeNoError {
		return 0
	}
	r := RegGeneric
	switch {
	case code&0xF000 == 0x2000:
		r |= RegCurrent
	case code&0xF000 == 0x3000:
		r |= RegVoltage
	case code&0xF000 == 0x4000:
		r |= RegTemperature
	case code&0xFF00 == 0x8100 || code&0xFF00 == 0x8200:
		r |= RegCommunication
	}
	return r
}
