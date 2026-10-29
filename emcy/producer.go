// SPDX-License-Identifier: MIT

package emcy

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Option configures a [Producer] or a [Consumer]. Each option names the
// services it applies to; the others ignore it.
type Option func(*config)

type config struct {
	log       *slog.Logger
	dict      *od.Dictionary
	cob       canopen.COBID
	cobSet    bool
	inhibit   time.Duration
	inhibSet  bool
	history   int
	histSet   bool
	handler   func(Event)
	producers map[canopen.COBID]canopen.NodeID
}

func newConfig(opts []Option) config {
	c := config{log: discard, history: 8}
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

// WithDictionary makes a [Producer] use a node's object dictionary: the
// COB-ID EMCY (0x1014) and the inhibit time (0x1015) are read from it and
// followed when written, the error register 0x1001 and, when the object
// exists, the pre-defined error field 0x1003 are kept up to date. The
// write hooks it installs refuse a new CAN-ID in 0x1014 while the COB-ID
// is valid, and any value but 0 in 0x1003:0 (writing 0 clears the error
// history), with [canopen.AbortInvalidValue].
func WithDictionary(d *od.Dictionary) Option { return func(c *config) { c.dict = d } }

// WithCOBID sets the COB-ID of a [Producer] (default 0x80 + node-ID, or
// 0x1014 with a dictionary). A COB-ID with [canopen.COBInvalid] set
// disables the transmission; errors are still recorded.
func WithCOBID(cob canopen.COBID) Option { return func(c *config) { c.cob, c.cobSet = cob, true } }

// WithInhibit sets the inhibit time of a [Producer]: the minimum time
// between two emergency messages (default 0, or 0x1015 with a
// dictionary). Messages raised within it are queued and sent later.
func WithInhibit(d time.Duration) Option { return func(c *config) { c.inhibit, c.inhibSet = d, true } }

// WithHistory sets the length of the error history of a [Producer]
// without a 0x1003 object (default 8).
func WithHistory(n int) Option { return func(c *config) { c.history, c.histSet = n, true } }

// WithHandler calls h for every emergency message a [Consumer] receives,
// on its goroutine; h must not block for long.
func WithHandler(h func(Event)) Option { return func(c *config) { c.handler = h } }

// WithProducer makes a [Consumer] receive the emergency messages of a node
// sent on a COB-ID (repeatable). Without it a consumer receives the
// predefined identifiers 0x081..0x0FF of all nodes.
func WithProducer(node canopen.NodeID, cob canopen.COBID) Option {
	return func(c *config) {
		if c.producers == nil {
			c.producers = map[canopen.COBID]canopen.NodeID{}
		}
		c.producers[cob&^canopen.COBInvalid] = node
	}
}

// maxQueue bounds the messages waiting for the inhibit time.
const maxQueue = 256

// ProducerStats counts what a [Producer] did.
type ProducerStats struct {
	Sent       uint64 `json:"sent"`       // messages transmitted
	Queued     uint64 `json:"queued"`     // messages delayed by the inhibit time
	Suppressed uint64 `json:"suppressed"` // messages not sent: COB-ID invalid
	Dropped    uint64 `json:"dropped"`    // messages lost: queue full, or pending at Close
	Errors     uint64 `json:"errors"`     // send failures
}

// Producer is a node's emergency producer (CiA 301 7.2.7). It keeps the
// set of active errors and the error register derived from it, records
// every raised error in the error history (newest first), and sends
// emergency messages: one per newly raised error and an error reset
// (code 0x0000) when the last active error is cleared.
//
// Consecutive messages are at least the inhibit time apart; messages that
// come sooner are queued in order and sent when the inhibit time has
// passed (up to 256 queued messages; Start runs the queue). Without an
// inhibit time and with an empty queue, messages are sent synchronously
// by the calling goroutine.
type Producer struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	syncMu sync.Mutex // orders dictionary updates
	mu     sync.Mutex
	node   canopen.NodeID
	cob    canopen.COBID
	inh    time.Duration
	last   time.Time
	queue  []Message
	active map[uint16]ErrorRegister
	hist   []uint32
	histN  int
	dictH  bool // the dictionary has 0x1003
	stats  ProducerStats
}

// NewProducer returns the emergency producer of a node. Options:
// [WithLogger], [WithDictionary], [WithCOBID], [WithInhibit],
// [WithHistory].
func NewProducer(bus canopen.Bus, node canopen.NodeID, opts ...Option) (*Producer, error) {
	if err := node.Check(); err != nil {
		return nil, err
	}
	p := &Producer{bus: bus, cfg: newConfig(opts), node: node, wake: make(chan struct{}, 1), active: map[uint16]ErrorRegister{}}
	p.cob = canopen.NewCOBID(canopen.FuncEMCY + canbus.ID(node))
	p.histN = p.cfg.history
	if d := p.cfg.dict; d != nil {
		cob, err := d.GetUint(od.IndexCOBIDEmcy, 0)
		if err != nil {
			return nil, fmt.Errorf("emcy: COB-ID EMCY 0x1014: %w", err)
		}
		p.cob = canopen.COBID(cob)
		if v, err := d.GetUint(od.IndexInhibitEmcy, 0); err == nil {
			p.inh = time.Duration(v) * 100 * time.Microsecond
		}
		if o := d.Object(od.IndexErrorField); o != nil {
			p.dictH, p.histN = true, len(o.Subs)-1
		}
		d.OnWrite(od.IndexCOBIDEmcy, 0, func(_ *od.Variable, data []byte) error {
			x, _ := d.GetUint(od.IndexCOBIDEmcy, 0)
			cur := canopen.COBID(x)
			nw := canopen.COBID(canopen.DecodeUint(data))
			if cur.Valid() && nw.Valid() && (cur.ID() != nw.ID() || cur.Extended() != nw.Extended()) {
				return canopen.AbortInvalidValue
			}
			return nil
		})
		if p.dictH {
			d.OnWrite(od.IndexErrorField, 0, func(_ *od.Variable, data []byte) error {
				if canopen.DecodeUint(data) != 0 {
					return canopen.AbortInvalidValue
				}
				p.mu.Lock()
				p.hist = nil
				p.mu.Unlock()
				p.syncDict()
				return nil
			})
		}
	}
	if p.cfg.cobSet {
		p.cob = p.cfg.cob
	}
	if p.cfg.inhibSet {
		p.inh = max(p.cfg.inhibit, 0)
	}
	if p.histN < 0 || p.histN > 254 {
		return nil, fmt.Errorf("emcy: history length %d out of range 0..254", p.histN)
	}
	p.syncDict()
	return p, nil
}

// Start runs the inhibit queue; with a dictionary it follows writes of
// 0x1014 and 0x1015 from then on.
func (p *Producer) Start() error {
	return p.lc.start(func(context.Context) (func(context.Context), error) {
		var unwatch func()
		if d := p.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				switch {
				case v.Index == od.IndexCOBIDEmcy && v.SubIndex == 0:
					p.SetCOBID(canopen.COBID(canopen.DecodeUint(data)))
				case v.Index == od.IndexInhibitEmcy && v.SubIndex == 0:
					p.SetInhibit(time.Duration(canopen.DecodeUint(data)) * 100 * time.Microsecond)
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

func (p *Producer) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		p.mu.Lock()
		var (
			m    Message
			send bool
			wait = time.Hour
		)
		if len(p.queue) > 0 {
			if d := time.Until(p.last.Add(p.inh)); d > 0 {
				wait = d
			} else {
				m, send = p.queue[0], true
				p.queue = p.queue[1:]
				p.last = time.Now()
			}
		}
		cob := p.cob
		p.mu.Unlock()
		if send {
			_ = p.transmit(ctx, cob, m)
			continue
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.stats.Dropped += uint64(len(p.queue))
			p.queue = nil
			p.mu.Unlock()
			return
		case <-p.wake:
		case <-timer.C:
		}
	}
}

// transmit sends one message on cob.
func (p *Producer) transmit(ctx context.Context, cob canopen.COBID, m Message) error {
	if !cob.Valid() {
		p.mu.Lock()
		p.stats.Suppressed++
		p.mu.Unlock()
		return nil
	}
	f := m.Frame(cob)
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
		p.cfg.log.Warn("emcy send", "node", int(p.node), "error", err)
		return fmt.Errorf("emcy: send: %w", err)
	}
	p.cfg.log.Debug("emcy sent", "node", int(p.node), "message", m.String())
	return nil
}

// Send sends an emergency message as it is, without changing the active
// errors, the register or the history. It is queued when the inhibit time
// has not passed since the previous message, and sent at once (returning
// the bus error) otherwise. A COB-ID with the invalid bit set suppresses
// it.
func (p *Producer) Send(ctx context.Context, m Message) error {
	p.mu.Lock()
	if !p.cob.Valid() {
		p.stats.Suppressed++
		p.mu.Unlock()
		return nil
	}
	now := time.Now()
	if len(p.queue) > 0 || (p.inh > 0 && !p.last.IsZero() && now.Before(p.last.Add(p.inh))) {
		if len(p.queue) >= maxQueue {
			p.stats.Dropped++
			p.mu.Unlock()
			return fmt.Errorf("%w: emergency queue full", canopen.ErrBusy)
		}
		p.queue = append(p.queue, m)
		p.stats.Queued++
		p.mu.Unlock()
		select {
		case p.wake <- struct{}{}:
		default:
		}
		return nil
	}
	p.last = now
	cob := p.cob
	p.mu.Unlock()
	return p.transmit(ctx, cob, m)
}

// registerLocked returns the error register of the active errors.
func (p *Producer) registerLocked() ErrorRegister {
	var r ErrorRegister
	for _, b := range p.active {
		r |= b
	}
	if len(p.active) > 0 {
		r |= RegGeneric
	}
	return r &^ RegReserved
}

// Raise records an error and sends its emergency message: the error
// becomes active, the register (0x1001) includes reg and the generic bit,
// and the code is added to the history (0x1003, newest at sub-index 1;
// the upper 16 bits of an entry, manufacturer-specific additional
// information, are 0). Raising an error that is already active only adds
// reg to its register bits: no message, no history entry. Code 0x0000 is
// refused.
func (p *Producer) Raise(ctx context.Context, code uint16, reg ErrorRegister, msef [5]byte) error {
	if code == CodeNoError {
		return fmt.Errorf("emcy: cannot raise error code 0x0000")
	}
	p.mu.Lock()
	old, dup := p.active[code]
	p.active[code] = old | reg
	r := p.registerLocked()
	if !dup && p.histN > 0 {
		p.hist = slices.Insert(p.hist, 0, uint32(code))
		if len(p.hist) > p.histN {
			p.hist = p.hist[:p.histN]
		}
	}
	p.mu.Unlock()
	p.syncDict()
	if dup {
		return nil
	}
	p.cfg.log.Info("emcy raise", "node", int(p.node), "code", fmt.Sprintf("0x%04X", code), "text", CodeText(code))
	return p.Send(ctx, Message{Code: code, Register: r, MSEF: msef})
}

// Clear deactivates an error. When no error remains active it sends the
// error reset message (code 0x0000, register 0); otherwise it only
// updates the register. Clearing an error that is not active does
// nothing.
func (p *Producer) Clear(ctx context.Context, code uint16) error {
	p.mu.Lock()
	if _, ok := p.active[code]; !ok {
		p.mu.Unlock()
		return nil
	}
	delete(p.active, code)
	r, none := p.registerLocked(), len(p.active) == 0
	p.mu.Unlock()
	p.syncDict()
	if !none {
		return nil
	}
	return p.Send(ctx, Message{Code: CodeNoError, Register: r})
}

// ClearAll deactivates every error and sends the error reset message when
// any was active.
func (p *Producer) ClearAll(ctx context.Context) error {
	p.mu.Lock()
	n := len(p.active)
	clear(p.active)
	p.mu.Unlock()
	if n == 0 {
		return nil
	}
	p.syncDict()
	return p.Send(ctx, Message{Code: CodeNoError})
}

// ClearHistory empties the error history, as writing 0 to 0x1003:0
// does.
func (p *Producer) ClearHistory() {
	p.mu.Lock()
	p.hist = nil
	p.mu.Unlock()
	p.syncDict()
}

// syncDict writes the register and the history to the dictionary.
func (p *Producer) syncDict() {
	d := p.cfg.dict
	if d == nil {
		return
	}
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	p.mu.Lock()
	reg, hist, dictH, n := p.registerLocked(), slices.Clone(p.hist), p.dictH, p.histN
	p.mu.Unlock()
	setIfChanged(d, od.IndexErrorRegister, 0, uint64(reg))
	if !dictH {
		return
	}
	for i := 1; i <= n; i++ {
		var v uint64
		if i <= len(hist) {
			v = uint64(hist[i-1])
		}
		setIfChanged(d, od.IndexErrorField, uint8(i), v)
	}
	setIfChanged(d, od.IndexErrorField, 0, uint64(len(hist)))
}

func setIfChanged(d *od.Dictionary, index uint16, sub uint8, v uint64) {
	if cur, err := d.GetUint(index, sub); err == nil && cur == v {
		return
	}
	_ = d.SetUint(index, sub, v)
}

// SetCOBID changes the COB-ID; the invalid bit stops the transmission.
func (p *Producer) SetCOBID(cob canopen.COBID) {
	p.mu.Lock()
	p.cob = cob
	p.mu.Unlock()
}

// SetInhibit changes the inhibit time.
func (p *Producer) SetInhibit(d time.Duration) {
	p.mu.Lock()
	p.inh = max(d, 0)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Register returns the error register.
func (p *Producer) Register() ErrorRegister {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registerLocked()
}

// Active returns the active error codes in ascending order.
func (p *Producer) Active() []uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint16, 0, len(p.active))
	for c := range p.active {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// History returns the error history, newest first (the entries of 0x1003
// from sub-index 1).
func (p *Producer) History() []uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.hist)
}

// Close stops the queue and waits for it; queued messages are dropped.
func (p *Producer) Close() error {
	p.lc.close()
	return nil
}

// Stats returns the counters.
func (p *Producer) Stats() ProducerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
