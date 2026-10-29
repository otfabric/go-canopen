// SPDX-License-Identifier: MIT

package master

import (
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
)

// BootStatus is the result of the boot slave process of CiA 302-2 for a
// node: 0 (BootOK) or one of the error status letters 'A'..'O' (CiA
// 302-2; the meanings are those Lely lely-core reports).
type BootStatus byte

// Boot slave process results.
const (
	// BootOK: the node booted and its error control started.
	BootOK BootStatus = 0
	// BootNotListed (A): the node is not in the network list (0x1F81
	// bit 0).
	BootNotListed BootStatus = 'A'
	// BootNoResponse (B): no answer to the upload of 0x1000.
	BootNoResponse BootStatus = 'B'
	// BootDeviceType (C): 0x1000 differs from 0x1F84.
	BootDeviceType BootStatus = 'C'
	// BootVendorID (D): 0x1018 sub-index 1 differs from 0x1F85.
	BootVendorID BootStatus = 'D'
	// BootHeartbeat (E): no heartbeat while checking a keep-alive node.
	BootHeartbeat BootStatus = 'E'
	// BootGuarding (F): no node guarding response while checking a
	// keep-alive node.
	BootGuarding BootStatus = 'F'
	// BootProgramObjects (G): the program download objects (0x1F55 of
	// the manager, 0x1F56 of the node) are not configured or not
	// readable.
	BootProgramObjects BootStatus = 'G'
	// BootUpdateNotAllowed (H): the software must be updated but the
	// assignment does not allow it.
	BootUpdateNotAllowed BootStatus = 'H'
	// BootUpdateFailed (I): the software must be updated but the program
	// download failed.
	BootUpdateFailed BootStatus = 'I'
	// BootConfiguration (J): the configuration download failed.
	BootConfiguration BootStatus = 'J'
	// BootErrorControl (K): no heartbeat while starting the error
	// control.
	BootErrorControl BootStatus = 'K'
	// BootOperational (L): the node was found operational (keep-alive):
	// it was not checked nor configured, and keeps running. Not a
	// failure.
	BootOperational BootStatus = 'L'
	// BootProductCode (M): 0x1018 sub-index 2 differs from 0x1F86.
	BootProductCode BootStatus = 'M'
	// BootRevision (N): 0x1018 sub-index 3 differs from 0x1F87.
	BootRevision BootStatus = 'N'
	// BootSerialNumber (O): 0x1018 sub-index 4 differs from 0x1F88.
	BootSerialNumber BootStatus = 'O'
)

var bootText = map[BootStatus]string{
	BootOK:               "booted",
	BootNotListed:        "not in the network list (0x1F81)",
	BootNoResponse:       "no response to the upload of 0x1000",
	BootDeviceType:       "device type 0x1000 differs from 0x1F84",
	BootVendorID:         "vendor-ID 0x1018:01 differs from 0x1F85",
	BootHeartbeat:        "no heartbeat received",
	BootGuarding:         "no node guarding response received",
	BootProgramObjects:   "program download objects not configured or inconsistent",
	BootUpdateNotAllowed: "software update required but not allowed",
	BootUpdateFailed:     "software update required but the program download failed",
	BootConfiguration:    "configuration download failed",
	BootErrorControl:     "no heartbeat received when starting the error control",
	BootOperational:      "node was initially operational",
	BootProductCode:      "product code 0x1018:02 differs from 0x1F86",
	BootRevision:         "revision number 0x1018:03 differs from 0x1F87",
	BootSerialNumber:     "serial number 0x1018:04 differs from 0x1F88",
}

// OK reports a successful boot: BootOK or BootOperational.
func (s BootStatus) OK() bool { return s == BootOK || s == BootOperational }

// String returns the letter and its meaning ("0: booted" for BootOK).
func (s BootStatus) String() string {
	t, ok := bootText[s]
	if !ok {
		return fmt.Sprintf("BootStatus(%d)", byte(s))
	}
	if s == BootOK {
		return "0: " + t
	}
	return fmt.Sprintf("%c: %s", byte(s), t)
}

// BootError is the failure of the boot slave process of a node. It
// unwraps to the cause (an SDO error, [ErrIdentity], a flash status...).
type BootError struct {
	Node   canopen.NodeID
	Status BootStatus
	Err    error
}

// Error implements error.
func (e *BootError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("master: boot slave %d: %s: %v", e.Node, e.Status, e.Err)
	}
	return fmt.Sprintf("master: boot slave %d: %s", e.Node, e.Status)
}

// Unwrap returns the cause.
func (e *BootError) Unwrap() error { return e.Err }

// BootTiming are the times of the CiA 302 manager's boot-up. The defaults
// are those of Lely lely-core (LELY_CO_NMT_BOOT_* and
// LELY_CO_NMT_CFG_RESET_TIMEOUT).
type BootTiming struct {
	// RetryDelay is the wait before a boot slave process is retried
	// after a node did not answer (status B); default 1 s.
	RetryDelay time.Duration
	// SDORetries is the number of times an SDO request that timed out
	// is repeated where a node may still be resetting (the first reads,
	// program control); default 3.
	SDORetries int
	// ResetTimeout bounds the wait for a node's boot-up message after a
	// reset the manager sent; default 1 s.
	ResetTimeout time.Duration
	// GuardTimeout bounds the wait for a node guarding response when
	// checking a keep-alive node without heartbeat; default 100 ms.
	GuardTimeout time.Duration
	// PollInterval is the period of the flash status and program control
	// polling of a program download; default 100 ms.
	PollInterval time.Duration
	// ProgramTimeout bounds a program download (from the stop command
	// to the program started); default 60 s.
	ProgramTimeout time.Duration
}

// withDefaults fills the zero fields.
func (t BootTiming) withDefaults() BootTiming {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&t.RetryDelay, time.Second)
	def(&t.ResetTimeout, time.Second)
	def(&t.GuardTimeout, 100*time.Millisecond)
	def(&t.PollInterval, 100*time.Millisecond)
	def(&t.ProgramTimeout, time.Minute)
	if t.SDORetries <= 0 {
		t.SDORetries = 3
	}
	return t
}
