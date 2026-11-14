// SPDX-License-Identifier: MIT

package nmt

import (
	"io"
	"log/slog"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/callback"
	"github.com/otfabric/go-canopen/od"
)

// sendTimeout bounds every frame a service sends on its own (boot-up,
// heartbeat, guard requests and responses).
const sendTimeout = time.Second

// Option configures the services of this package. Each option names the
// services it applies to; the others ignore it.
type Option func(*config)

type config struct {
	log         *slog.Logger
	dict        *od.Dictionary
	period      time.Duration
	periodSet   bool
	guardTime   time.Duration
	lifeFactor  uint8
	guardSet    bool
	onState     func(StateChange)
	onReset     func(ResetKind)
	onBootUp    func()
	onHeartbeat func(HeartbeatEvent)
	onGuard     func(GuardEvent)
	fd, brs     bool
}

// frame returns f as the service sends it: classical, or with [WithFD] a
// CAN FD frame.
func (c *config) frame(f canbus.Frame) canbus.Frame {
	if c.fd {
		return ToFD(f, c.brs)
	}
	return f
}

func newConfig(opts []Option) config {
	c := config{log: discard}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.log == nil {
		c.log = discard
	}
	c.log = callback.Logger(c.log)
	return c
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// stateOf calls a state source, which is the application's code: one
// that does not return normally is logged and counts as Unknown.
func stateOf(log *slog.Logger, state func() canopen.NMTState) canopen.NMTState {
	st := canopen.StateUnknown
	callback.Do(log, "state source", func() { st = state() })
	return st
}

// WithLogger logs service events to l (all services). The default logs
// nothing.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithDictionary takes the service's parameters from an object dictionary
// and follows their changes:
//   - [Slave]: an NMT reset restores the dictionary's default values
//     (reset node: 0x2000..0x9FFF, then 0x1000..0x1FFF; reset
//     communication: 0x1000..0x1FFF) and applies a pending node-ID;
//   - [HeartbeatProducer]: the period is the producer heartbeat time
//     0x1017 (ms);
//   - [HeartbeatConsumer]: the supervised nodes are the entries of the
//     consumer heartbeat time 0x1016 (node-ID in bits 16..23, time in ms
//     in bits 0..15), and writes that would supervise one node twice are
//     refused with [canopen.AbortParamIncompatible];
//   - [GuardResponder]: the life guarding parameters are the guard time
//     0x100C (ms) and the life time factor 0x100D.
func WithDictionary(d *od.Dictionary) Option { return func(c *config) { c.dict = d } }

// WithHeartbeatPeriod sets the period of a [HeartbeatProducer]; 0 (the
// default without a dictionary) produces no heartbeat. It overrides the
// dictionary's 0x1017 until that object is written.
func WithHeartbeatPeriod(d time.Duration) Option {
	return func(c *config) { c.period, c.periodSet = d, true }
}

// WithLifeGuarding sets the guard time and life time factor a
// [GuardResponder] expects from the guarding master; life guarding is off
// when either is 0 (the default without a dictionary).
func WithLifeGuarding(guardTime time.Duration, lifeTimeFactor uint8) Option {
	return func(c *config) { c.guardTime, c.lifeFactor, c.guardSet = guardTime, lifeTimeFactor, true }
}

// WithStateHandler calls h for every NMT state change of a [Slave]. It
// runs on the goroutine that caused the change (the slave's for NMT
// commands, the caller's for Start, SetState and Reset) and must not
// block for long. Two changes made by different goroutines call it
// concurrently, in either order: read [Slave.State] for the state that
// holds. It may call the slave's methods, [Slave.Close] included (see
// there). A handler that does not return normally is logged and the
// slave goes on, as for every handler of this package.
func WithStateHandler(h func(StateChange)) Option { return func(c *config) { c.onState = h } }

// WithResetHandler calls h during an NMT reset of a [Slave], after the
// dictionary areas were restored (see [WithDictionary]) and before the
// boot-up message: the place to reload stored parameters or reinitialise
// the application. It runs on the goroutine that does the reset (the
// slave's for an NMT command, the caller's for [Slave.Reset]), may call
// [Slave.Close] and must not call [Slave.Reset].
func WithResetHandler(h func(ResetKind)) Option { return func(c *config) { c.onReset = h } }

// WithBootUpHandler calls h just before a [Slave] sends its boot-up
// message (at Start and after every reset), on the goroutine that sends
// it: the moment the node stops being Initialising, for services that
// must not act on frames received earlier. It must not block. It may call
// [Slave.Close]: the boot-up message is then not sent.
func WithBootUpHandler(h func()) Option { return func(c *config) { c.onBootUp = h } }

// WithHeartbeatHandler calls h for the events of a [HeartbeatConsumer],
// on its goroutine, one at a time; h must not block for long: while it
// runs no heartbeat is processed. It may call the consumer's methods,
// and [HeartbeatConsumer.Close], which then does not wait for the
// goroutine the handler runs on.
func WithHeartbeatHandler(h func(HeartbeatEvent)) Option {
	return func(c *config) { c.onHeartbeat = h }
}

// WithFD selects CANopen FD (CiA 1301), which carries every protocol in
// CAN FD frames (with bit rate switching when brs is true): a [Master]
// sends its commands, a [Slave] its boot-up message and a
// [HeartbeatProducer] its heartbeats in CAN FD frames, and a [Slave] and a
// [HeartbeatConsumer] accept CAN FD frames as well as classical ones
// (without the option they ignore CAN FD frames). The NMT protocols
// themselves are unchanged. Node guarding uses remote frames, which CAN FD
// does not have: [GuardMaster] and [GuardResponder] ignore the option and
// a CANopen FD device supervises with heartbeats only.
func WithFD(brs bool) Option { return func(c *config) { c.fd, c.brs = true, brs } }

// WithGuardHandler calls h for the events of a [GuardMaster] or a
// [GuardResponder], on its goroutine, one at a time; h must not block
// for long. It may call the service's methods, and its Close, which then
// does not wait for the goroutine the handler runs on.
func WithGuardHandler(h func(GuardEvent)) Option { return func(c *config) { c.onGuard = h } }
