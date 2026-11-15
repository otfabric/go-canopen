// SPDX-License-Identifier: MIT

package pdo

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

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

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

func (w *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// failures returns how many entries report a callback that did not
// return normally, and whether each of the values was logged.
func (w *logBuffer) failures(values ...string) (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.b.String()
	all := strings.Contains(s, "stack=") || len(values) == 0
	for _, v := range values {
		all = all && strings.Contains(s, v)
	}
	return strings.Count(s, `msg="callback did not return normally"`), all
}

// TestRPDOEveryCloseWaits: while the RPDO's goroutine is inside a
// handler, no Close returns; all return when the handler has returned.
func TestRPDOEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := testbus.Pair(t)
		d := device(t, 5)
		configure(t, d, false, 1, Config{COBID: 0x205, TransmissionType: 255, Mapping: Mapping{{idxU8, 0, 8}}})
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		rp := newRPDO(t, a, d, 1, WithReceiveHandler(func([]byte) {
			close(entered)
			<-held
		}))
		rp.SetEnabled(true)
		f, _ := canbus.NewStandard(0x205, []byte{0x42})
		if err := b.Send(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
		<-entered
		const callers = 3
		returned := make(chan struct{}, callers)
		for range callers {
			go func() {
				_ = rp.Close()
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
	})
}

// TestRPDOHandlers: the handlers of an RPDO may close it, and a handler,
// a write hook or a watcher that does not return normally is logged
// while the RPDO goes on.
func TestRPDOHandlers(t *testing.T) {
	var log logBuffer
	a, b := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, false, 1, Config{COBID: 0x205, TransmissionType: 255, Mapping: Mapping{{idxU8, 0, 8}, {idxI16, 0, 16}}})
	var hooked, received atomic.Int32
	d.OnWrite(idxI16, 0, func(*od.Variable, []byte) error {
		if hooked.Add(1) == 1 {
			panic("write hook of the application")
		}
		return nil
	})
	closed := make(chan struct{})
	var rp *RPDO
	rp = newRPDO(t, a, d, 1, WithLogger(log.logger()),
		WithErrorHandler(func(ErrorEvent) { panic("error handler of the application") }),
		WithReceiveHandler(func([]byte) {
			switch received.Add(1) {
			case 1:
				panic("receive handler of the application")
			case 2:
				if rp.Config().COBID != 0x205 || rp.Stats().Applied != 2 {
					t.Error("the handler sees another configuration, or not its PDO")
				}
				_ = rp.Close()
				close(closed)
			}
		}))
	rp.SetEnabled(true)
	cx := ctx(t)
	send := func(data ...byte) {
		t.Helper()
		f, _ := canbus.NewStandard(0x205, data)
		if err := b.Send(cx, &f); err != nil {
			t.Fatal(err)
		}
	}
	send(1)       // too short: the error handler is called
	send(2, 3, 0) // the write hook, then the receive handler do not return normally
	send(4, 5, 0) // the error is resolved; the receive handler closes the RPDO
	select {
	case <-closed:
	case <-cx.Done():
		t.Fatal("the PDOs were not all handled, or Close called from the handler did not return")
	}
	_ = rp.Close()
	if st := rp.Stats(); st.Applied != 2 || st.WriteErrors != 1 || st.Short != 1 {
		t.Errorf("%+v, want 2 applied, 1 write error, 1 short", st)
	}
	if v := u8(d, idxU8, 0); v != 4 {
		t.Errorf("object %#x, want the value of the last PDO", v)
	}
	// The error handler is called for the short PDO and when the next
	// PDO resolves the error.
	if n, all := log.failures("write hook of the application", "error handler of the application", "receive handler of the application"); n != 4 || !all {
		t.Errorf("%d failures logged (all values and the stack: %v), want 4", n, all)
	}
}

// TestTPDOReadHookDoesNotReturnNormally: a read hook of a mapped object
// that does not return normally counts as a read error; the TPDO is sent
// again once the hook returns.
func TestTPDOReadHookDoesNotReturnNormally(t *testing.T) {
	var log logBuffer
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	var fail atomic.Bool
	fail.Store(true)
	d.OnRead(idxU8, 0, func(_ *od.Variable, stored []byte) ([]byte, error) {
		if fail.Load() {
			panic("read hook of the application")
		}
		return stored, nil
	})
	tp := newTPDO(t, a, d, 1, WithLogger(log.logger()))
	tp.SetEnabled(true)
	if err := tp.Transmit(ctx(t)); !errors.Is(err, canopen.AbortGeneral) {
		t.Errorf("Transmit through the read hook = %v, want AbortGeneral", err)
	}
	tp.OnSync(0)
	if st := tp.Stats(); st.Sent != 0 || st.ReadErrors != 2 {
		t.Errorf("%+v, want nothing sent and 2 read errors", st)
	}
	fail.Store(false)
	if err := tp.Transmit(ctx(t)); err != nil {
		t.Errorf("Transmit after the failures: %v", err)
	}
	if n, all := log.failures("read hook of the application"); n != 2 || !all {
		t.Errorf("%d failures logged (value and stack: %v), want 2", n, all)
	}
}

// TestListenerHandler: the handler of a listener may close it, a second
// Close waits for the handler, and a handler that does not return
// normally is logged while the next PDO is still delivered.
func TestListenerHandler(t *testing.T) {
	var log logBuffer
	a, b := testbus.Pair(t)
	dec, err := NewDecoder(Mapping{{idxU8, 0, 8}}, device(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	closed, held := make(chan struct{}), make(chan struct{})
	var l *Listener
	l = NewListener(a, 0x185, dec, func(*canbus.Frame, []Value) {
		switch received.Add(1) {
		case 1:
			panic("listener handler of the application")
		case 2:
			if l.Stats().Received != 2 {
				t.Error("the handler does not see its PDO counted")
			}
			_ = l.Close()
			close(closed)
			<-held
		}
	}, WithLogger(log.logger()))
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	cx := ctx(t)
	for i := range 2 {
		f, _ := canbus.NewStandard(0x185, []byte{byte(i)})
		if err := b.Send(cx, &f); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-closed:
	case <-cx.Done():
		t.Fatal("the second PDO was not delivered, or Close called from the handler did not return")
	}
	if err := l.Request(cx); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Request after Close = %v, want ErrClosed", err)
	}
	other := make(chan struct{})
	go func() {
		_ = l.Close()
		close(other)
	}()
	close(held)
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("Close on another goroutine did not return after the handler")
	}
	if n, all := log.failures("listener handler of the application"); n != 1 || !all {
		t.Errorf("%d failures logged (value and stack: %v), want 1", n, all)
	}
}
