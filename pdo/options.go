// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Errors of the PDO services.
var (
	// ErrDisabled reports a transmission while the service is disabled
	// (the node is not NMT operational).
	ErrDisabled = errors.New("pdo: PDO disabled (node not operational)")
	// ErrInactive reports a transmission of a PDO that is not active: its
	// COB-ID is invalid or nothing is mapped.
	ErrInactive = errors.New("pdo: PDO not active (invalid COB-ID or empty mapping)")
	// ErrNoRTR reports a remote transmission request for a CANopen FD PDO:
	// CAN FD has no remote frames (CiA 1301).
	ErrNoRTR = errors.New("pdo: no remote frames in CANopen FD")
)

// EMCY error codes the PDO services report (CiA 301 table 21).
const (
	EMCYPDOLength         uint16 = 0x8210 // PDO not processed due to length error
	EMCYPDOLengthExceeded uint16 = 0x8220 // PDO length exceeded
	EMCYRPDOTimeout       uint16 = 0x8250 // RPDO timeout (deadline monitoring)
)

// ErrorEvent reports a receive error of an RPDO, to be turned into an
// EMCY by the node. Errors are reported once when they appear (Active
// true) and once when they disappear (Active false): when an RPDO of the
// right length arrives again, or when the deadline is met again.
type ErrorEvent struct {
	// PDO is the RPDO number.
	PDO int
	// Code is the EMCY error code: [EMCYPDOLength],
	// [EMCYPDOLengthExceeded] or [EMCYRPDOTimeout].
	Code uint16
	// Active is true when the error appears, false when it is resolved.
	Active bool
	// Err describes the error.
	Err error
}

// Option configures a PDO service.
type Option func(*options)

type options struct {
	log     *slog.Logger
	onError func(ErrorEvent)
	onRx    func(data []byte)
	fd, brs bool
	clock   func() time.Time
}

// WithLogger sets the logger (default: discard).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.log = l
		}
	}
}

// WithErrorHandler sets the function an [RPDO] reports receive errors to.
// It is called from the service goroutine.
func WithErrorHandler(f func(ErrorEvent)) Option {
	return func(o *options) { o.onError = f }
}

// WithReceiveHandler sets a function an [RPDO] calls after it has written
// the data of a received PDO to the dictionary (for synchronous types, on
// the SYNC that applies it), with the PDO data. It is called from the
// goroutine that wrote the dictionary: the service goroutine, or the
// caller of [RPDO.OnSync].
func WithReceiveHandler(f func(data []byte)) Option {
	return func(o *options) { o.onRx = f }
}

// WithFD makes the PDO a CANopen FD PDO (CiA 1301): it is sent in CAN FD
// frames (with bit rate switching when brs is true), padded with zeros to
// the next valid CAN FD length, and its mapping may total up to
// [MaxBitsFD] bits (64 bytes). CiA 1301 runs every service of a CANopen FD
// network in CAN FD frames and has no per-PDO FD flag in the dictionary,
// so the choice is an option, normally the same for all PDOs of a device.
// Remote frames do not exist in CAN FD: an FD TPDO answers no remote
// request (types 252 and 253 therefore never transmit), and
// [Listener.Request] fails with [ErrNoRTR]. An FD RPDO also accepts
// classical frames; its length check allows the CAN FD padding (a mapping
// of 9 bytes arrives in a 12-byte frame). Applies to [TPDO], [RPDO],
// [NewDecoder], [NewEncoder] (and so [Sender] and [Listener]) and
// [ProtectFD]-style validation of the services.
func WithFD(brs bool) Option {
	return func(o *options) { o.fd, o.brs = true, brs }
}

// WithClock sets the clock the SYNC window (0x1007) is measured with
// (default time.Now). It exists for tests; inhibit and event timers use
// the system clock.
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now != nil {
			o.clock = now
		}
	}
}

func buildOptions(opts []Option) options {
	o := options{log: slog.New(discard{}), clock: time.Now}
	for _, f := range opts {
		f(&o)
	}
	return o
}

// maxBits returns the mapping limit: [MaxBits] or, for CANopen FD,
// [MaxBitsFD].
func (o *options) maxBits() int {
	if o.fd {
		return MaxBitsFD
	}
	return MaxBits
}

// frame returns the data frame of a PDO: classical, or CAN FD padded to a
// valid CAN FD length.
func (o *options) frame(c canopen.COBID, data []byte) (canbus.Frame, error) {
	if o.fd {
		return canbus.NewFD(c.ID(), c.Extended(), o.brs, canbus.PadFD(data, 0))
	}
	return newFrame(c, data)
}

// discard is a slog.Handler that drops everything.
type discard struct{}

func (discard) Enabled(context.Context, slog.Level) bool  { return false }
func (discard) Handle(context.Context, slog.Record) error { return nil }
func (d discard) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discard) WithGroup(string) slog.Handler           { return d }

// newFrame returns a data frame for a COB-ID.
func newFrame(c canopen.COBID, data []byte) (canbus.Frame, error) {
	if c.Extended() {
		return canbus.NewExtended(c.ID(), data)
	}
	return canbus.NewStandard(c.ID(), data)
}

// filter returns the receive filter of a COB-ID for data or remote
// frames.
func filter(c canopen.COBID, remote canbus.RemoteMatch) canbus.FilterSet {
	f := c.Filter()
	f.Remote = remote
	return canbus.FilterSet{Filters: []canbus.Filter{f}}
}

// subKey identifies the subscription a service needs: none (zero) or
// one for a COB-ID.
type subKey struct {
	on  bool
	cob canopen.COBID
}

func keyOf(on bool, c canopen.COBID) subKey {
	if !on {
		return subKey{}
	}
	return subKey{on: true, cob: canopen.NewCOBID(c.ID()) | c&canopen.COBExtended}
}

func (k subKey) String() string {
	if !k.on {
		return "none"
	}
	return fmt.Sprint(k.cob)
}
