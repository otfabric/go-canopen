// SPDX-License-Identifier: MIT

package emcy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestCodeText(t *testing.T) {
	for code, want := range map[uint16]string{
		0x0000: "error reset or no error",
		0x8130: "life guard error or heartbeat error",
		0x8113: "CAN overrun (objects lost) (0x8113)",
		0x2310: "current, device output side (0x2310)",
		0x5432: "device hardware, generic error (0x5432)",
		0xFF12: "device specific, generic error (0xFF12)",
		0x8260: "protocol error, generic (0x8260)",
		0x0012: "unknown error code 0x0012",
		0xA000: "unknown error code 0xA000",
	} {
		if got := CodeText(code); got != want {
			t.Errorf("0x%04X: %q, want %q", code, got, want)
		}
	}
	for c := range codeText {
		if CodeText(c) == "" || strings.HasPrefix(CodeText(c), "unknown") {
			t.Errorf("0x%04X", c)
		}
	}
}

func TestErrorRegister(t *testing.T) {
	if ErrorRegister(0).String() != "none" {
		t.Error(ErrorRegister(0).String())
	}
	if s := (RegGeneric | RegCommunication | RegManufacturer).String(); s != "generic|communication|manufacturer" {
		t.Error(s)
	}
	for code, want := range map[uint16]ErrorRegister{
		0x0000: 0, 0x1000: RegGeneric, 0x2310: RegGeneric | RegCurrent, 0x3100: RegGeneric | RegVoltage,
		0x4200: RegGeneric | RegTemperature, 0x8110: RegGeneric | RegCommunication,
		0x8240: RegGeneric | RegCommunication, 0x8000: RegGeneric, 0xFF00: RegGeneric,
	} {
		if got := ClassRegister(code); got != want {
			t.Errorf("0x%04X: %v, want %v", code, got, want)
		}
	}
}

func TestMessage(t *testing.T) {
	m := Message{Code: 0x8130, Register: 0x11, MSEF: [5]byte{1, 2, 3, 4, 5}}
	b := m.Bytes()
	if b != [8]byte{0x30, 0x81, 0x11, 1, 2, 3, 4, 5} {
		t.Fatalf("% X", b)
	}
	got, err := ParseMessage(b[:])
	if err != nil || got != m {
		t.Fatal(got, err)
	}
	if !strings.Contains(m.String(), "0x8130") || m.IsReset() || !(Message{}).IsReset() {
		t.Error(m.String())
	}
	short, err := ParseMessage([]byte{0x00, 0x10, 0x01})
	if err != nil || short.Code != 0x1000 || short.Register != 1 {
		t.Error(short, err)
	}
	for _, n := range []int{0, 2, 9} {
		if _, err := ParseMessage(make([]byte, n)); !errors.Is(err, canopen.ErrProtocol) {
			t.Errorf("%d bytes: %v", n, err)
		}
	}
	f := m.Frame(canopen.NewCOBID(0x85))
	if f.ID != 0x85 || f.Len != 8 || f.IsExtended() {
		t.Error(f)
	}
	f = m.Frame(canopen.NewCOBID(0x12345))
	if !f.IsExtended() {
		t.Error(f)
	}
	if got, err := ParseFrame(&f); err != nil || got != m {
		t.Error(got, err)
	}
	f.Flags |= canbus.FlagRemote
	if _, err := ParseFrame(&f); err == nil {
		t.Error("remote frame accepted")
	}
}

func TestProducerConsumer(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c := NewConsumer(b, WithHandler(ev.add), WithLogger(nil))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	p, err := NewProducer(a, 5, WithHistory(2))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Raise(ctx, CodeVoltageMains, RegVoltage, [5]byte{9}); err != nil {
		t.Fatal(err)
	}
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if e.Node != 5 || e.COBID != 0x85 || e.Code != 0x3100 || e.Register != RegGeneric|RegVoltage || e.MSEF[0] != 9 || e.Time.IsZero() {
		t.Errorf("event %+v", e)
	}
	// Raising an active error again: no message.
	_ = p.Raise(ctx, CodeVoltageMains, RegManufacturer, [5]byte{})
	_ = p.Raise(ctx, CodeCANOverrun, RegCommunication, [5]byte{})
	e = ev.wait(t, time.Second, func(Event) bool { return true })
	if e.Code != CodeCANOverrun || e.Register != RegGeneric|RegVoltage|RegCommunication|RegManufacturer {
		t.Errorf("event %+v", e)
	}
	_ = p.Raise(ctx, CodeHeartbeat, RegCommunication, [5]byte{})
	ev.wait(t, time.Second, func(Event) bool { return true })
	if h := p.History(); !slices.Equal(h, []uint32{0x8130, 0x8110}) {
		t.Errorf("history %X", h)
	}
	if a := p.Active(); !slices.Equal(a, []uint16{0x3100, 0x8110, 0x8130}) {
		t.Errorf("active %X", a)
	}
	// Clearing: no message until the last one goes.
	_ = p.Clear(ctx, CodeVoltageMains)
	_ = p.Clear(ctx, 0x1234) // not active
	if p.Register() != RegGeneric|RegCommunication {
		t.Error(p.Register())
	}
	_ = p.Clear(ctx, CodeCANOverrun)
	_ = p.Clear(ctx, CodeHeartbeat)
	e = ev.wait(t, time.Second, func(Event) bool { return true })
	if !e.IsReset() || e.Register != 0 {
		t.Errorf("event %+v", e)
	}
	if p.Register() != 0 {
		t.Error(p.Register())
	}
	_ = p.Raise(ctx, CodeGeneric, 0, [5]byte{})
	ev.wait(t, time.Second, func(Event) bool { return true })
	_ = p.ClearAll(ctx)
	ev.wait(t, time.Second, func(e Event) bool { return e.IsReset() })
	if err := p.ClearAll(ctx); err != nil {
		t.Error(err)
	}
	if err := p.Raise(ctx, 0, 0, [5]byte{}); err == nil {
		t.Error("code 0 raised")
	}
	_ = p.Send(ctx, Message{Code: 0xFF01})
	ev.wait(t, time.Second, func(e Event) bool { return e.Code == 0xFF01 })
	p.ClearHistory()
	if len(p.History()) != 0 {
		t.Error(p.History())
	}
	// Invalid COB-ID: errors recorded, nothing sent.
	p.SetCOBID(0x85 | canopen.COBInvalid)
	_ = p.Raise(ctx, CodeGeneric, 0, [5]byte{})
	ev.none(t, 50*time.Millisecond, func(Event) bool { return true })
	if st := p.Stats(); st.Sent != 7 || st.Suppressed != 1 {
		t.Errorf("stats %+v", st)
	}
	if last, ok := c.Last(5); !ok || last.Code != 0xFF01 {
		t.Error(last, ok)
	}
	if st := c.Stats(); st.Received != 7 || st.Resets != 2 {
		t.Errorf("consumer stats %+v", st)
	}
	// Malformed and foreign frames.
	f, _ := canbus.NewStandard(0x85, []byte{1})
	send(t, b, f) // b does not receive its own frames: use a
	send(t, a, f)
	f, _ = canbus.NewStandard(0x80, []byte{1, 2, 3})
	send(t, a, f)
	eventually(t, time.Second, func() bool { return c.Stats().Invalid == 2 })
	if err := c.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
	if _, err := NewProducer(a, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Error(err)
	}
	if _, err := NewProducer(a, 1, WithHistory(300)); err == nil {
		t.Error("history 300 accepted")
	}
}

func TestProducerInhibit(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	p, _ := NewProducer(a, 1, WithInhibit(30*time.Millisecond), WithCOBID(canopen.NewCOBID(0x123)))
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	for i := uint16(1); i <= 3; i++ {
		if err := p.Raise(ctx, 0x1000+i, 0, [5]byte{}); err != nil {
			t.Fatal(err)
		}
	}
	var times []time.Time
	for i := uint16(1); i <= 3; i++ {
		f := recv(t, sub, nil)
		if f.ID != 0x123 || uint16(f.Data[0])|uint16(f.Data[1])<<8 != 0x1000+i {
			t.Fatalf("frame %d: %v", i, f)
		}
		times = append(times, time.Now())
	}
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d < 25*time.Millisecond {
			t.Errorf("messages %v apart", d)
		}
	}
	// Sent is counted after the frame is on the bus.
	eventually(t, time.Second, func() bool { st := p.Stats(); return st.Queued == 2 && st.Sent == 3 })
	// The queue is bounded.
	p.SetInhibit(time.Hour)
	_ = p.Send(ctx, Message{Code: 1})
	var err error
	for i := 0; i <= maxQueue && err == nil; i++ {
		err = p.Send(ctx, Message{Code: 2})
	}
	if !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("full queue: %v", err)
	}
	_ = p.Close()
	if st := p.Stats(); st.Dropped < maxQueue {
		t.Errorf("stats %+v", st)
	}
	if err := p.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Error(err)
	}
}

func TestProducerDictionary(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	d, err := od.Standard(od.Config{NodeID: 7, ErrorHistory: 3})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProducer(a, 7, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	for _, c := range []uint16{0x2300, 0x4200, 0x8120, 0x5000} {
		_ = p.Raise(ctx, c, ClassRegister(c), [5]byte{})
	}
	if f := recv(t, sub, nil); f.ID != 0x87 {
		t.Fatal(f)
	}
	get := func(i uint16, s uint8) uint64 { v, _ := d.GetUint(i, s); return v }
	if r := get(od.IndexErrorRegister, 0); r != uint64(RegGeneric|RegCurrent|RegTemperature|RegCommunication) {
		t.Errorf("0x1001 = 0x%X", r)
	}
	if n := get(od.IndexErrorField, 0); n != 3 {
		t.Errorf("0x1003:0 = %d", n)
	}
	for i, want := range []uint64{0x5000, 0x8120, 0x4200} {
		if v := get(od.IndexErrorField, uint8(i+1)); v != want {
			t.Errorf("0x1003:%d = 0x%X", i+1, v)
		}
	}
	// 0x1003:0 accepts only 0, which clears the history.
	if err := d.Write(od.IndexErrorField, 0, []byte{2}); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("write 2: %v", err)
	}
	if err := d.Write(od.IndexErrorField, 0, []byte{0}); err != nil {
		t.Fatal(err)
	}
	if get(od.IndexErrorField, 0) != 0 || get(od.IndexErrorField, 1) != 0 || len(p.History()) != 0 {
		t.Error("history not cleared")
	}
	// 0x1014: the CAN-ID changes only while invalid; the invalid bit stops
	// the messages.
	le := func(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }
	if err := d.Write(od.IndexCOBIDEmcy, 0, le(0x90)); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("change valid COB-ID: %v", err)
	}
	if err := d.Write(od.IndexCOBIDEmcy, 0, le(0x87|uint32(canopen.COBInvalid))); err != nil {
		t.Fatal(err)
	}
	_ = p.Raise(ctx, 0x6100, 0, [5]byte{})
	if err := d.Write(od.IndexCOBIDEmcy, 0, le(0x90|uint32(canopen.COBInvalid))); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(od.IndexCOBIDEmcy, 0, le(0x90)); err != nil {
		t.Fatal(err)
	}
	_ = p.Raise(ctx, 0x6200, 0, [5]byte{})
	f := recv(t, sub, func(f *canbus.Frame) bool { return f.ID != 0x87 })
	if f.ID != 0x90 || f.Data[0] != 0x00 || f.Data[1] != 0x62 {
		t.Errorf("frame %v", f)
	}
	// 0x1015 in 100 µs units.
	if err := d.Write(od.IndexInhibitEmcy, 0, []byte{0xF4, 0x01}); err != nil { // 500 = 50 ms
		t.Fatal(err)
	}
	t0 := time.Now()
	_ = p.Send(ctx, Message{Code: 1})
	_ = p.Send(ctx, Message{Code: 2})
	recv(t, sub, nil)
	recv(t, sub, nil)
	if el := time.Since(t0); el < 45*time.Millisecond {
		t.Errorf("inhibit 50 ms: two messages in %v", el)
	}
	// Local Set of 0x1003:0 is not a remote write: no hook.
	_ = p.Raise(ctx, 0x6300, 0, [5]byte{})
	p.ClearHistory()
	if get(od.IndexErrorField, 0) != 0 {
		t.Error("ClearHistory")
	}

	if _, err := NewProducer(a, 7, WithDictionary(od.New(7))); err == nil {
		t.Error("dictionary without 0x1014 accepted")
	}
	// A dictionary without 0x1003 keeps the history in memory only.
	d2, _ := od.Standard(od.Config{NodeID: 3})
	p2, _ := NewProducer(a, 3, WithDictionary(d2))
	_ = p2.Raise(ctx, 0x1000, 0, [5]byte{})
	if len(p2.History()) != 1 || get(od.IndexErrorRegister, 0) == 0 {
		t.Error(p2.History())
	}
	if v, _ := d2.GetUint(od.IndexErrorRegister, 0); v != 1 {
		t.Error(v)
	}
}

func TestConsumerProducers(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c := NewConsumer(b, WithHandler(ev.add), WithProducer(3, canopen.NewCOBID(0x1234567)), WithProducer(4, 0x200))
	_ = c.Start()
	defer func() { _ = c.Close() }()
	p, _ := NewProducer(a, 3, WithCOBID(canopen.NewCOBID(0x1234567)))
	_ = p.Raise(context.Background(), 0x8250, 0, [5]byte{})
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if e.Node != 3 || e.COBID != canopen.NewCOBID(0x1234567) || e.Code != 0x8250 {
		t.Errorf("event %+v", e)
	}
	// The default identifier of node 3 is not listened to.
	p2, _ := NewProducer(a, 3)
	_ = p2.Raise(context.Background(), 0x1000, 0, [5]byte{})
	p4, _ := NewProducer(a, 4, WithCOBID(0x200))
	_ = p4.Raise(context.Background(), 0x1000, 0, [5]byte{})
	if e := ev.wait(t, time.Second, func(Event) bool { return true }); e.Node != 4 {
		t.Errorf("event %+v", e)
	}
	// Unit test of the source match for a non-matching frame.
	f, _ := canbus.NewStandard(0x300, nil)
	if _, _, ok := c.source(&f); ok {
		t.Error("foreign frame matched")
	}
}

func TestProducerSendError(t *testing.T) {
	fb := &failBus{Bus: testbus.Ports(t, 1)[0]}
	fb.fail.Store(true)
	p, _ := NewProducer(fb, 1, WithInhibit(5*time.Millisecond))
	_ = p.Start()
	defer func() { _ = p.Close() }()
	if err := p.Send(context.Background(), Message{Code: 1}); err == nil {
		t.Error("send error not returned")
	}
	_ = p.Send(context.Background(), Message{Code: 2}) // queued
	eventually(t, time.Second, func() bool { return p.Stats().Errors == 2 })
}

type failBus struct {
	*canbus.Bus
	fail atomic.Bool
}

func (f *failBus) Send(ctx context.Context, fr *canbus.Frame) error {
	if f.fail.Load() {
		return errors.New("send failed")
	}
	return f.Bus.Send(ctx, fr)
}
