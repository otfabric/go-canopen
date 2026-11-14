// SPDX-License-Identifier: MIT

// Package goid returns the identifier of the calling goroutine.
//
// A service runs application code (a handler, a dictionary hook) on a
// goroutine of its own and waits for that goroutine in Close. A Close
// called from such a handler would wait for itself; with the identifier
// Close can tell that it runs on the goroutine it would wait for. It is
// used once per Close and once when the goroutine starts, never on a hot
// path.
package goid

import (
	"bytes"
	"runtime"
	"strconv"
)

// ID returns the identifier of the calling goroutine, 0 if the runtime
// does not report it.
func ID() uint64 {
	var buf [64]byte
	return parse(buf[:runtime.Stack(buf[:], false)])
}

// parse reads the identifier from the first line of a goroutine's stack:
// "goroutine 12 [running]:".
func parse(stack []byte) uint64 {
	rest, ok := bytes.CutPrefix(stack, []byte("goroutine "))
	if !ok {
		return 0
	}
	n := bytes.IndexByte(rest, ' ')
	if n < 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:n]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
