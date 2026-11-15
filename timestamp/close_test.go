// SPDX-License-Identifier: MIT

package timestamp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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
	first := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
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
		if err := p.Send(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		ev.wait(t, 10*time.Second, func(Event) bool { return true })
		_ = p.Close()
		if err := p.Send(context.Background(), first.Add(time.Hour)); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("started %v: Send after Close = %v, want ErrClosed", started, err)
		}
		if st := p.Stats(); st.Sent != 1 || st.Errors != 0 {
			t.Errorf("started %v: %+v after a Send on a closed producer", started, st)
		}
	}
	// What a closed producer sent would arrive before this message.
	live, _ := NewProducer(a)
	_ = live.Send(context.Background(), first.Add(2*time.Hour))
	if e := ev.wait(t, 10*time.Second, func(Event) bool { return true }); !e.Time.Equal(first.Add(2 * time.Hour)) {
		t.Errorf("a TIME message of a closed producer arrived: %+v", e)
	}
}

// logBuffer is a log destination a test reads while services write.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *logBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *logBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestConsumerHandler: the handler may close the consumer, and a handler
// that does not return normally is logged while the next message is
// still delivered.
func TestConsumerHandler(t *testing.T) {
	var log logBuffer
	a, b := testbus.Pair(t)
	var events atomic.Int32
	closed := make(chan struct{})
	var c *Consumer
	c, err := NewConsumer(b, WithLogger(slog.New(slog.NewTextHandler(&log, nil))), WithHandler(func(e Event) {
		switch events.Add(1) {
		case 1:
			panic("time handler of the application")
		case 2:
			if last, ok := c.Last(); !ok || !last.Time.Equal(e.Time) {
				t.Error("the handler sees another last message")
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
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	_ = p.Send(context.Background(), now)
	_ = p.Send(context.Background(), now.Add(time.Second))
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second message was not delivered, or Close called from the handler did not return")
	}
	_ = c.Close()
	entries := log.String()
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 1 || !strings.Contains(entries, "time handler of the application") || !strings.Contains(entries, "stack=") {
		t.Errorf("log: %s", entries)
	}
}

// TestClockDoesNotReturnNormally: a period whose clock does not return
// normally sends nothing; the next period sends again.
func TestClockDoesNotReturnNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var log logBuffer
		a, _ := testbus.Pair(t)
		calls := 0
		p, err := NewProducer(a, WithLogger(slog.New(slog.NewTextHandler(&log, nil))), WithPeriod(10*time.Millisecond),
			WithClock(func() time.Time {
				if calls++; calls == 1 {
					panic("clock of the application")
				}
				return time.Now()
			}))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(35 * time.Millisecond)
		_ = p.Close()
		if st := p.Stats(); st.Sent != 2 {
			t.Errorf("%+v, want 2 of 3 periods sent", st)
		}
		if entries := log.String(); !strings.Contains(entries, "clock of the application") || !strings.Contains(entries, `callback=clock`) {
			t.Errorf("log: %s", entries)
		}
	})
}
