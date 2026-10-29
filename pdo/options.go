// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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

func buildOptions(opts []Option) options {
	o := options{log: slog.New(discard{})}
	for _, f := range opts {
		f(&o)
	}
	return o
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
