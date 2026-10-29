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
// [CodeText] describes the error codes of CiA 301 table 21, and
// [ErrorRegister] names the error register bits.
//
// Long-running services have Start, which subscribes before it returns,
// and Close, which stops them and waits for their goroutine. Callbacks run
// on the service's goroutine and must not block for long.
package emcy
