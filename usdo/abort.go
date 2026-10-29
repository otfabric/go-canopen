// SPDX-License-Identifier: MIT

package usdo

import (
	"fmt"

	"github.com/otfabric/go-canopen"
)

// AbortCode is a USDO abort code: one byte (CiA's CANopen FD poster, table
// "USDO abort codes"; MicroControl's CANopen FD master documentation
// shows it as the 7th byte of an abort message, 12h for a timeout). It
// implements error.
type AbortCode uint8

// USDO abort codes (CiA 1301, as CiA's CANopen FD poster lists them).
const (
	AbortUnknown       AbortCode = 0x10 // unknown error
	AbortProtocol      AbortCode = 0x11 // USDO protocol error detected
	AbortTimeout       AbortCode = 0x12 // USDO time out error
	AbortCommand       AbortCode = 0x13 // unknown command specifier
	AbortCounter       AbortCode = 0x14 // invalid value in counter byte (segmented protocol)
	AbortSize          AbortCode = 0x15 // incorrect data size
	AbortOutOfMemory   AbortCode = 0x16 // out of memory
	AbortRouting       AbortCode = 0x19 // USDO general routing error
	AbortDestNetwork   AbortCode = 0x1A // USDO destination network unknown
	AbortDestNode      AbortCode = 0x1B // USDO destination node unknown
	AbortSourceNetwork AbortCode = 0x1C // USDO source network unknown
	AbortSourceNode    AbortCode = 0x1D // USDO source node unknown
	AbortDLC           AbortCode = 0x1E // unexpected CAN DLC
	AbortMultiplexer   AbortCode = 0x1F // unexpected multiplexer
	AbortParallel      AbortCode = 0x20 // parallel USDO access to the very same sub-index
	AbortSession       AbortCode = 0x21 // session-ID wrong or unknown
	AbortWriteOnly     AbortCode = 0x31 // attempt to read a write only data element
	AbortReadOnly      AbortCode = 0x32 // attempt to write a read only data element
	AbortNoObject      AbortCode = 0x33 // data object does not exist in the object dictionary
	AbortNoSubIndex    AbortCode = 0x34 // sub-index does not exist
	AbortHardware      AbortCode = 0x35 // access failed due to a hardware error
	AbortLength        AbortCode = 0x36 // data type does not match, length of service parameter does not match
	AbortLengthHigh    AbortCode = 0x37 // ... length of service parameter too high
	AbortLengthLow     AbortCode = 0x38 // ... length of service parameter too low
	AbortInvalidValue  AbortCode = 0x40 // invalid value for parameter (download only)
	AbortValueHigh     AbortCode = 0x41 // value of parameter written too high (download only)
	AbortValueLow      AbortCode = 0x42 // value of parameter written too low (download only)
	AbortMaxLessMin    AbortCode = 0x43 // maximum value is less than minimum value
	AbortMinMoreMax    AbortCode = 0x44 // minimum value is higher than maximum value
	AbortCANIDReadOnly AbortCode = 0x45 // CAN-ID and CAN frame format are of access type read-only
	AbortNotMappable   AbortCode = 0x50 // data element cannot be mapped to the PDO
	AbortPDOLength     AbortCode = 0x51 // number and length of the mapped data elements would exceed the PDO length
	AbortNonMappable   AbortCode = 0x52 // attempt to map a nonmappable data element to a PDO
	AbortRPDOToTPDO    AbortCode = 0x53 // attempt to map a data element of PDO access type RPDO to a TPDO
	AbortTPDOToRPDO    AbortCode = 0x54 // attempt to map a data element of PDO access type TPDO to a RPDO
	AbortPDOValid      AbortCode = 0x55 // attempt to write to PDO parameters, while PDO is valid
	AbortMappingValid  AbortCode = 0x56 // attempt to write to PDO mapping parameters, while PDO mapping is valid
	AbortTransfer      AbortCode = 0x60 // data cannot be transferred or stored to the application
	AbortLocalControl  AbortCode = 0x61 // ... because of local control
	AbortDeviceState   AbortCode = 0x62 // ... because of the present device state
	AbortNoDictionary  AbortCode = 0x63 // object dictionary dynamic generation fails or no object dictionary is present
	AbortNoData        AbortCode = 0x64 // no data available
	AbortBusy          AbortCode = 0x70 // USDO server busy
)

var abortText = map[AbortCode]string{
	AbortUnknown: "unknown error", AbortProtocol: "USDO protocol error detected", AbortTimeout: "USDO time out error",
	AbortCommand: "unknown command specifier", AbortCounter: "invalid value in counter byte",
	AbortSize: "incorrect data size", AbortOutOfMemory: "out of memory", AbortRouting: "USDO general routing error",
	AbortDestNetwork: "USDO destination network unknown", AbortDestNode: "USDO destination node unknown",
	AbortSourceNetwork: "USDO source network unknown", AbortSourceNode: "USDO source node unknown",
	AbortDLC: "unexpected CAN DLC", AbortMultiplexer: "unexpected multiplexer",
	AbortParallel:  "data transfer not possible; parallel USDO access to the very same sub-index",
	AbortSession:   "session-ID wrong or unknown",
	AbortWriteOnly: "attempt to read a write only data element", AbortReadOnly: "attempt to write a read only data element",
	AbortNoObject: "data object does not exist in the object dictionary", AbortNoSubIndex: "sub-index does not exist",
	AbortHardware:     "access failed due to a hardware error",
	AbortLength:       "data type does not match, length of service parameter does not match",
	AbortLengthHigh:   "data type does not match, length of service parameter too high",
	AbortLengthLow:    "data type does not match, length of service parameter too low",
	AbortInvalidValue: "invalid value for parameter", AbortValueHigh: "value of parameter written too high",
	AbortValueLow: "value of parameter written too low", AbortMaxLessMin: "maximum value is less than minimum value",
	AbortMinMoreMax: "minimum value is higher than maximum value", AbortCANIDReadOnly: "CAN-ID and CAN frame format are of access type read-only",
	AbortNotMappable:  "data element cannot be mapped to the PDO",
	AbortPDOLength:    "the number and length of the data elements to be mapped would exceed PDO length",
	AbortNonMappable:  "attempt to map a nonmappable data element to a PDO",
	AbortRPDOToTPDO:   "attempt to map a data element of PDO access type RPDO to a TPDO",
	AbortTPDOToRPDO:   "attempt to map a data element of PDO access type TPDO to a RPDO",
	AbortPDOValid:     "attempt to write to PDO parameters, while PDO is valid",
	AbortMappingValid: "attempt to write to PDO mapping parameters, while PDO mapping is valid",
	AbortTransfer:     "data cannot be transferred or stored to the application",
	AbortLocalControl: "data cannot be transferred or stored to the application because of local control",
	AbortDeviceState:  "data cannot be transferred or stored to the application because of the present device state",
	AbortNoDictionary: "object dictionary dynamic generation fails or no object dictionary is present",
	AbortNoData:       "no data available", AbortBusy: "USDO server busy",
}

// Text describes the code; empty for codes the poster does not define.
func (c AbortCode) Text() string { return abortText[c] }

// Error implements error.
func (c AbortCode) Error() string {
	if t := abortText[c]; t != "" {
		return fmt.Sprintf("usdo: abort 0x%02X: %s", uint8(c), t)
	}
	return fmt.Sprintf("usdo: abort 0x%02X", uint8(c))
}

// fromSDO maps the classical codes onto the USDO codes with the same
// description; codes without a counterpart become AbortUnknown.
var fromSDO = map[canopen.AbortCode]AbortCode{
	canopen.AbortToggle: AbortCounter, canopen.AbortTimeout: AbortTimeout, canopen.AbortCommand: AbortCommand,
	canopen.AbortBlockSize: AbortProtocol, canopen.AbortSequence: AbortCounter, canopen.AbortCRC: AbortProtocol,
	canopen.AbortOutOfMemory: AbortOutOfMemory, canopen.AbortWriteOnly: AbortWriteOnly, canopen.AbortReadOnly: AbortReadOnly,
	canopen.AbortNoObject: AbortNoObject, canopen.AbortNotMappable: AbortNotMappable, canopen.AbortPDOLength: AbortPDOLength,
	canopen.AbortHardware: AbortHardware, canopen.AbortLength: AbortLength, canopen.AbortLengthHigh: AbortLengthHigh,
	canopen.AbortLengthLow: AbortLengthLow, canopen.AbortNoSubIndex: AbortNoSubIndex, canopen.AbortInvalidValue: AbortInvalidValue,
	canopen.AbortValueHigh: AbortValueHigh, canopen.AbortValueLow: AbortValueLow, canopen.AbortMaxLessMin: AbortMaxLessMin,
	canopen.AbortNoResource: AbortBusy, canopen.AbortTransfer: AbortTransfer, canopen.AbortLocalControl: AbortLocalControl,
	canopen.AbortDeviceState: AbortDeviceState, canopen.AbortNoDictionary: AbortNoDictionary, canopen.AbortNoData: AbortNoData,
}

// toSDO is the reverse mapping for the codes that have a classical
// counterpart.
var toSDO = map[AbortCode]canopen.AbortCode{
	AbortUnknown: canopen.AbortGeneral, AbortProtocol: canopen.AbortGeneral, AbortTimeout: canopen.AbortTimeout,
	AbortCommand: canopen.AbortCommand, AbortCounter: canopen.AbortSequence, AbortSize: canopen.AbortLength,
	AbortOutOfMemory: canopen.AbortOutOfMemory, AbortParallel: canopen.AbortDeviceState,
	AbortWriteOnly: canopen.AbortWriteOnly, AbortReadOnly: canopen.AbortReadOnly, AbortNoObject: canopen.AbortNoObject,
	AbortNoSubIndex: canopen.AbortNoSubIndex, AbortHardware: canopen.AbortHardware, AbortLength: canopen.AbortLength,
	AbortLengthHigh: canopen.AbortLengthHigh, AbortLengthLow: canopen.AbortLengthLow, AbortInvalidValue: canopen.AbortInvalidValue,
	AbortValueHigh: canopen.AbortValueHigh, AbortValueLow: canopen.AbortValueLow, AbortMaxLessMin: canopen.AbortMaxLessMin,
	AbortMinMoreMax: canopen.AbortMaxLessMin, AbortCANIDReadOnly: canopen.AbortReadOnly, AbortNotMappable: canopen.AbortNotMappable,
	AbortPDOLength: canopen.AbortPDOLength, AbortNonMappable: canopen.AbortNotMappable, AbortRPDOToTPDO: canopen.AbortNotMappable,
	AbortTPDOToRPDO: canopen.AbortNotMappable, AbortPDOValid: canopen.AbortUnsupported, AbortMappingValid: canopen.AbortUnsupported,
	AbortTransfer: canopen.AbortTransfer, AbortLocalControl: canopen.AbortLocalControl, AbortDeviceState: canopen.AbortDeviceState,
	AbortNoDictionary: canopen.AbortNoDictionary, AbortNoData: canopen.AbortNoData, AbortBusy: canopen.AbortNoResource,
}

// FromSDO returns the USDO abort code with the meaning of a classical SDO
// abort code (the object dictionary refuses accesses with those);
// [AbortUnknown] when there is none.
func FromSDO(c canopen.AbortCode) AbortCode {
	if u, ok := fromSDO[c]; ok {
		return u
	}
	return AbortUnknown
}

// SDO returns the classical SDO abort code closest in meaning, so that
// errors.Is(err, canopen.AbortNoObject) works on USDO errors;
// [canopen.AbortGeneral] when there is none.
func (c AbortCode) SDO() canopen.AbortCode {
	if s, ok := toSDO[c]; ok {
		return s
	}
	return canopen.AbortGeneral
}

// AbortError is a USDO transfer that ended with an abort, sent by the
// server (Remote) or by the client. It unwraps to its [AbortCode], to the
// closest [canopen.AbortCode] ([AbortCode.SDO]) and, for a client timeout,
// to [canopen.ErrTimeout].
type AbortError struct {
	Code     AbortCode
	Node     canopen.NodeID // the server
	Index    uint16
	SubIndex uint8
	Remote   bool
	timeout  bool
}

// Error implements error.
func (e *AbortError) Error() string {
	who := "local"
	if e.Remote {
		who = "remote"
	}
	return fmt.Sprintf("%s (%s, node %d, object 0x%04X:%02X)", e.Code.Error(), who, e.Node, e.Index, e.SubIndex)
}

// Unwrap returns the abort codes (and canopen.ErrTimeout for a timeout).
func (e *AbortError) Unwrap() []error {
	errs := []error{e.Code, e.Code.SDO()}
	if e.timeout {
		errs = append(errs, canopen.ErrTimeout)
	}
	return errs
}

// codeOf returns the USDO abort code of a dictionary error.
func codeOf(err error) AbortCode {
	var a canopen.AbortCode
	if asAbort(err, &a) {
		return FromSDO(a)
	}
	return AbortTransfer
}
