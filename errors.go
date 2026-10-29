// SPDX-License-Identifier: MIT

package canopen

import "errors"

// Sentinel errors. They are usually wrapped with detail; test with
// errors.Is. SDO aborts are [AbortCode] values (errors.As).
var (
	// ErrInvalidNodeID reports a node-ID outside 1..127.
	ErrInvalidNodeID = errors.New("canopen: invalid node-ID")

	// ErrTimeout reports a protocol timeout (an SDO response, a heartbeat,
	// a boot-up message that did not arrive in time). A context deadline is
	// reported as the context's error instead.
	ErrTimeout = errors.New("canopen: timeout")

	// ErrClosed reports an operation on a closed service.
	ErrClosed = errors.New("canopen: closed")

	// ErrDataType reports a value that does not fit its CANopen data type.
	ErrDataType = errors.New("canopen: data type mismatch")

	// ErrProtocol reports a message that violates the protocol (an
	// unexpected SDO command specifier, a wrong toggle bit, a short frame).
	ErrProtocol = errors.New("canopen: protocol error")

	// ErrBusy reports a service that is already running a transfer.
	ErrBusy = errors.New("canopen: busy")
)
