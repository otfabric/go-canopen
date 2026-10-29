// SPDX-License-Identifier: MIT

package mpdo

import (
	"context"
	"errors"
	"log/slog"

	"github.com/otfabric/go-canopen/pdo"
)

// Errors of the MPDO services.
var (
	// ErrDisabled reports a transmission while the producer is disabled
	// (the node is not NMT operational).
	ErrDisabled = errors.New("mpdo: MPDO disabled (node not operational)")
	// ErrInactive reports a transmission on a TPDO that is not an active
	// MPDO: invalid COB-ID, mapping sub-index 00h not 0xFE/0xFF, or a
	// transmission type other than 254/255.
	ErrInactive = errors.New("mpdo: TPDO is not an active MPDO")
	// ErrMode reports a transmission in the other address mode than the
	// TPDO is configured for.
	ErrMode = errors.New("mpdo: wrong address mode")
	// ErrNotScanned reports a SAM-MPDO of an object the object scanner
	// lists do not allow. It is returned together with
	// [canopen.AbortNotMappable] (Lely CO_SDO_AC_NO_PDO).
	ErrNotScanned = errors.New("mpdo: object not in the object scanner list")
	// ErrNotStarted reports a transmission before Start.
	ErrNotStarted = errors.New("mpdo: not started")
)

// EMCYDAMNoObject is the EMCY error code of a DAM-MPDO for an object the
// consumer lacks (CiA 301 table 21: "DAM MPDO not processed, destination
// object not available").
const EMCYDAMNoObject uint16 = 0x8230

// Option configures a [Producer] or a [Consumer].
type Option func(*options)

type options struct {
	log     *slog.Logger
	onError func(pdo.ErrorEvent)
	onRx    func(Event)
}

// WithLogger sets the logger (default: discard).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.log = l
		}
	}
}

// WithErrorHandler sets the function a [Consumer] reports the emergencies
// 0x8230 ([EMCYDAMNoObject]) and 0x8210 (pdo.EMCYPDOLength) to, once when
// one appears (Active true) and once when it is resolved. The PDO field is
// the RPDO number. It is called from the service goroutine.
func WithErrorHandler(f func(pdo.ErrorEvent)) Option {
	return func(o *options) { o.onError = f }
}

// WithReceiveHandler sets a function a [Consumer] calls, from its
// goroutine, after it has written a received value to the dictionary.
func WithReceiveHandler(f func(Event)) Option {
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
