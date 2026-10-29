// SPDX-License-Identifier: MIT

// Package emcy implements the CANopen emergency object (CiA 301 7.2.7).
//
// A [Producer] is a node's side: [Producer.Raise] records an error
// (active error set, error register 0x1001, pre-defined error field
// 0x1003) and sends its emergency message; [Producer.Clear] deactivates it
// and sends the error reset message (0x0000) once no error remains. The
// COB-ID (0x1014, whose invalid bit suppresses the messages) and the
// inhibit time (0x1015, in 100 µs units) come from options or from the
// node's dictionary; messages within the inhibit time are delayed, not
// lost.
//
// A [Consumer] receives the emergency messages of all nodes (or of given
// COB-IDs) and reports them as [Event] values.
//
// # CANopen FD
//
// CiA 1301 replaces the 8-byte message by a 20-byte one ([FDMessage]):
// logical device number, CiA specification number of the error code, the
// classical code, register and 5 device-specific bytes, a status byte and
// a TIME_OF_DAY time stamp, on the same COB-ID (default 080h + node-ID).
// [WithFD] makes a producer send it and a consumer accept it. Sources:
// CiA's CANopen FD poster (CiA 1301, field list with byte positions, 2026
// edition), the HMS/Ixxat "Basics of the CANopen and CANopen FD protocol"
// whitepaper (same fields, and an error status state machine), CiA's
// "EMCY write protocol" page (field meanings) and its CAN Newsletter
// article on logical devices in CANopen FD (20 bytes). The poster places
// the EEC in bytes 4..5 and the register in byte 6; the whitepaper the
// register in byte 4 and the EEC in 5..6: [FDLayoutCiA] (default) and
// [FDLayoutRegisterFirst] select between them. The status encoding and
// the byte order of the CiA specification number are not public (raw
// byte; little-endian assumed).
//
// [CodeText] describes the error codes of CiA 301 table 21, and
// [ErrorRegister] names the error register bits.
//
// Long-running services have Start, which subscribes before it returns,
// and Close, which stops them and waits for their goroutine. Callbacks run
// on the service's goroutine and must not block for long.
package emcy
