// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// TestServerEveryCloseWaits: while the server's goroutine is inside a
// dictionary hook, no Close returns, a second one no more than the first;
// all return when the hook has returned.
func TestServerEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, s, d := setup(t, nil)
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		d.OnWrite(idxU8, 0, func(*od.Variable, []byte) error {
			close(entered)
			<-held
			return nil
		})
		download := make(chan error, 1)
		go func() { download <- c.Download(context.Background(), idxU8, 0, []byte{1}) }()
		<-entered

		const callers = 3
		returned := make(chan struct{}, callers)
		for range callers {
			go func() {
				_ = s.Close()
				returned <- struct{}{}
			}()
		}
		synctest.Wait()
		if n := len(returned); n != 0 {
			t.Fatalf("%d of %d Close calls returned while the hook was running", n, callers)
		}
		release()
		for range callers {
			<-returned
		}
		_ = c.Close()
		<-download
	})
}

// TestServerHookClosesServer: a dictionary hook may call the server's
// methods and Close. Close returns to the hook, and the server's
// goroutine ends when the hook has returned; a Close on another goroutine
// waits for that.
func TestServerHookClosesServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, s, d := setup(t, nil)
		closed, held := make(chan struct{}), make(chan struct{})
		d.OnWrite(idxU8, 0, func(*od.Variable, []byte) error {
			if !s.Enabled() || s.Stats().Requests == 0 {
				t.Error("the hook sees a disabled server, or no request")
			}
			_ = s.Close()
			close(closed)
			<-held
			return nil
		})
		download := make(chan error, 1)
		go func() { download <- c.Download(context.Background(), idxU8, 0, []byte{1}) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("Close called from the hook did not return")
		}
		other := make(chan struct{})
		go func() {
			_ = s.Close()
			close(other)
		}()
		synctest.Wait()
		select {
		case <-other:
			t.Fatal("Close on another goroutine returned while the hook was running")
		default:
		}
		close(held)
		<-other
		_ = c.Close()
		<-download
	})
}

// TestServerHookDoesNotReturnNormally: a read hook, a write hook and a
// watcher that do not return normally are stopped where the server
// called them. The transfer is aborted, the failure is logged with its
// stack, and the server serves the next request.
func TestServerHookDoesNotReturnNormally(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(lockedWriter{&mu, &buf}, nil))
	c, _, d := setup(t, []ServerOption{WithLogger(log)}, WithTimeout(10*time.Second))
	ctx := ctxT(t)
	fail := true
	d.OnRead(idxI16, 0, func(_ *od.Variable, stored []byte) ([]byte, error) {
		if fail {
			panic("read hook of the application")
		}
		return stored, nil
	})
	d.OnWrite(idxU8, 0, func(*od.Variable, []byte) error {
		if fail {
			panic("write hook of the application")
		}
		return nil
	})
	d.Watch(func(v *od.Variable, _ []byte) {
		if fail && v.Index == idxReal {
			panic("watcher of the application")
		}
	})
	_, err := c.Upload(ctx, idxI16, 0)
	wantAbort(t, err, canopen.AbortGeneral, true)
	wantAbort(t, c.Download(ctx, idxU8, 0, []byte{1}), canopen.AbortGeneral, true)
	wantAbort(t, c.Download(ctx, idxReal, 0, []byte{0, 0, 0x80, 0x3F}), canopen.AbortGeneral, true)
	if err := c.Download(ctx, idxDomain, 0, pattern(100)); err != nil { // an object without failing hooks
		t.Errorf("download between the failures: %v", err)
	}
	mu.Lock()
	entries := buf.String()
	mu.Unlock()
	for _, want := range []string{"read hook of the application", "write hook of the application", "watcher of the application"} {
		if !strings.Contains(entries, want) {
			t.Errorf("no log entry for the %s", want)
		}
	}
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 3 {
		t.Errorf("%d entries \"callback did not return normally\", want 3:\n%s", n, entries)
	}
	if !strings.Contains(entries, "level=ERROR") || !strings.Contains(entries, "stack=") || !strings.Contains(entries, `callback="dictionary hook"`) {
		t.Errorf("entries lack the level, the callback or the stack:\n%s", entries)
	}
	// The server goes on.
	fail = false
	if _, err := c.Upload(ctx, idxI16, 0); err != nil {
		t.Errorf("upload after the failures: %v", err)
	}
	if err := c.Download(ctx, idxU8, 0, []byte{2}); err != nil {
		t.Errorf("download after the failures: %v", err)
	}
}

// lockedWriter serialises the writes of a log handler with the test's
// reads.
type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// TestLoggerDoesNotReturnNormally: a log handler that does not return
// normally loses its entry; the server and the client go on.
func TestLoggerDoesNotReturnNormally(t *testing.T) {
	log := slog.New(failingHandler{})
	c, _, _ := setup(t, []ServerOption{WithLogger(log)}, WithLogger(log), WithTimeout(10*time.Second))
	ctx := ctxT(t)
	// An abort makes the server and the client write a debug entry each.
	_, err := c.Upload(ctx, 0x5FFF, 0)
	wantAbort(t, err, canopen.AbortNoObject, true)
	if err := c.Download(ctx, idxU8, 0, []byte{3}); err != nil {
		t.Errorf("download after the logger failed: %v", err)
	}
}

// failingHandler is a log handler none of whose calls returns normally.
type failingHandler struct{}

func (failingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (failingHandler) Handle(context.Context, slog.Record) error {
	panic("log handler of the application")
}
func (h failingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h failingHandler) WithGroup(string) slog.Handler      { return h }
