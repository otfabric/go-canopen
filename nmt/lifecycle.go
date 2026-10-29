// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// sendTimeout bounds every frame a service sends on its own (boot-up,
// heartbeat, guard requests and responses).
const sendTimeout = time.Second

// lifecycle is the Start/Close state of a long-running service.
type lifecycle struct {
	mu     sync.Mutex
	state  int // 0 new, 1 running, 2 closed
	cancel context.CancelFunc
	done   chan struct{}
}

// start runs setup with the service context and, when it succeeds, the
// loop it returns in a new goroutine.
func (l *lifecycle) start(setup func(ctx context.Context) (func(ctx context.Context), error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state {
	case 1:
		return fmt.Errorf("%w: already started", canopen.ErrBusy)
	case 2:
		return canopen.ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop, err := setup(ctx)
	if err != nil {
		cancel()
		return err
	}
	l.cancel, l.done, l.state = cancel, make(chan struct{}), 1
	go func() {
		defer close(l.done)
		loop(ctx)
	}()
	return nil
}

// close stops the loop and waits for it; later calls do nothing.
func (l *lifecycle) close() {
	l.mu.Lock()
	st, cancel, done := l.state, l.cancel, l.done
	l.state = 2
	l.mu.Unlock()
	if st == 1 {
		cancel()
		<-done
	}
}

// closed reports whether Close was called.
func (l *lifecycle) closed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == 2
}

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
	onHeartbeat func(HeartbeatEvent)
	onGuard     func(GuardEvent)
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
	return c
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

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
// block for long.
func WithStateHandler(h func(StateChange)) Option { return func(c *config) { c.onState = h } }

// WithResetHandler calls h during an NMT reset of a [Slave], after the
// dictionary areas were restored (see [WithDictionary]) and before the
// boot-up message: the place to reload stored parameters or reinitialise
// the application. It must not call [Slave.Reset].
func WithResetHandler(h func(ResetKind)) Option { return func(c *config) { c.onReset = h } }

// WithHeartbeatHandler calls h for the events of a [HeartbeatConsumer],
// on its goroutine; h must not block for long.
func WithHeartbeatHandler(h func(HeartbeatEvent)) Option {
	return func(c *config) { c.onHeartbeat = h }
}

// WithGuardHandler calls h for the events of a [GuardMaster] or a
// [GuardResponder], on its goroutine; h must not block for long.
func WithGuardHandler(h func(GuardEvent)) Option { return func(c *config) { c.onGuard = h } }
