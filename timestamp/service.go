// SPDX-License-Identifier: MIT

package timestamp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// ErrNotProducer reports a TIME message the node may not send: bit 30
// (produce) of the COB-ID TIME (0x1012) is clear.
var ErrNotProducer = errors.New("timestamp: not a TIME producer (0x1012 bit 30 clear)")

// Option configures a [Producer] or a [Consumer]. Each option names the
// services it applies to; the others ignore it.
type Option func(*config)

type config struct {
	log     *slog.Logger
	dict    *od.Dictionary
	cob     canopen.COBID
	cobSet  bool
	period  time.Duration
	clock   func() time.Time
	handler func(Event)
}

func newConfig(opts []Option) config {
	c := config{log: discard, cob: canopen.COBID(canopen.FuncTIME), clock: time.Now}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.log == nil {
		c.log = discard
	}
	if c.clock == nil {
		c.clock = time.Now
	}
	return c
}

// WithLogger logs service events to l (both services). The default logs
// nothing.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithDictionary takes the COB-ID TIME from 0x1012 (which must exist) and
// follows its changes: a [Producer] sends only while bit 30 (produce) is
// set, a [Consumer] delivers only while bit 31 (consume) is set. The write
// hook it installs refuses a new CAN-ID while bit 30 or 31 is set, with
// [canopen.AbortInvalidValue].
func WithDictionary(d *od.Dictionary) Option { return func(c *config) { c.dict = d } }

// WithCOBID sets the COB-ID TIME (both services; default 0x100, produced
// and consumed). Flag bits are ignored without a dictionary.
func WithCOBID(cob canopen.COBID) Option { return func(c *config) { c.cob, c.cobSet = cob, true } }

// WithPeriod makes a started [Producer] send the clock's time every
// period (default 0: only on [Producer.Send]).
func WithPeriod(d time.Duration) Option { return func(c *config) { c.period = d } }

// WithClock sets the time source of a [Producer]'s periodic messages
// (default time.Now).
func WithClock(clock func() time.Time) Option { return func(c *config) { c.clock = clock } }

// WithHandler calls h for every TIME message a [Consumer] receives, on its
// goroutine; h must not block for long.
func WithHandler(h func(Event)) Option { return func(c *config) { c.handler = h } }

// installHook refuses CAN-ID changes of 0x1012 while it is in use; the
// returned function removes the hook.
func installHook(d *od.Dictionary) (remove func()) {
	return d.OnWrite(od.IndexCOBIDTime, 0, func(_ *od.Variable, data []byte) error {
		x, err := d.GetUint(od.IndexCOBIDTime, 0)
		if err != nil {
			return nil //nolint:nilerr // no current value: nothing to compare with
		}
		const use = canopen.COBGenerate | canopen.COBConsume
		cur, nw := canopen.COBID(x), canopen.COBID(canopen.DecodeUint(data))
		if cur&use != 0 && nw&use != 0 && (cur.ID() != nw.ID() || cur.Extended() != nw.Extended()) {
			return canopen.AbortInvalidValue
		}
		return nil
	})
}

// unhook removes the dictionary hook of a service once.
type unhook struct {
	mu sync.Mutex
	rm func()
}

func (u *unhook) set(rm func()) { u.mu.Lock(); u.rm = rm; u.mu.Unlock() }

func (u *unhook) remove() {
	u.mu.Lock()
	rm := u.rm
	u.rm = nil
	u.mu.Unlock()
	if rm != nil {
		rm()
	}
}

// Frame returns the TIME frame of a time on a COB-ID's identifier.
func Frame(cob canopen.COBID, t TimeOfDay) canbus.Frame {
	b := t.Bytes()
	var f canbus.Frame
	if cob.Extended() {
		f, _ = canbus.NewExtended(cob.ID(), b[:])
	} else {
		f, _ = canbus.NewStandard(cob.ID(), b[:])
	}
	return f
}

// ParseFrame decodes a TIME frame: a classical data frame (not remote,
// not CAN FD) with a 6-byte TIME_OF_DAY.
func ParseFrame(f *canbus.Frame) (TimeOfDay, error) {
	if f.Flags&(canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) != 0 {
		return TimeOfDay{}, fmt.Errorf("%w: not a TIME data frame", canopen.ErrProtocol)
	}
	n := min(int(f.Len), len(f.Data))
	t, err := ParseTimeOfDay(f.Data[:n])
	if err != nil {
		return TimeOfDay{}, fmt.Errorf("%w: %w", canopen.ErrProtocol, err)
	}
	return t, nil
}

// ProducerStats counts what a [Producer] did.
type ProducerStats struct {
	Sent   uint64 `json:"sent"`
	Errors uint64 `json:"errors"` // send failures
}

// Producer sends TIME messages: on request ([Producer.Send]) or, once
// started with a period, every period with the clock's time.
type Producer struct {
	bus canopen.Bus
	cfg config
	lc  lifecycle

	mu    sync.Mutex
	cob   canopen.COBID
	stats ProducerStats
	hooks unhook
}

// NewProducer returns a TIME producer. Options: [WithLogger],
// [WithDictionary], [WithCOBID], [WithPeriod], [WithClock].
func NewProducer(bus canopen.Bus, opts ...Option) (*Producer, error) {
	p := &Producer{bus: bus, cfg: newConfig(opts)}
	p.cob = p.cfg.cob | canopen.COBGenerate
	if d := p.cfg.dict; d != nil {
		x, err := d.GetUint(od.IndexCOBIDTime, 0)
		if err != nil {
			return nil, fmt.Errorf("timestamp: COB-ID TIME 0x1012: %w", err)
		}
		p.cob = canopen.COBID(x)
		if p.cfg.cobSet {
			p.cob = p.cfg.cob
		}
	} else if p.cfg.cobSet {
		p.cob = p.cfg.cob | canopen.COBGenerate
	}
	if p.cfg.period < 0 {
		return nil, fmt.Errorf("timestamp: negative period")
	}
	if d := p.cfg.dict; d != nil {
		p.hooks.set(installHook(d))
	}
	return p, nil
}

// Send sends a TIME message with t. It returns [ErrNotProducer] when the
// COB-ID's produce bit is clear.
func (p *Producer) Send(ctx context.Context, t time.Time) error {
	td, err := FromTime(t)
	if err != nil {
		return err
	}
	p.mu.Lock()
	cob := p.cob
	p.mu.Unlock()
	if cob&canopen.COBGenerate == 0 {
		return ErrNotProducer
	}
	f := Frame(cob, td)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err = p.bus.Send(sctx, &f)
	p.mu.Lock()
	if err != nil {
		p.stats.Errors++
	} else {
		p.stats.Sent++
	}
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("timestamp: send: %w", err)
	}
	return nil
}

// Start sends the time every period (when one is set); with a dictionary
// it follows writes of 0x1012 from then on.
func (p *Producer) Start() error {
	return p.lc.start(func(context.Context) (func(context.Context), error) {
		var unwatch func()
		if d := p.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				if v.Index == od.IndexCOBIDTime && v.SubIndex == 0 {
					p.mu.Lock()
					p.cob = canopen.COBID(canopen.DecodeUint(data))
					p.mu.Unlock()
				}
			})
		}
		return func(ctx context.Context) {
			if unwatch != nil {
				defer unwatch()
			}
			if p.cfg.period == 0 {
				<-ctx.Done()
				return
			}
			ticker := time.NewTicker(p.cfg.period)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					err := p.Send(ctx, p.cfg.clock())
					if err != nil && !errors.Is(err, ErrNotProducer) && ctx.Err() == nil {
						p.cfg.log.Warn("time send", "error", err)
					}
				}
			}
		}, nil
	})
}

// Close stops the periodic messages and waits for the goroutine. It
// removes the write hook the producer installed on the dictionary.
func (p *Producer) Close() error {
	p.lc.close()
	p.hooks.remove()
	return nil
}

// Stats returns the counters.
func (p *Producer) Stats() ProducerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Event is a TIME message a [Consumer] received.
type Event struct {
	TimeOfDay
	// Time is the time the message carries, in UTC.
	Time time.Time
	// Received is the frame's receive timestamp, or the time it was
	// processed: Time - Received estimates the local clock's offset.
	Received time.Time
}

// ConsumerStats counts what a [Consumer] did.
type ConsumerStats struct {
	Received uint64 `json:"received"`
	Ignored  uint64 `json:"ignored"` // messages while the consume bit is clear
	Invalid  uint64 `json:"invalid"` // malformed frames
}

// Consumer receives TIME messages and reports them to a handler.
type Consumer struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	mu    sync.Mutex
	cob   canopen.COBID
	last  Event
	stats ConsumerStats
	hooks unhook
}

// NewConsumer returns a TIME consumer. Options: [WithLogger],
// [WithHandler], [WithDictionary], [WithCOBID].
func NewConsumer(bus canopen.Bus, opts ...Option) (*Consumer, error) {
	c := &Consumer{bus: bus, cfg: newConfig(opts), wake: make(chan struct{}, 1)}
	c.cob = c.cfg.cob | canopen.COBConsume
	if d := c.cfg.dict; d != nil {
		x, err := d.GetUint(od.IndexCOBIDTime, 0)
		if err != nil {
			return nil, fmt.Errorf("timestamp: COB-ID TIME 0x1012: %w", err)
		}
		c.cob = canopen.COBID(x)
		c.hooks.set(installHook(d))
		if c.cfg.cobSet {
			c.cob = c.cfg.cob
		}
	} else if c.cfg.cobSet {
		c.cob = c.cfg.cob | canopen.COBConsume
	}
	return c, nil
}

func (c *Consumer) subscribe() (*canbus.Subscription, canopen.COBID, error) {
	c.mu.Lock()
	cob := c.cob
	c.mu.Unlock()
	flt := cob.Filter()
	flt.Remote = canbus.RemoteDataOnly
	sub, err := c.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{flt}}))
	if err != nil {
		return nil, 0, fmt.Errorf("timestamp: subscribe: %w", err)
	}
	return sub, cob, nil
}

// Start subscribes to the COB-ID TIME and starts delivering; with a
// dictionary it follows writes of 0x1012 from then on (a new CAN-ID
// re-subscribes).
func (c *Consumer) Start() error {
	return c.lc.start(func(context.Context) (func(context.Context), error) {
		sub, cob, err := c.subscribe()
		if err != nil {
			return nil, err
		}
		var unwatch func()
		if d := c.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				if v.Index != od.IndexCOBIDTime || v.SubIndex != 0 {
					return
				}
				c.mu.Lock()
				c.cob = canopen.COBID(canopen.DecodeUint(data))
				c.mu.Unlock()
				select {
				case c.wake <- struct{}{}:
				default:
				}
			})
		}
		return func(ctx context.Context) {
			if unwatch != nil {
				defer unwatch()
			}
			c.run(ctx, sub, cob)
		}, nil
	})
}

func (c *Consumer) run(ctx context.Context, sub *canbus.Subscription, cob canopen.COBID) {
	defer func() { _ = sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case <-c.wake:
			c.mu.Lock()
			ncob := c.cob
			c.mu.Unlock()
			if ncob.ID() == cob.ID() && ncob.Extended() == cob.Extended() {
				continue
			}
			nsub, _, err := c.subscribe()
			if err != nil {
				c.cfg.log.Warn("time re-subscribe", "error", err)
				return
			}
			_ = sub.Close()
			sub, cob = nsub, ncob
		case f := <-sub.Frames():
			if e, ok := c.receive(&f); ok {
				if h := c.cfg.handler; h != nil {
					h(e)
				}
			}
		}
	}
}

// receive processes one frame; ok reports an event to deliver.
func (c *Consumer) receive(f *canbus.Frame) (Event, bool) {
	td, err := ParseFrame(f)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.cob.Matches(f):
		return Event{}, false
	case err != nil:
		c.stats.Invalid++
		return Event{}, false
	case c.cob&canopen.COBConsume == 0:
		c.stats.Ignored++
		return Event{}, false
	}
	r := f.Timestamp
	if r.IsZero() {
		r = time.Now()
	}
	e := Event{TimeOfDay: td, Time: td.Time(), Received: r}
	c.stats.Received++
	c.last = e
	return e, true
}

// Last returns the last TIME message received; ok is false before the
// first one.
func (c *Consumer) Last() (e Event, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, !c.last.Time.IsZero()
}

// Close stops the consumer and waits for it. It removes the write hook
// the consumer installed on the dictionary.
func (c *Consumer) Close() error {
	c.lc.close()
	c.hooks.remove()
	return nil
}

// Stats returns the counters.
func (c *Consumer) Stats() ConsumerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
