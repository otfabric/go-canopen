// SPDX-License-Identifier: MIT

package usdo

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
	"github.com/otfabric/go-canopen/od"
)

// TestClientClosed: after Close every transfer fails with ErrClosed (a
// client that was never started still reports ErrNotStarted), and a
// transfer in progress when the client is closed ends with ErrClosed
// instead of waiting for its timeout.
func TestClientClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fdPorts(t, 2)
		c := startClient(t, p[0], 1, WithTimeout(time.Hour))
		ctx := context.Background()

		// No server answers: the upload waits for a response.
		upload := make(chan error, 1)
		go func() { _, err := c.Upload(ctx, 5, idxU8, 0); upload <- err }()
		multi := make(chan error, 1)
		go func() { _, err := c.UploadMulti(ctx, Broadcast, idxU8, 0); multi <- err }()
		synctest.Wait() // both wait for a response
		start := time.Now()
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		for name, ch := range map[string]chan error{"Upload": upload, "UploadMulti": multi} {
			if err := <-ch; !errors.Is(err, canopen.ErrClosed) {
				t.Errorf("%s in progress at Close = %v, want ErrClosed", name, err)
			}
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("the transfers ended %v after Close, want at once", d)
		}

		if _, err := c.Upload(ctx, 5, idxU8, 0); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Upload after Close = %v, want ErrClosed", err)
		}
		if err := c.Download(ctx, 5, idxU8, 0, []byte{1}); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Download after Close = %v, want ErrClosed", err)
		}
		if _, err := c.DownloadMulti(ctx, Broadcast, idxU8, 0, []byte{1}); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("DownloadMulti after Close = %v, want ErrClosed", err)
		}
		if _, err := c.UploadMulti(ctx, Broadcast, idxU8, 0); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("UploadMulti after Close = %v, want ErrClosed", err)
		}
		if err := c.Start(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Start after Close = %v, want ErrClosed", err)
		}
	})
}

// TestServerEveryCloseWaits: while the server's goroutine is inside a
// dictionary hook, no Close returns; all return when the hook has
// returned.
func TestServerEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fdPorts(t, 2)
		s, d := startServer(t, p[0], 5)
		c := startClient(t, p[1], 1, WithTimeout(time.Hour))
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		d.OnWrite(idxU8, 0, func(*od.Variable, []byte) error {
			close(entered)
			<-held
			return nil
		})
		download := make(chan error, 1)
		go func() { download <- c.Download(context.Background(), 5, idxU8, 0, []byte{1}) }()
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

// TestServerHookClosesServer: a dictionary hook may call Close, which
// returns to it; a Close on another goroutine waits for the hook.
func TestServerHookClosesServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := fdPorts(t, 2)
		s, d := startServer(t, p[0], 5)
		c := startClient(t, p[1], 1, WithTimeout(time.Hour))
		closed, held := make(chan struct{}), make(chan struct{})
		d.OnWrite(idxU8, 0, func(*od.Variable, []byte) error {
			if s.NodeID() != 5 || s.Stats().Requests == 0 {
				t.Error("the hook sees another node-ID, or no request")
			}
			_ = s.Close()
			close(closed)
			<-held
			return nil
		})
		download := make(chan error, 1)
		go func() { download <- c.Download(context.Background(), 5, idxU8, 0, []byte{1}) }()
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

// lockedBuffer is a log destination a test reads while services write.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestServerHookDoesNotReturnNormally: hooks and watchers that do not
// return normally are stopped where the server called them: the transfer
// is aborted, the failure logged with its stack, and the server serves
// the next request.
func TestServerHookDoesNotReturnNormally(t *testing.T) {
	var log lockedBuffer
	p := fdPorts(t, 2)
	_, d := startServer(t, p[0], 5, WithLogger(slog.New(slog.NewTextHandler(&log, nil))))
	c := startClient(t, p[1], 1, WithTimeout(10*time.Second))
	ctx := tctx(t)
	fail := true
	d.OnRead(idxU32, 0, func(_ *od.Variable, stored []byte) ([]byte, error) {
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
		if fail && v.Index == idxDomain {
			panic("watcher of the application")
		}
	})
	aborted := func(what string, err error) {
		t.Helper()
		var ae *AbortError
		if !errors.As(err, &ae) || !ae.Remote {
			t.Errorf("%s = %v, want an abort of the server", what, err)
		}
	}
	_, err := c.Upload(ctx, 5, idxU32, 0)
	aborted("upload through a read hook", err)
	aborted("expedited download through a write hook", c.Download(ctx, 5, idxU8, 0, []byte{1}))
	aborted("segmented download with a watcher", c.Download(ctx, 5, idxDomain, 0, pattern(300, 1)))
	entries := log.String()
	for _, want := range []string{"read hook of the application", "write hook of the application", "watcher of the application"} {
		if !strings.Contains(entries, want) {
			t.Errorf("no log entry for the %s", want)
		}
	}
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 3 || !strings.Contains(entries, "stack=") {
		t.Errorf("%d entries \"callback did not return normally\", want 3 with the stack:\n%s", n, entries)
	}
	fail = false
	if _, err := c.Upload(ctx, 5, idxU32, 0); err != nil {
		t.Errorf("upload after the failures: %v", err)
	}
	if err := c.Download(ctx, 5, idxU8, 0, []byte{2}); err != nil {
		t.Errorf("download after the failures: %v", err)
	}
}

// TestCloseEndsBulkDownload: a bulk download that waits between two
// segments when the client is closed sends no further segment and ends
// with ErrClosed.
func TestCloseEndsBulkDownload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := fdPorts(t, 2)
		c := startClient(t, ports[0], 1, WithTimeout(time.Second))
		sub, err := ports[1].Subscribe(canbus.WithFilters(filter(canopen.FuncSDORx)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sub.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var segments atomic.Int32
		served := make(chan struct{})
		go func() { // server 9: accepts the bulk download, asks for 500 ms per segment
			defer close(served)
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				m, err := Decode(f.Payload())
				if err != nil {
					continue
				}
				switch m.Cmd {
				case CmdBulkInit:
					r := Message{Dest: 1, Session: m.Session, Cmd: CmdBulkInitOK, Index: idxDomain, TProc: 5000}
					b, _ := r.Encode()
					out, _ := canbus.NewFD(canopen.FuncSDOTx+9, false, false, canbus.PadFD(b, 0))
					_ = ports[1].Send(ctx, &out)
				case CmdBulkSegment, CmdBulkLast:
					segments.Add(1)
				default: // nothing else is sent
				}
			}
		}()
		download := make(chan error, 1)
		go func() {
			download <- c.Download(context.Background(), 9, idxDomain, 0, make([]byte, 4*SegmentSize), UseBulk(true))
		}()
		synctest.Wait() // the first segment is sent; the client waits for the pace
		if n := segments.Load(); n != 1 {
			t.Fatalf("%d segments sent before the first pace, want 1", n)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-download; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("bulk download in progress at Close = %v, want ErrClosed", err)
		}
		time.Sleep(5 * time.Second)
		if n := segments.Load(); n != 1 {
			t.Errorf("%d segments sent, want none after Close", n-1)
		}
		cancel()
		<-served
	})
}
