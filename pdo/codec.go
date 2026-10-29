// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Value is one decoded object of a PDO.
type Value struct {
	Index    uint16           `json:"index"`
	SubIndex uint8            `json:"subIndex"`
	Name     string           `json:"name,omitempty"`
	Type     canopen.DataType `json:"type"`
	// Raw is the encoding of the value in its data type.
	Raw []byte `json:"raw"`
	// Value is Raw decoded with [canopen.Decode].
	Value any `json:"value"`
}

// String formats the value as "name=value".
func (v Value) String() string {
	name := v.Name
	if name == "" {
		name = fmt.Sprintf("%04X:%02X", v.Index, v.SubIndex)
	}
	return name + "=" + canopen.FormatValue(v.Type, v.Raw)
}

// codec resolves the entries of a mapping to the variables of a
// dictionary.
type codec struct {
	m    Mapping
	vars []*od.Variable // nil for dummy entries
	n    int            // non-dummy entries
	opt  options
}

func newCodec(m Mapping, d *od.Dictionary, opts []Option) (codec, error) {
	c := codec{m: append(Mapping(nil), m...), vars: make([]*od.Variable, len(m)), opt: buildOptions(opts)}
	if m.Bits() > c.opt.maxBits() {
		return c, fmt.Errorf("pdo: mapping %s: %w", m, canopen.AbortPDOLength)
	}
	for i, e := range m {
		if e.IsDummy() {
			if !lengthFits(canopen.DataType(e.Index), int(e.Bits)) {
				return c, fmt.Errorf("pdo: dummy entry %s: %w", e, canopen.AbortNotMappable)
			}
			continue
		}
		v, err := d.Variable(e.Index, e.SubIndex)
		if err != nil {
			return c, fmt.Errorf("pdo: entry %s: %w", e, err)
		}
		if !lengthFits(v.DataType, int(e.Bits)) {
			return c, fmt.Errorf("pdo: entry %s: %s has %d bits: %w", e, v.DataType, v.DataType.Bits(), canopen.AbortNotMappable)
		}
		c.vars[i] = v
		c.n++
	}
	return c, nil
}

// Decoder decodes the data of a PDO into named, typed values: a master's
// view of a remote node's TPDO (or of any PDO on the bus).
type Decoder struct{ c codec }

// NewDecoder returns a decoder for a mapping whose objects are described
// by d, a dictionary of the node that maps them (for example built from
// its EDS). Each entry must exist in d with a length that fits its data
// type ([ValidateEntry] rules; mappability and access are not checked).
// Of the options only [WithFD] applies: a CANopen FD mapping of up to
// [MaxBitsFD] bits.
func NewDecoder(m Mapping, d *od.Dictionary, opts ...Option) (*Decoder, error) {
	c, err := newCodec(m, d, opts)
	if err != nil {
		return nil, err
	}
	return &Decoder{c}, nil
}

// Mapping returns the mapping.
func (dec *Decoder) Mapping() Mapping { return append(Mapping(nil), dec.c.m...) }

// Decode decodes PDO data into one value per non-dummy entry, in mapping
// order. Data shorter than the mapping is an error wrapping [ErrLength];
// extra bytes are ignored.
func (dec *Decoder) Decode(data []byte) ([]Value, error) {
	raw, err := unpack(dec.c.m, data, dec.c.opt.maxBits())
	if err != nil {
		return nil, err
	}
	out := make([]Value, 0, dec.c.n)
	for i, v := range dec.c.vars {
		if v == nil {
			continue
		}
		b := raw[i]
		if n := v.DataType.Size(); len(b) < n {
			b = append(b, make([]byte, n-len(b))...)
		}
		x, err := canopen.Decode(v.DataType, b)
		if err != nil {
			return nil, fmt.Errorf("pdo: %s: %w", v.Key(), err)
		}
		out = append(out, Value{Index: v.Index, SubIndex: v.SubIndex, Name: v.Name, Type: v.DataType, Raw: b, Value: x})
	}
	return out, nil
}

// Encoder encodes values into the data of a PDO: a master producing a
// remote node's RPDO.
type Encoder struct{ c codec }

// NewEncoder returns an encoder for a mapping whose objects are described
// by d, as for [NewDecoder]. With [WithFD] the mapping may have up to
// [MaxBitsFD] bits and a [Sender] using the encoder sends CAN FD frames.
func NewEncoder(m Mapping, d *od.Dictionary, opts ...Option) (*Encoder, error) {
	c, err := newCodec(m, d, opts)
	if err != nil {
		return nil, err
	}
	return &Encoder{c}, nil
}

// Mapping returns the mapping.
func (enc *Encoder) Mapping() Mapping { return append(Mapping(nil), enc.c.m...) }

// Encode encodes one Go value per non-dummy entry, in mapping order
// ([canopen.Encode] rules: any Go integer, bool, float32/float64, or the
// encoded bytes); dummy entries are sent as zeros.
func (enc *Encoder) Encode(values ...any) ([]byte, error) {
	if len(values) != enc.c.n {
		return nil, fmt.Errorf("pdo: %d values for %d mapped objects", len(values), enc.c.n)
	}
	raw := make([][]byte, len(enc.c.m))
	j := 0
	for i, v := range enc.c.vars {
		if v == nil {
			continue
		}
		b, err := canopen.Encode(v.DataType, values[j])
		if err != nil {
			return nil, fmt.Errorf("pdo: %s: %w", v.Key(), err)
		}
		raw[i] = b
		j++
	}
	return pack(enc.c.m, raw, enc.c.opt.maxBits())
}

// Sender produces a PDO on the bus: a master's RPDO for a node.
type Sender struct {
	bus canopen.Bus
	cob canopen.COBID
	enc *Encoder
}

// NewSender returns a sender of PDOs on a COB-ID with an encoder. The
// frames are CAN FD frames when the encoder was made [WithFD].
func NewSender(bus canopen.Bus, cob canopen.COBID, enc *Encoder) *Sender {
	return &Sender{bus: bus, cob: cob, enc: enc}
}

// Send encodes the values ([Encoder.Encode]) and sends the PDO.
func (s *Sender) Send(ctx context.Context, values ...any) error {
	data, err := s.enc.Encode(values...)
	if err != nil {
		return err
	}
	f, err := s.enc.c.opt.frame(s.cob, data)
	if err != nil {
		return err
	}
	return s.bus.Send(ctx, &f)
}

// ListenerStats counts what a [Listener] did.
type ListenerStats struct {
	Received uint64 `json:"received"` // PDOs decoded and delivered
	Errors   uint64 `json:"errors"`   // PDOs that could not be decoded (too short)
}

// Listener receives a remote node's PDO and delivers its decoded values:
// a master's TPDO consumer.
type Listener struct {
	bus     canopen.Bus
	cob     canopen.COBID
	dec     *Decoder
	handler func(f *canbus.Frame, values []Value)
	log     *slog.Logger

	mu      sync.Mutex
	sub     *canbus.Subscription
	started bool
	closed  bool
	done    chan struct{}
	wg      sync.WaitGroup

	received, errors atomic.Uint64
}

// NewListener returns a listener for PDOs on a COB-ID. The handler is
// called from the listener goroutine with each received frame and its
// decoded values. Of the options only [WithLogger] applies.
func NewListener(bus canopen.Bus, cob canopen.COBID, dec *Decoder, handler func(f *canbus.Frame, values []Value), opts ...Option) *Listener {
	o := buildOptions(opts)
	return &Listener{bus: bus, cob: cob, dec: dec, handler: handler, log: o.log, done: make(chan struct{})}
}

// Start subscribes to the COB-ID and starts the listener goroutine.
func (l *Listener) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return canopen.ErrClosed
	}
	if l.started {
		return nil
	}
	s, err := l.bus.Subscribe(canbus.WithFilters(filter(l.cob, canbus.RemoteDataOnly)))
	if err != nil {
		return err
	}
	l.sub, l.started = s, true
	l.wg.Add(1)
	go l.run(s)
	return nil
}

func (l *Listener) run(s *canbus.Subscription) {
	defer l.wg.Done()
	for {
		select {
		case <-l.done:
			return
		case <-s.Done():
			return
		case f := <-s.Frames():
			vals, err := l.dec.Decode(f.Payload())
			if err != nil {
				l.errors.Add(1)
				l.log.Debug("decode PDO", "cob-id", l.cob, "err", err)
				continue
			}
			l.received.Add(1)
			if l.handler != nil {
				l.handler(&f, vals)
			}
		}
	}
}

// Request sends a remote transmission request for the PDO. It fails with
// [ErrNoRTR] for a CANopen FD PDO (a decoder made [WithFD]).
func (l *Listener) Request(ctx context.Context) error {
	if l.dec.c.opt.fd {
		return ErrNoRTR
	}
	f, err := canbus.NewRemote(l.cob.ID(), l.cob.Extended(), uint8(l.dec.c.m.Len()))
	if err != nil {
		return err
	}
	return l.bus.Send(ctx, &f)
}

// Stats returns a snapshot of the counters.
func (l *Listener) Stats() ListenerStats {
	return ListenerStats{Received: l.received.Load(), Errors: l.errors.Load()}
}

// Close stops the listener and waits for its goroutine. It is idempotent.
func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	if l.sub != nil {
		_ = l.sub.Close()
	}
	l.mu.Unlock()
	l.wg.Wait()
	return nil
}
