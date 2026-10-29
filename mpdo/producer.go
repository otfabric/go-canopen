// SPDX-License-Identifier: MIT

package mpdo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// ProducerStats counts what a [Producer] did.
type ProducerStats struct {
	Sent       uint64 `json:"sent"`       // MPDOs transmitted
	SendErrors uint64 `json:"sendErrors"` // transmissions the bus refused
	Refused    uint64 `json:"refused"`    // SAM-MPDOs refused: object not scanned, not mappable, unreadable or longer than 4 bytes
	Inhibited  uint64 `json:"inhibited"`  // transmissions delayed by the inhibit time
}

// Producer is an MPDO producer: TPDO n of a dictionary whose mapping
// sub-index 00h is 0xFE (SAM) or 0xFF (DAM). It reads the COB-ID,
// transmission type and inhibit time at every transmission, so it follows
// changes of the parameters. Transmissions are serialised and respect the
// inhibit time: a transmission that comes too early waits (Lely refuses it
// with EAGAIN instead). The producer has no goroutine.
type Producer struct {
	bus   canopen.Bus
	d     *od.Dictionary
	n     int
	log   *slog.Logger
	unpro func()

	mu      sync.Mutex
	started bool
	closed  bool
	enabled bool

	sendMu   sync.Mutex // serialises transmissions
	lastSent time.Time

	st struct{ sent, sendErrors, refused, inhibited atomic.Uint64 }
}

// NewProducer returns the MPDO producer of TPDO n (1..512) of d. It
// installs the MPDO reconfiguration rules ([ProtectPDO]) and does nothing
// on the bus. Call Start, and SetEnabled(true) when the node is NMT
// operational.
func NewProducer(bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) (*Producer, error) {
	if bus == nil || d == nil {
		return nil, errors.New("mpdo: nil bus or dictionary")
	}
	unpro, err := ProtectPDO(d, true, n)
	if err != nil {
		return nil, err
	}
	o := buildOptions(opts)
	return &Producer{bus: bus, d: d, n: n, unpro: unpro, log: o.log.With("mpdo", fmt.Sprintf("TPDO%d", n))}, nil
}

// Number returns the TPDO number.
func (p *Producer) Number() int { return p.n }

// Mode returns the current address mode of the TPDO.
func (p *Producer) Mode() Mode { return ModeOf(p.d, true, p.n) }

// Start makes the producer ready to send. A second Start fails with
// [canopen.ErrBusy], a Start after Close with [canopen.ErrClosed].
func (p *Producer) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.closed:
		return canopen.ErrClosed
	case p.started:
		return fmt.Errorf("mpdo: %w: already started", canopen.ErrBusy)
	}
	p.started = true
	return nil
}

// Close stops the producer and removes its hooks. It is idempotent.
func (p *Producer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.unpro()
	return nil
}

// SetEnabled enables (NMT operational) or disables the producer.
func (p *Producer) SetEnabled(on bool) {
	p.mu.Lock()
	p.enabled = on
	p.mu.Unlock()
}

// Stats returns a snapshot of the counters.
func (p *Producer) Stats() ProducerStats {
	return ProducerStats{
		Sent: p.st.sent.Load(), SendErrors: p.st.sendErrors.Load(),
		Refused: p.st.refused.Load(), Inhibited: p.st.inhibited.Load(),
	}
}

// ready checks the state and the configuration for a transmission in
// mode m.
func (p *Producer) ready(m Mode) (config, error) {
	p.mu.Lock()
	started, closed, enabled := p.started, p.closed, p.enabled
	p.mu.Unlock()
	switch {
	case closed:
		return config{}, canopen.ErrClosed
	case !started:
		return config{}, ErrNotStarted
	case !enabled:
		return config{}, ErrDisabled
	}
	c := load(p.d, true, p.n)
	if !c.active() {
		return c, fmt.Errorf("%w: TPDO%d (COB-ID %v, %v, mode %v)", ErrInactive, p.n, c.cob, c.tt, c.mode)
	}
	if c.mode != m {
		return c, fmt.Errorf("%w: TPDO%d is a %v producer", ErrMode, p.n, c.mode)
	}
	return c, nil
}

// SendDAM sends a DAM-MPDO: value (at most 4 bytes, padded with zeros) for
// object index:sub of node dest, or of every consumer when dest is 0. The
// TPDO must be a DAM producer (mapping sub 00h 0xFF). A value longer than
// 4 bytes fails with [canopen.AbortPDOLength], dest above 127 with
// [canopen.ErrInvalidNodeID].
func (p *Producer) SendDAM(ctx context.Context, dest canopen.NodeID, index uint16, sub uint8, value []byte) error {
	if dest > canopen.MaxNodeID {
		return fmt.Errorf("mpdo: destination: %w: %d", canopen.ErrInvalidNodeID, dest)
	}
	if len(value) > 4 {
		return fmt.Errorf("mpdo: %d-byte value: %w", len(value), canopen.AbortPDOLength)
	}
	c, err := p.ready(ModeDAM)
	if err != nil {
		return err
	}
	m := Message{DAM: true, Node: dest, Index: index, SubIndex: sub}
	copy(m.Data[:], value)
	return p.transmit(ctx, c, m)
}

// Send sends a SAM-MPDO of the local object index:sub. The TPDO must be a
// SAM producer (mapping sub 00h 0xFE), the dictionary's node-ID valid,
// and the object listed in an object scanner list ([ErrNotScanned]),
// existing ([canopen.AbortNoObject], [canopen.AbortNoSubIndex]),
// PDO-mappable and readable ([canopen.AbortNotMappable]), and at most 4
// bytes long ([canopen.AbortPDOLength]); it is read with a remote read,
// so read hooks apply.
func (p *Producer) Send(ctx context.Context, index uint16, sub uint8) error {
	c, err := p.ready(ModeSAM)
	if err != nil {
		return err
	}
	node := p.d.NodeID()
	if err := node.Check(); err != nil {
		return fmt.Errorf("mpdo: SAM producer: %w", err)
	}
	data, err := p.sample(index, sub)
	if err != nil {
		p.st.refused.Add(1)
		return fmt.Errorf("mpdo: SAM %04X:%02X: %w", index, sub, err)
	}
	m := Message{Node: node, Index: index, SubIndex: sub}
	copy(m.Data[:], data)
	return p.transmit(ctx, c, m)
}

// sample reads a SAM object (Lely co_sam_mpdo_up).
func (p *Producer) sample(index uint16, sub uint8) ([]byte, error) {
	if !Scanned(p.d, index, sub) {
		return nil, fmt.Errorf("%w: %w", ErrNotScanned, canopen.AbortNotMappable)
	}
	v, err := p.d.Variable(index, sub)
	if err != nil {
		return nil, err
	}
	if !v.PDOMappable || !v.Access.Readable() {
		return nil, canopen.AbortNotMappable
	}
	data, err := p.d.Read(index, sub)
	if err != nil {
		return nil, err
	}
	if len(data) > 4 {
		return nil, canopen.AbortPDOLength
	}
	return data, nil
}

// transmit sends a message no sooner than the inhibit time after the
// previous one.
func (p *Producer) transmit(ctx context.Context, c config, m Message) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if c.inhibit > 0 && !p.lastSent.IsZero() {
		if wait := time.Until(p.lastSent.Add(c.inhibit)); wait > 0 {
			p.st.inhibited.Add(1)
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
	}
	b := m.Bytes()
	var f canbus.Frame
	var err error
	if c.cob.Extended() {
		f, err = canbus.NewExtended(c.cob.ID(), b[:])
	} else {
		f, err = canbus.NewStandard(c.cob.ID(), b[:])
	}
	if err == nil {
		err = p.bus.Send(ctx, &f)
	}
	if err != nil {
		p.st.sendErrors.Add(1)
		p.log.Debug("send", "mpdo", m, "err", err)
		return fmt.Errorf("mpdo: TPDO%d: send: %w", p.n, err)
	}
	p.lastSent = time.Now()
	p.st.sent.Add(1)
	return nil
}
