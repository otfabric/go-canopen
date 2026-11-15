// SPDX-License-Identifier: MIT

package mpdo

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
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// TestCloseEndsInhibitWait: an MPDO waiting for the inhibit time when
// the producer is closed is not sent: the call ends with ErrClosed at
// once. Later calls fail with ErrClosed too.
func TestCloseEndsInhibitWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := testbus.Pair(t)
		da, db := device(t, 5), device(t, 6)
		setMPDO(t, da, true, 1, 0x281, ModeDAM)
		setMPDO(t, db, false, 1, 0x281, ModeDAM)
		if err := da.SetUint(0x1800, 3, 5000); err != nil { // inhibit time 500 ms
			t.Fatal(err)
		}
		p := newProducer(t, a, da, 1)
		c := newConsumer(t, b, db, 1)
		ctx := context.Background()
		if err := p.SendDAM(ctx, 6, idxU8, 0, []byte{1}); err != nil {
			t.Fatal(err)
		}
		waiting, queued := make(chan error, 1), make(chan error, 1)
		go func() { waiting <- p.SendDAM(ctx, 6, idxU8, 0, []byte{2}) }()
		synctest.Wait() // the second MPDO waits for the inhibit time
		go func() { queued <- p.SendDAM(ctx, 6, idxU8, 0, []byte{4}) }()
		synctest.Wait() // the third waits for its turn behind the second
		// A transmission waiting for its turn ends with its context.
		short, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		if err := p.SendDAM(short, 6, idxU8, 0, []byte{5}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("SendDAM waiting for its turn = %v, want its context's error", err)
		}
		start := time.Now()
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-waiting; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("SendDAM waiting for the inhibit time at Close = %v, want ErrClosed", err)
		}
		if err := <-queued; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("SendDAM waiting for its turn at Close = %v, want ErrClosed", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("the waiting MPDO ended %v after Close, want at once", d)
		}
		if err := p.SendDAM(ctx, 6, idxU8, 0, []byte{3}); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("SendDAM after Close = %v, want ErrClosed", err)
		}
		time.Sleep(time.Second) // past the inhibit time
		if st := p.Stats(); st.Sent != 1 {
			t.Errorf("%d MPDOs sent, want the first only", st.Sent)
		}
		if st := c.Stats(); st.Received != 1 || getU(db, idxU8, 0) != 1 {
			t.Errorf("consumer %+v, value %d: want the first MPDO only", st, getU(db, idxU8, 0))
		}
	})
}

// TestConsumerHandlers: the handlers of a consumer may close it, and a
// handler, a write hook or a watcher that does not return normally is
// logged while the consumer goes on.
func TestConsumerHandlers(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	a, b := testbus.Pair(t)
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	p := newProducer(t, a, da, 1)
	var received atomic.Int32
	closed := make(chan struct{})
	db.OnWrite(idxU32, 0, func(*od.Variable, []byte) error { panic("write hook of the application") })
	var c *Consumer
	c = newConsumer(t, b, db, 1, WithLogger(log),
		WithErrorHandler(func(pdo.ErrorEvent) { panic("error handler of the application") }),
		WithReceiveHandler(func(Event) {
			switch received.Add(1) {
			case 1:
				panic("receive handler of the application")
			case 2:
				if c.Mode() != ModeDAM || c.Stats().Applied != 2 {
					t.Error("the handler sees another mode, or not its MPDO")
				}
				_ = c.Close()
				close(closed)
			}
		}))
	cx := ctx(t)
	send := func(index uint16, sub uint8, v byte) {
		t.Helper()
		if err := p.SendDAM(cx, 6, index, sub, []byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	send(idxU32, 0, 1) // the write hook does not return normally: a write error
	send(0x5FFF, 0, 1) // no such object: the error handler is called
	send(idxU8, 0, 7)  // the receive handler does not return normally
	send(idxU8, 0, 8)  // the receive handler closes the consumer
	select {
	case <-closed:
	case <-cx.Done():
		t.Fatal("the MPDOs were not all handled, or Close called from the handler did not return")
	}
	_ = c.Close()
	if st := c.Stats(); st.WriteErrors != 1 || st.Applied != 2 || st.NoObject != 1 {
		t.Errorf("%+v, want 1 write error, 1 missing object and 2 applied", st)
	}
	mu.Lock()
	entries := buf.String()
	mu.Unlock()
	for _, want := range []string{"write hook", "error handler", "receive handler"} {
		if !strings.Contains(entries, want+" of the application") {
			t.Errorf("no log entry for the %s", want)
		}
	}
	// The error handler is called twice: the emergency appears with the
	// MPDO for the missing object and is resolved by the next one.
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 4 || !strings.Contains(entries, "stack=") {
		t.Errorf("%d failures logged, want 4 with the stack", n)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestConsumerEveryCloseWaits: while the consumer's goroutine is inside
// a handler, no Close returns; all return when the handler has returned.
// A closed consumer stays disabled.
func TestConsumerEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := testbus.Pair(t)
		da, db := device(t, 5), device(t, 6)
		setMPDO(t, da, true, 1, 0x281, ModeDAM)
		setMPDO(t, db, false, 1, 0x281, ModeDAM)
		p := newProducer(t, a, da, 1)
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		c := newConsumer(t, b, db, 1, WithReceiveHandler(func(Event) {
			close(entered)
			<-held
		}))
		if err := p.SendDAM(context.Background(), 6, idxU8, 0, []byte{1}); err != nil {
			t.Fatal(err)
		}
		<-entered
		const callers = 3
		returned := make(chan struct{}, callers)
		for range callers {
			go func() {
				_ = c.Close()
				returned <- struct{}{}
			}()
		}
		synctest.Wait()
		if n := len(returned); n != 0 {
			t.Fatalf("%d of %d Close calls returned while the handler was running", n, callers)
		}
		release()
		for range callers {
			<-returned
		}
		c.SetEnabled(true)
		if err := c.Start(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Start after Close = %v, want ErrClosed", err)
		}
	})
}
