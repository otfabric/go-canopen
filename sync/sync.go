// SPDX-License-Identifier: MIT

package syncobj

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// MaxOverflow is the largest synchronous counter overflow value (0x1019).
const MaxOverflow = 240

// Option configures a [Producer] or a [Consumer]. Each option names the
// services it applies to; the others ignore it.
type Option func(*config)

type config struct {
	log         *slog.Logger
	dict        *od.Dictionary
	cob         canopen.COBID
	cobSet      bool
	period      time.Duration
	periodSet   bool
	overflow    uint8
	overflowSet bool
	window      time.Duration
	windowSet   bool
	handler     func(Event)
	fd, brs     bool
}

func newConfig(opts []Option) config {
	c := config{log: discard, cob: canopen.COBID(canopen.FuncSYNC)}
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

// WithLogger logs service events to l (both services). The default logs
// nothing.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithDictionary takes the parameters from a node's object dictionary and
// follows their changes: the COB-ID SYNC 0x1005 (bit 30: this node
// generates SYNC), the communication cycle period 0x1006 (µs), the
// synchronous window length 0x1007 (µs, consumer) and the synchronous
// counter overflow value 0x1019. It installs the write hooks CiA 301
// requires: 0x1019 cannot change while the period is not 0
// ([canopen.AbortDeviceState]) and accepts 0 and 2..240 only
// ([canopen.AbortInvalidValue]); the CAN-ID of 0x1005 cannot change while
// the node generates SYNC ([canopen.AbortDeviceState]). Values wider than
// their object (possible only with a dictionary that declares them wider)
// are refused: a COB-ID write with [canopen.AbortInvalidValue], at
// construction an error (unless an option overrides the value), and a
// later local change is ignored, as is a local change of 0x1019 to a value
// other than 0 and 2..240.
func WithDictionary(d *od.Dictionary) Option { return func(c *config) { c.dict = d } }

// WithCOBID sets the SYNC COB-ID (both services; default 0x080). Flag bits
// are ignored.
func WithCOBID(cob canopen.COBID) Option { return func(c *config) { c.cob, c.cobSet = cob, true } }

// WithPeriod sets the communication cycle period of a [Producer] (default
// 0: no SYNC, or 0x1006 with a dictionary).
func WithPeriod(d time.Duration) Option { return func(c *config) { c.period, c.periodSet = d, true } }

// WithOverflow sets the synchronous counter overflow value: 0 (the
// default) sends SYNC without data, 2..240 adds a counter byte running
// from 1 to the value. A [Consumer] given an overflow value checks the
// length of received SYNC messages.
func WithOverflow(n uint8) Option { return func(c *config) { c.overflow, c.overflowSet = n, true } }

// WithWindow sets the synchronous window length of a [Consumer] (default
// 0: no window, or 0x1007 with a dictionary).
func WithWindow(d time.Duration) Option { return func(c *config) { c.window, c.windowSet = d, true } }

// WithHandler calls h for every SYNC a [Consumer] receives, on its
// goroutine; h must not block for long (synchronous PDOs are due).
func WithHandler(h func(Event)) Option { return func(c *config) { c.handler = h } }

// WithFD selects CANopen FD (CiA 1301), which carries every protocol in
// CAN FD frames: a [Producer] sends SYNC as a CAN FD frame (with bit rate
// switching when brs is true) of the same 0 or 1 data byte, and a
// [Consumer] accepts SYNC in CAN FD frames as well as classical ones
// (without the option it ignores CAN FD frames). The SYNC protocol itself
// is unchanged in CANopen FD (CiA's CANopen FD poster: SYNC with an
// optional 1-byte counter on CAN-ID 080h).
func WithFD(brs bool) Option { return func(c *config) { c.fd, c.brs = true, brs } }

func checkOverflow(n uint64) error {
	if n == 1 || n > MaxOverflow {
		return fmt.Errorf("syncobj: counter overflow %d not in 0, 2..%d", n, MaxOverflow)
	}
	return nil
}

// overflowValue converts a counter overflow value read as an unsigned
// integer; ok is false for a value other than 0 and 2..240.
func overflowValue(x uint64) (uint8, bool) {
	if x > MaxOverflow {
		return 0, false
	}
	if x == 1 {
		return 0, false
	}
	return uint8(x), true
}

// cobID converts a COB-ID read as an unsigned integer; ok is false for a
// value wider than the UNSIGNED32 of a COB-ID.
func cobID(x uint64) (canopen.COBID, bool) {
	if x > math.MaxUint32 {
		return 0, false
	}
	return canopen.COBID(x), true
}

// micros converts a UNSIGNED32 µs value (0x1006, 0x1007) read as an
// unsigned integer; ok is false for a value wider than 32 bits.
func micros(x uint64) (time.Duration, bool) {
	if x > math.MaxUint32 {
		return 0, false
	}
	return time.Duration(x) * time.Microsecond, true
}

// errRange reports a dictionary value that does not fit its object.
func errRange(what string, x uint64) error {
	return fmt.Errorf("syncobj: %s value %d out of range: %w", what, x, canopen.AbortInvalidValue)
}

// installHooks registers the CiA 301 write checks of 0x1005 and 0x1019;
// the returned function removes them.
func installHooks(d *od.Dictionary) (remove func()) {
	rm1 := d.OnWrite(od.IndexSyncOverflow, 0, func(_ *od.Variable, data []byte) error {
		if p, err := d.GetUint(od.IndexCyclePeriod, 0); err == nil && p != 0 {
			return canopen.AbortDeviceState
		}
		if checkOverflow(canopen.DecodeUint(data)) != nil {
			return canopen.AbortInvalidValue
		}
		return nil
	})
	rm2 := d.OnWrite(od.IndexCOBIDSync, 0, func(_ *od.Variable, data []byte) error {
		nw, ok := cobID(canopen.DecodeUint(data))
		if !ok {
			return canopen.AbortInvalidValue
		}
		x, err := d.GetUint(od.IndexCOBIDSync, 0)
		if err != nil {
			return nil //nolint:nilerr // no current value: nothing to compare with
		}
		cur, ok := cobID(x)
		if !ok {
			return nil // no usable current value: nothing to compare with
		}
		if cur&canopen.COBGenerate != 0 && nw&canopen.COBGenerate != 0 && (cur.ID() != nw.ID() || cur.Extended() != nw.Extended()) {
			return canopen.AbortDeviceState
		}
		return nil
	})
	return func() { rm1(); rm2() }
}

// unhook removes the dictionary hooks of a service once.
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

// frame returns the SYNC frame on cob with the counter (none when 0), a
// CAN FD frame when fd is set.
func frame(cob canopen.COBID, counter uint8, fd, brs bool) canbus.Frame {
	var data []byte
	if counter != 0 {
		data = []byte{counter}
	}
	var f canbus.Frame
	if fd {
		f, _ = canbus.NewFD(cob.ID(), cob.Extended(), brs, data)
	} else if cob.Extended() {
		f, _ = canbus.NewExtended(cob.ID(), data)
	} else {
		f, _ = canbus.NewStandard(cob.ID(), data)
	}
	return f
}

// ProducerStats counts what a [Producer] did.
type ProducerStats struct {
	Sent   uint64 `json:"sent"`
	Errors uint64 `json:"errors"` // send failures
}

// Producer is the SYNC producer (CiA 301 7.2.5): every communication cycle
// period it sends the SYNC message, with a counter byte when the counter
// overflow value is not 0. A new period takes effect at once and restarts
// the counter at 1.
//
// With a dictionary it produces only while bit 30 (generate) of 0x1005 is
// set. Periods are kept by a time.Ticker: SYNC does not drift, but each
// message may be late by the scheduling latency of the Go runtime and the
// operating system (typically tens to hundreds of microseconds, more on a
// loaded system), so a Go SYNC producer suits cycle periods of
// milliseconds rather than tight motion control loops.
type Producer struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	mu       sync.Mutex
	cob      canopen.COBID
	generate bool
	period   time.Duration
	overflow uint8
	counter  uint8
	stats    ProducerStats
	hooks    unhook
}

// NewProducer returns a SYNC producer. Options: [WithLogger],
// [WithDictionary], [WithCOBID], [WithPeriod], [WithOverflow], [WithFD].
func NewProducer(bus canopen.Bus, opts ...Option) (*Producer, error) {
	p := &Producer{bus: bus, cfg: newConfig(opts), wake: make(chan struct{}, 1), generate: true}
	p.cob = p.cfg.cob
	if d := p.cfg.dict; d != nil {
		cob, err := d.GetUint(od.IndexCOBIDSync, 0)
		if err != nil {
			return nil, fmt.Errorf("syncobj: COB-ID SYNC 0x1005: %w", err)
		}
		period, err := d.GetUint(od.IndexCyclePeriod, 0)
		if err != nil {
			return nil, fmt.Errorf("syncobj: cycle period 0x1006: %w", err)
		}
		ovf, err := d.GetUint(od.IndexSyncOverflow, 0)
		if err != nil {
			return nil, fmt.Errorf("syncobj: counter overflow 0x1019: %w", err)
		}
		c, ok := cobID(cob)
		if !ok {
			return nil, errRange("COB-ID SYNC 0x1005", cob)
		}
		if p.period, ok = micros(period); !ok && !p.cfg.periodSet {
			return nil, errRange("cycle period 0x1006", period)
		}
		if p.overflow, ok = overflowValue(ovf); !ok && !p.cfg.overflowSet {
			return nil, checkOverflow(ovf)
		}
		p.cob, p.generate = c, c&canopen.COBGenerate != 0
	}
	if p.cfg.cobSet {
		p.cob = p.cfg.cob
	}
	if p.cfg.periodSet {
		p.period = p.cfg.period
	}
	if p.cfg.overflowSet {
		p.overflow = p.cfg.overflow
	}
	if p.period < 0 {
		return nil, fmt.Errorf("syncobj: negative period")
	}
	if err := checkOverflow(uint64(p.overflow)); err != nil {
		return nil, err
	}
	p.counter = 1
	if d := p.cfg.dict; d != nil {
		p.hooks.set(installHooks(d))
	}
	return p, nil
}

// Start starts producing; with a dictionary it follows writes of 0x1005,
// 0x1006 and 0x1019 from then on.
func (p *Producer) Start() error {
	return p.lc.start(func(context.Context) (func(context.Context), error) {
		var unwatch func()
		if d := p.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				x := canopen.DecodeUint(data)
				switch {
				case v.SubIndex != 0:
				case v.Index == od.IndexCOBIDSync:
					c, ok := cobID(x)
					if !ok {
						return
					}
					p.mu.Lock()
					p.cob, p.generate = c, c&canopen.COBGenerate != 0
					p.counter = 1
					p.mu.Unlock()
					p.poke()
				case v.Index == od.IndexCyclePeriod:
					if d, ok := micros(x); ok {
						p.SetPeriod(d)
					}
				case v.Index == od.IndexSyncOverflow:
					n, ok := overflowValue(x)
					if !ok {
						return
					}
					p.mu.Lock()
					p.overflow, p.counter = n, 1
					p.mu.Unlock()
				}
			})
		}
		return func(ctx context.Context) {
			if unwatch != nil {
				defer unwatch()
			}
			p.run(ctx)
		}, nil
	})
}

func (p *Producer) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// effective returns the period that applies: 0 when not generating.
func (p *Producer) effective() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.generate {
		return 0
	}
	return p.period
}

func (p *Producer) run(ctx context.Context) {
	var (
		ticker *time.Ticker
		cur    time.Duration
	)
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		period := p.effective()
		if period != cur {
			cur = period
			switch {
			case period == 0 && ticker != nil:
				ticker.Stop()
			case period > 0 && ticker == nil:
				ticker = time.NewTicker(period)
			case period > 0:
				ticker.Reset(period)
			}
		}
		var tick <-chan time.Time
		if cur > 0 {
			tick = ticker.C
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-tick:
			if err := p.Send(ctx); err != nil && ctx.Err() == nil {
				p.cfg.log.Warn("sync send", "error", err)
			}
		}
	}
}

// Send sends one SYNC message now (with the next counter value), for
// applications that drive the cycle themselves.
func (p *Producer) Send(ctx context.Context) error {
	p.mu.Lock()
	var c uint8
	if p.overflow != 0 {
		c = p.counter
		p.counter++
		if p.counter > p.overflow {
			p.counter = 1
		}
	}
	f := frame(p.cob, c, p.cfg.fd, p.cfg.brs)
	p.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err := p.bus.Send(sctx, &f)
	p.mu.Lock()
	if err != nil {
		p.stats.Errors++
	} else {
		p.stats.Sent++
	}
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("syncobj: send: %w", err)
	}
	return nil
}

// SetPeriod changes the communication cycle period (0 stops producing)
// and restarts the counter. It takes effect at once.
func (p *Producer) SetPeriod(d time.Duration) {
	p.mu.Lock()
	p.period, p.counter = max(d, 0), 1
	p.mu.Unlock()
	p.poke()
}

// SetOverflow changes the counter overflow value. As CiA 301 requires, it
// fails with [canopen.AbortDeviceState] while the period is not 0, and
// with [canopen.AbortInvalidValue] for 1 and values above 240.
func (p *Producer) SetOverflow(n uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.period != 0 {
		return canopen.AbortDeviceState
	}
	if checkOverflow(uint64(n)) != nil {
		return canopen.AbortInvalidValue
	}
	p.overflow, p.counter = n, 1
	return nil
}

// Period returns the configured period.
func (p *Producer) Period() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.period
}

// Producing reports whether SYNC messages are being generated: a period
// is set and (with a dictionary) 0x1005 bit 30 is set.
func (p *Producer) Producing() bool { return p.effective() > 0 }

// Close stops producing and waits for the goroutine. It removes the
// write hooks the producer installed on the dictionary.
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
