// SPDX-License-Identifier: MIT

// Package syncobj implements the CANopen SYNC object (CiA 301 7.2.5). It
// lives in the directory sync/ of the module but is named syncobj, so that
// it does not shadow the standard library's sync package where both are
// imported:
//
//	import "github.com/otfabric/go-canopen/sync" // package syncobj
//
// A [Producer] sends the SYNC message every communication cycle period
// (0x1006, µs) on the SYNC COB-ID (0x1005, produced when bit 30 is set),
// with a counter byte 1..overflow when the synchronous counter overflow
// value (0x1019) is 2..240. A [Consumer] receives SYNC messages and
// reports them with their counter and the end of the synchronous window
// (0x1007), which is what synchronous PDOs are driven by.
//
// Timing: the producer uses a time.Ticker, so SYNC does not drift over
// time, but every message is subject to the scheduling latency of the Go
// runtime and the operating system (jitter typically in the tens to
// hundreds of microseconds on an idle system, more under load). Consumers
// timestamp SYNC with the frame's receive time when the transport reports
// one (SocketCAN does), which is closer to the bus than the handler call.
//
// Long-running services have Start, which subscribes before it returns,
// and Close, which stops them and waits for their goroutine. Callbacks run
// on the service's goroutine and must not block for long.
package syncobj
