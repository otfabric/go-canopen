// SPDX-License-Identifier: MIT

package canopen

import "fmt"

// AbortCode is an SDO abort code (CiA 301 7.2.4.3.17). It is also the
// error type the object dictionary returns for refused accesses, so that an
// SDO server can pass the reason on unchanged. Test with errors.As.
type AbortCode uint32

// SDO abort codes.
const (
	AbortToggle            AbortCode = 0x05030000 // toggle bit not alternated
	AbortTimeout           AbortCode = 0x05040000 // SDO protocol timed out
	AbortCommand           AbortCode = 0x05040001 // command specifier not valid or unknown
	AbortBlockSize         AbortCode = 0x05040002 // invalid block size (block mode)
	AbortSequence          AbortCode = 0x05040003 // invalid sequence number (block mode)
	AbortCRC               AbortCode = 0x05040004 // CRC error (block mode)
	AbortOutOfMemory       AbortCode = 0x05040005 // out of memory
	AbortUnsupported       AbortCode = 0x06010000 // unsupported access to an object
	AbortWriteOnly         AbortCode = 0x06010001 // attempt to read a write-only object
	AbortReadOnly          AbortCode = 0x06010002 // attempt to write a read-only object
	AbortNoObject          AbortCode = 0x06020000 // object does not exist
	AbortNotMappable       AbortCode = 0x06040041 // object cannot be mapped to the PDO
	AbortPDOLength         AbortCode = 0x06040042 // mapped objects exceed the PDO length
	AbortParamIncompatible AbortCode = 0x06040043 // general parameter incompatibility
	AbortInternal          AbortCode = 0x06040047 // general internal incompatibility in the device
	AbortHardware          AbortCode = 0x06060000 // access failed due to a hardware error
	AbortLength            AbortCode = 0x06070010 // data type or length does not match
	AbortLengthHigh        AbortCode = 0x06070012 // length of service parameter too high
	AbortLengthLow         AbortCode = 0x06070013 // length of service parameter too low
	AbortNoSubIndex        AbortCode = 0x06090011 // sub-index does not exist
	AbortInvalidValue      AbortCode = 0x06090030 // invalid value for parameter
	AbortValueHigh         AbortCode = 0x06090031 // value written too high
	AbortValueLow          AbortCode = 0x06090032 // value written too low
	AbortMaxLessMin        AbortCode = 0x06090036 // maximum value is less than minimum value
	AbortNoResource        AbortCode = 0x060A0023 // resource not available: SDO connection
	AbortGeneral           AbortCode = 0x08000000 // general error
	AbortTransfer          AbortCode = 0x08000020 // data cannot be transferred or stored
	AbortLocalControl      AbortCode = 0x08000021 // ... because of local control
	AbortDeviceState       AbortCode = 0x08000022 // ... because of the present device state
	AbortNoDictionary      AbortCode = 0x08000023 // no object dictionary present
	AbortNoData            AbortCode = 0x08000024 // no data available
)

var abortText = map[AbortCode]string{
	AbortToggle:            "toggle bit not alternated",
	AbortTimeout:           "SDO protocol timed out",
	AbortCommand:           "client/server command specifier not valid or unknown",
	AbortBlockSize:         "invalid block size",
	AbortSequence:          "invalid sequence number",
	AbortCRC:               "CRC error",
	AbortOutOfMemory:       "out of memory",
	AbortUnsupported:       "unsupported access to an object",
	AbortWriteOnly:         "attempt to read a write-only object",
	AbortReadOnly:          "attempt to write a read-only object",
	AbortNoObject:          "object does not exist in the object dictionary",
	AbortNotMappable:       "object cannot be mapped to the PDO",
	AbortPDOLength:         "number and length of mapped objects exceed the PDO length",
	AbortParamIncompatible: "general parameter incompatibility",
	AbortInternal:          "general internal incompatibility in the device",
	AbortHardware:          "access failed due to a hardware error",
	AbortLength:            "data type does not match, length of service parameter does not match",
	AbortLengthHigh:        "data type does not match, length of service parameter too high",
	AbortLengthLow:         "data type does not match, length of service parameter too low",
	AbortNoSubIndex:        "sub-index does not exist",
	AbortInvalidValue:      "invalid value for parameter",
	AbortValueHigh:         "value of parameter written too high",
	AbortValueLow:          "value of parameter written too low",
	AbortMaxLessMin:        "maximum value is less than minimum value",
	AbortNoResource:        "resource not available: SDO connection",
	AbortGeneral:           "general error",
	AbortTransfer:          "data cannot be transferred or stored to the application",
	AbortLocalControl:      "data cannot be transferred or stored because of local control",
	AbortDeviceState:       "data cannot be transferred or stored because of the present device state",
	AbortNoDictionary:      "object dictionary dynamic generation fails or no object dictionary is present",
	AbortNoData:            "no data available",
}

// Text describes the code; empty for codes CiA 301 does not define.
func (c AbortCode) Text() string { return abortText[c] }

// Error implements error.
func (c AbortCode) Error() string {
	if t := abortText[c]; t != "" {
		return fmt.Sprintf("canopen: SDO abort 0x%08X: %s", uint32(c), t)
	}
	return fmt.Sprintf("canopen: SDO abort 0x%08X", uint32(c))
}

// AbortError is an SDO abort with the object it concerns, as reported by
// the SDO client. It unwraps to its [AbortCode].
type AbortError struct {
	Code     AbortCode
	Index    uint16
	SubIndex uint8
	// Remote is true when the peer sent the abort, false when this side
	// aborted the transfer.
	Remote bool
}

// Error implements error.
func (e *AbortError) Error() string {
	who := "local"
	if e.Remote {
		who = "remote"
	}
	return fmt.Sprintf("%s (%s, object 0x%04X:%02X)", e.Code.Error(), who, e.Index, e.SubIndex)
}

// Unwrap returns the abort code.
func (e *AbortError) Unwrap() error { return e.Code }
