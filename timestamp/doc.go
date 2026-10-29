// SPDX-License-Identifier: MIT

// Package timestamp implements the CANopen TIME object (CiA 301 7.2.6) and
// the TIME_OF_DAY and TIME_DIFFERENCE data types (CiA 301 7.1.6.5).
//
// A [TimeOfDay] is milliseconds after midnight (28 bits) and days since
// 1984-01-01, encoded in 6 bytes; a [TimeDifference] has the same layout
// for a duration. [FromTime] and [TimeOfDay.Time] convert to and from
// time.Time (in UTC: CiA 301 does not define a time zone, and CANopen
// networks commonly use UTC).
//
// A [Producer] sends TIME messages on the COB-ID TIME (0x1012; with a
// dictionary only while bit 30 "produce" is set), on request or every
// period. A [Consumer] receives them (with a dictionary only while bit 31
// "consume" is set) and reports the time to a handler.
//
// Long-running services have Start, which subscribes before it returns,
// and Close, which stops them and waits for their goroutine. Callbacks run
// on the service's goroutine and must not block for long.
package timestamp
