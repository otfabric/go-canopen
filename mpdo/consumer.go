// SPDX-License-Identifier: MIT

package mpdo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Event describes a value a [Consumer] wrote to its dictionary.
type Event struct {
	// PDO is the RPDO number.
	PDO int `json:"pdo"`
	// Message is the MPDO as received.
	Message Message `json:"message"`
	// Index and SubIndex are the local object written: the message's
	// object in DAM, the dispatcher list's in SAM.
	Index    uint16 `json:"index"`
	SubIndex uint8  `json:"subIndex"`
	// Data is the value written.
	Data []byte `json:"data"`
}

// ConsumerStats counts what a [Consumer] did.
type ConsumerStats struct {
	Received      uint64 `json:"received"`      // MPDOs received while enabled and active
	Ignored       uint64 `json:"ignored"`       // MPDOs received while disabled or inactive
	Short         uint64 `json:"short"`         // MPDOs shorter than 8 bytes, discarded
	WrongMode     uint64 `json:"wrongMode"`     // MPDOs of the other address mode, ignored
	OtherNode     uint64 `json:"otherNode"`     // DAM-MPDOs for another node, ignored
	NotDispatched uint64 `json:"notDispatched"` // SAM-MPDOs without a dispatcher entry, ignored
	NoObject      uint64 `json:"noObject"`      // values for a missing local object (EMCY 0x8230 in DAM)
	NotMappable   uint64 `json:"notMappable"`   // values for an object that is not PDO-mappable or not writable
	WriteErrors   uint64 `json:"writeErrors"`   // values the dictionary refused
	Applied       uint64 `json:"applied"`       // values written
}

// Consumer is an MPDO consumer: RPDO n of a dictionary whose mapping
// sub-index 00h is 0xFE (it consumes SAM-MPDOs, routed through the object
// dispatcher lists) or 0xFF (it consumes DAM-MPDOs addressed to the
// dictionary's node-ID or to all nodes). It follows changes of its
// parameters and writes values with remote writes (see the package
// documentation for the checks and the emergencies).
type Consumer struct {
	bus   canopen.Bus
	d     *od.Dictionary
	n     int
	opt   options
	log   *slog.Logger
	unpro func()

	mu      sync.Mutex
	cfg     config
	enabled bool
	started bool
	closed  bool
	sub     *canbus.Subscription
	subCOB  canopen.COBID // COB-ID subscribed to (valid when sub != nil)
	lenErr  bool          // 0x8210 active
	objErr  bool          // 0x8230 active
	pending []pdo.ErrorEvent
	unwatch func()

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	st struct {
		received, ignored, short, wrongMode, otherNode, notDispatched,
		noObject, notMappable, writeErrors, applied atomic.Uint64
	}
}

// NewConsumer returns the MPDO consumer of RPDO n (1..512) of d. It
// installs the MPDO reconfiguration rules ([ProtectPDO]) and does nothing
// on the bus. Call Start, and SetEnabled(true) when the node is NMT
// operational.
func NewConsumer(bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) (*Consumer, error) {
	if bus == nil || d == nil {
		return nil, errors.New("mpdo: nil bus or dictionary")
	}
	unpro, err := ProtectPDO(d, false, n)
	if err != nil {
		return nil, err
	}
	c := &Consumer{bus: bus, d: d, n: n, unpro: unpro, opt: buildOptions(opts), wake: make(chan struct{}, 1)}
	c.log = c.opt.log.With("mpdo", fmt.Sprintf("RPDO%d", n))
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.cfg = load(d, false, n)
	return c, nil
}

// Number returns the RPDO number.
func (c *Consumer) Number() int { return c.n }

// Mode returns the current address mode of the RPDO.
func (c *Consumer) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.mode
}

// SetEnabled enables (NMT operational) or disables the consumer.
func (c *Consumer) SetEnabled(on bool) {
	c.mu.Lock()
	c.enabled = on
	c.mu.Unlock()
}

// Stats returns a snapshot of the counters.
func (c *Consumer) Stats() ConsumerStats {
	return ConsumerStats{
		Received: c.st.received.Load(), Ignored: c.st.ignored.Load(), Short: c.st.short.Load(),
		WrongMode: c.st.wrongMode.Load(), OtherNode: c.st.otherNode.Load(),
		NotDispatched: c.st.notDispatched.Load(), NoObject: c.st.noObject.Load(),
		NotMappable: c.st.notMappable.Load(), WriteErrors: c.st.writeErrors.Load(), Applied: c.st.applied.Load(),
	}
}

// Start subscribes to the COB-ID (when the RPDO is an active MPDO) before
// it returns and starts the service goroutine. A second Start fails with
// [canopen.ErrBusy], a Start after Close with [canopen.ErrClosed].
func (c *Consumer) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.closed:
		return canopen.ErrClosed
	case c.started:
		return fmt.Errorf("mpdo: %w: already started", canopen.ErrBusy)
	}
	c.cfg = load(c.d, false, c.n)
	if err := c.resubLocked(); err != nil {
		return err
	}
	c.started = true
	c.unwatch = c.d.Watch(c.watch)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run()
	}()
	return nil
}

// Close stops the goroutine, waits for it and removes the hooks and the
// watcher. It is idempotent.
func (c *Consumer) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cancel()
	if c.sub != nil {
		_ = c.sub.Close()
		c.sub = nil
	}
	unwatch := c.unwatch
	c.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	c.unpro()
	c.wg.Wait()
	return nil
}

// resubLocked opens, replaces or closes the subscription as the
// configuration needs.
func (c *Consumer) resubLocked() error {
	want := c.cfg.active()
	key := canopen.NewCOBID(c.cfg.cob.ID()) | c.cfg.cob&canopen.COBExtended
	if c.sub != nil && want && key == c.subCOB {
		return nil
	}
	if c.sub != nil {
		_ = c.sub.Close()
		c.sub = nil
	}
	if !want {
		return nil
	}
	f := key.Filter()
	f.Remote = canbus.RemoteDataOnly
	s, err := c.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{f}}))
	if err != nil {
		return fmt.Errorf("mpdo: RPDO%d: subscribe %v: %w", c.n, key, err)
	}
	c.sub, c.subCOB = s, key
	return nil
}

// watch follows changes of the RPDO parameters.
func (c *Consumer) watch(v *od.Variable, _ []byte) {
	if v.Index != pdo.CommIndex(false, c.n) && v.Index != pdo.MappingIndex(false, c.n) {
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.cfg = load(c.d, false, c.n)
		if err := c.resubLocked(); err != nil {
			c.log.Error("resubscribe", "err", err)
		}
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// run is the service goroutine.
func (c *Consumer) run() {
	for {
		c.mu.Lock()
		sub := c.sub
		c.mu.Unlock()
		var frames <-chan canbus.Frame
		var done <-chan struct{}
		if sub != nil {
			frames, done = sub.Frames(), sub.Done()
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		case f, ok := <-frames:
			if ok {
				c.receive(&f)
			}
		case <-done:
			c.mu.Lock()
			swapped := c.sub != sub
			c.mu.Unlock()
			if !swapped {
				c.log.Error("subscription ended", "err", sub.Err())
				return
			}
		}
		c.report()
	}
}

// report delivers the pending error events.
func (c *Consumer) report() {
	c.mu.Lock()
	evs := c.pending
	c.pending = nil
	c.mu.Unlock()
	if c.opt.onError == nil {
		return
	}
	for _, ev := range evs {
		c.opt.onError(ev)
	}
}

// setErrLocked records an error state change.
func (c *Consumer) setErrLocked(flag *bool, on bool, code uint16, err error) {
	if *flag == on {
		return
	}
	*flag = on
	c.pending = append(c.pending, pdo.ErrorEvent{PDO: c.n, Code: code, Active: on, Err: err})
}

// receive handles a frame.
func (c *Consumer) receive(f *canbus.Frame) {
	c.mu.Lock()
	cfg := c.cfg
	if f.IsRemote() || !cfg.cob.Matches(f) {
		c.mu.Unlock()
		return
	}
	if !c.enabled || !cfg.active() {
		c.mu.Unlock()
		c.st.ignored.Add(1)
		return
	}
	c.st.received.Add(1)
	m, err := ParseMessage(f.Payload())
	if err != nil {
		c.st.short.Add(1)
		c.setErrLocked(&c.lenErr, true, pdo.EMCYPDOLength, fmt.Errorf("mpdo: RPDO%d: %w", c.n, err))
		c.mu.Unlock()
		return
	}
	c.setErrLocked(&c.lenErr, false, pdo.EMCYPDOLength, nil)
	c.mu.Unlock()
	c.handle(cfg.mode, m)
}

// handle routes and writes a message (Lely co_mpdo_dn).
func (c *Consumer) handle(mode Mode, m Message) {
	index, sub := m.Index, m.SubIndex
	switch {
	case m.DAM && mode != ModeDAM, !m.DAM && mode != ModeSAM:
		c.st.wrongMode.Add(1)
		return
	case m.DAM:
		if m.Node != 0 && m.Node != c.d.NodeID() {
			c.st.otherNode.Add(1)
			return
		}
	default:
		var ok bool
		if index, sub, ok = Dispatch(c.d, m.Node, m.Index, m.SubIndex); !ok {
			c.st.notDispatched.Add(1)
			return
		}
	}
	v, err := c.d.Variable(index, sub)
	if err != nil {
		c.st.noObject.Add(1)
		c.log.Debug("MPDO for a missing object", "mpdo", m, "object", fmt.Sprintf("%04X:%02X", index, sub))
		if m.DAM {
			c.mu.Lock()
			c.setErrLocked(&c.objErr, true, EMCYDAMNoObject,
				fmt.Errorf("mpdo: RPDO%d: DAM-MPDO for %04X:%02X: %w", c.n, index, sub, err))
			c.mu.Unlock()
		}
		return
	}
	if !v.PDOMappable || !v.Access.Writable() {
		c.st.notMappable.Add(1)
		return
	}
	n := 4
	if s := v.DataType.Size(); s != 0 && s < 4 {
		n = s
	}
	data := append([]byte(nil), m.Data[:n]...)
	if err := c.d.Write(index, sub, data); err != nil {
		c.st.writeErrors.Add(1)
		c.log.Debug("write", "mpdo", m, "err", err)
		return
	}
	c.st.applied.Add(1)
	if m.DAM {
		c.mu.Lock()
		c.setErrLocked(&c.objErr, false, EMCYDAMNoObject, nil)
		c.mu.Unlock()
	}
	if c.opt.onRx != nil {
		c.opt.onRx(Event{PDO: c.n, Message: m, Index: index, SubIndex: sub, Data: data})
	}
}
