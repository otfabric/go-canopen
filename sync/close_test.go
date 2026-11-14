// SPDX-License-Identifier: MIT

package syncobj

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

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// TestProducerClosed: after Close, Send fails with ErrClosed and sends
// nothing, whether or not the producer was started.
func TestProducerClosed(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c, err := NewConsumer(b, WithHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, started := range []bool{true, false} {
		p, err := NewProducer(a)
		if err != nil {
			t.Fatal(err)
		}
		if started {
			if err := p.Start(); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Send(context.Background()); err != nil {
			t.Fatal(err)
		}
		ev.wait(t, 10*time.Second, func(Event) bool { return true })
		_ = p.Close()
		if err := p.Send(context.Background()); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("started %v: Send after Close = %v, want ErrClosed", started, err)
		}
		if st := p.Stats(); st.Sent != 1 || st.Errors != 0 {
			t.Errorf("started %v: %+v after a Send on a closed producer", started, st)
		}
	}
	// What a closed producer sent would arrive before this SYNC.
	live, _ := NewProducer(a, WithOverflow(10))
	_ = live.Send(context.Background())
	if e := ev.wait(t, 10*time.Second, func(Event) bool { return true }); e.Counter != 1 {
		t.Errorf("a SYNC without counter arrived after Close: %+v", e)
	}
}

// TestConsumerHandler: the handler may close the consumer, and a handler
// that does not return normally is logged while the next SYNC is still
// delivered.
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
	c, err := NewConsumer(b, WithLogger(log), WithHandler(func(e Event) {
		switch events.Add(1) {
		case 1:
			panic("sync handler of the application")
		case 2:
			if last, ok := c.Last(); !ok || last.Counter != e.Counter {
				t.Error("the handler sees another last SYNC")
			}
			_ = c.Close()
			close(closed)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	p, _ := NewProducer(a)
	_ = p.Send(context.Background())
	_ = p.Send(context.Background())
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second SYNC was not delivered, or Close called from the handler did not return")
	}
	_ = c.Close()
	mu.Lock()
	entries := buf.String()
	mu.Unlock()
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 1 || !strings.Contains(entries, "sync handler of the application") || !strings.Contains(entries, "stack=") {
		t.Errorf("log: %s", entries)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
