// SPDX-License-Identifier: MIT

package emcy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// TestProducerClosed: after Close the producer sends, queues and records
// nothing: Raise, Clear, ClearAll, Send and SendFD fail with ErrClosed.
// Messages queued at Close are counted as dropped.
func TestProducerClosed(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c := NewConsumer(b, WithHandler(ev.add))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, started := range []bool{true, false} {
		p, err := NewProducer(a, 5, WithInhibit(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if started {
			if err := p.Start(); err != nil {
				t.Fatal(err)
			}
		}
		ctx := context.Background()
		// The first message is sent, the second waits for the inhibit
		// time.
		if err := errors.Join(p.Raise(ctx, 0x5000, RegGeneric, [5]byte{}), p.Raise(ctx, 0x6000, RegGeneric, [5]byte{})); err != nil {
			t.Fatal(err)
		}
		ev.wait(t, 10*time.Second, func(e Event) bool { return e.Code == 0x5000 })
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		before := p.Stats()
		if before.Sent != 1 || before.Queued != 1 || before.Dropped != 1 {
			t.Errorf("started %v: at Close %+v, want 1 sent, 1 queued and dropped", started, before)
		}
		for name, err := range map[string]error{
			"Raise":    p.Raise(ctx, 0x7000, RegGeneric, [5]byte{}),
			"Clear":    p.Clear(ctx, 0x5000),
			"ClearAll": p.ClearAll(ctx),
			"Send":     p.Send(ctx, Message{Code: 0x7000}),
			"SendFD":   p.SendFD(ctx, FDMessage{Code: 0x7000}),
		} {
			if !errors.Is(err, canopen.ErrClosed) {
				t.Errorf("started %v: %s after Close = %v, want ErrClosed", started, name, err)
			}
		}
		if after := p.Stats(); after != before {
			t.Errorf("started %v: counters changed after Close: %+v, before %+v", started, after, before)
		}
		if act := p.Active(); len(act) != 2 {
			t.Errorf("started %v: active errors %X changed after Close", started, act)
		}
		if err := p.Start(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("started %v: Start after Close = %v", started, err)
		}
	}
}

// TestEventOwnsItsFDMessage: every event the consumer hands out has an
// FD message of its own. What the handler, or a caller of Last, changes
// in its event reaches no other.
func TestEventOwnsItsFDMessage(t *testing.T) {
	p := testbus.Ports(t, 2, virtual.WithFD())
	handled := make(chan Event, 1)
	c := NewConsumer(p[1], WithFD(false), WithHandler(func(e Event) {
		e.FD.Status = 0xEE // the handler modifies its event
		e.FD.Specific[0] = 0xEE
		handled <- e
	}))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	prod, err := NewProducer(p[0], 5, WithFD(false))
	if err != nil {
		t.Fatal(err)
	}
	sent := FDMessage{Code: 0x5000, Register: RegGeneric, Status: 0x01, Specific: [5]byte{1, 2, 3, 4, 5}}
	if err := prod.SendFD(context.Background(), sent); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	first, ok := c.Last(5)
	if !ok || first.FD == nil {
		t.Fatalf("Last = %+v, %v", first, ok)
	}
	if first.FD.Status != 0x01 || first.FD.Specific != sent.Specific {
		t.Errorf("Last after the handler modified its event: status %#x, specific % X, want the message received (0x01, % X)",
			first.FD.Status, first.FD.Specific, sent.Specific)
	}
	first.FD.Status = 0xDD // a caller of Last modifies its event
	if second, _ := c.Last(5); second.FD == first.FD || second.FD.Status != 0x01 {
		t.Errorf("Last after a caller modified its event: status %#x (same message: %v), want 0x01 in a message of its own",
			second.FD.Status, second.FD == first.FD)
	}
}

// TestConsumerHandler: the handler may close the consumer, and a handler
// that does not return normally is logged while the next message is
// still delivered.
func TestConsumerHandler(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	a, b := testbus.Pair(t)
	var events atomic.Int32
	closed := make(chan struct{})
	var c *Consumer
	c = NewConsumer(b, WithLogger(log), WithHandler(func(e Event) {
		switch events.Add(1) {
		case 1:
			panic("emcy handler of the application")
		case 2:
			if last, ok := c.Last(e.Node); !ok || last.Code != e.Code {
				t.Error("the handler sees another last message")
			}
			_ = c.Close()
			close(closed)
		}
	}))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	p, _ := NewProducer(a, 5)
	ctx := context.Background()
	_ = p.Raise(ctx, 0x5000, RegGeneric, [5]byte{})
	_ = p.Raise(ctx, 0x6000, RegGeneric, [5]byte{})
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second message was not delivered, or Close called from the handler did not return")
	}
	_ = c.Close()
	mu.Lock()
	entries := buf.String()
	mu.Unlock()
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 1 || !strings.Contains(entries, "emcy handler of the application") || !strings.Contains(entries, "stack=") {
		t.Errorf("log: %s", entries)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestClockDoesNotReturnNormally: a clock of a CANopen FD producer that
// does not return normally gives a message without time.
func TestClockDoesNotReturnNormally(t *testing.T) {
	p := testbus.Ports(t, 2, virtual.WithFD())
	ev := newEvents[Event]()
	c := NewConsumer(p[1], WithFD(false), WithHandler(ev.add))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	prod, err := NewProducer(p[0], 5, WithFD(false), WithFDClock(func() time.Time { panic("clock of the application") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := prod.Raise(context.Background(), 0x5000, RegGeneric, [5]byte{}); err != nil {
		t.Fatal(err)
	}
	e := ev.wait(t, 10*time.Second, func(e Event) bool { return e.FD != nil })
	if e.FD.Time.Days != 0 || e.FD.Time.Ms != 0 {
		t.Errorf("time %+v, want 0", e.FD.Time)
	}
}
