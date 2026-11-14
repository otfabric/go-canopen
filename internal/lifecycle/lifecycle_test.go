// SPDX-License-Identifier: MIT

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/otfabric/go-canopen"
)

// untilDone is a loop that ends with the service context.
func untilDone(ctx context.Context) { <-ctx.Done() }

func idle(context.Context) (func(context.Context), error) { return untilDone, nil }

func TestStartClose(t *testing.T) {
	var s Service
	if s.Closed() || s.Running() {
		t.Fatal("a new service is closed or running")
	}
	done := s.Done()
	if err := s.Start(idle); err != nil {
		t.Fatal(err)
	}
	if !s.Running() {
		t.Error("Running() = false after Start")
	}
	select {
	case <-done:
		t.Fatal("Done is closed before Close")
	default:
	}
	if err := s.Start(idle); !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	s.Close()
	if !s.Closed() || s.Running() {
		t.Error("after Close: Closed() = false or Running() = true")
	}
	select {
	case <-done:
	default:
		t.Error("Done is not closed after Close")
	}
	s.Close()
	if err := s.Start(idle); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestCloseBeforeStart(t *testing.T) {
	var s Service
	s.Close()
	select {
	case <-s.Done():
	default:
		t.Error("Done is not closed after Close of a service that was not started")
	}
	if err := s.Start(idle); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestSetupFails(t *testing.T) {
	var s Service
	errSetup := errors.New("no bus")
	var sctx context.Context
	err := s.Start(func(ctx context.Context) (func(context.Context), error) {
		sctx = ctx
		return nil, errSetup
	})
	if !errors.Is(err, errSetup) {
		t.Fatalf("Start = %v, want the setup error", err)
	}
	if sctx.Err() == nil {
		t.Error("the service context of a failed Start is not ended")
	}
	if s.Closed() {
		t.Error("a failed Start closed the service")
	}
	if err := s.Start(idle); err != nil {
		t.Errorf("Start after a failed Start = %v", err)
	}
	s.Close()
}

// TestEveryCloseWaits: while the service goroutine is still inside a
// handler, no Close returns, the first no more than a later one; all of
// them return when the handler does.
func TestEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s Service
		release := make(chan struct{})
		if err := s.Start(func(context.Context) (func(context.Context), error) {
			return func(ctx context.Context) {
				<-ctx.Done()
				<-release // a handler that is still running
			}, nil
		}); err != nil {
			t.Fatal(err)
		}
		const callers = 3
		returned := make(chan int, callers)
		for i := range callers {
			go func() {
				s.Close()
				returned <- i
			}()
		}
		synctest.Wait()
		if n := len(returned); n != 0 {
			t.Fatalf("%d of %d Close calls returned while the handler was running", n, callers)
		}
		close(release)
		synctest.Wait()
		if n := len(returned); n != callers {
			t.Fatalf("%d of %d Close calls returned after the handler ended", n, callers)
		}
	})
}

// TestCloseFromServiceGoroutine: a Close on the service's own goroutine
// (a handler closing its service) returns; a Close on another goroutine
// waits until that handler has returned.
func TestCloseFromServiceGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s Service
		closed, release := make(chan struct{}), make(chan struct{})
		if err := s.Start(func(context.Context) (func(context.Context), error) {
			return func(ctx context.Context) {
				s.Close() // returns: it runs on the goroutine it would wait for
				if ctx.Err() == nil {
					t.Error("Close on the service goroutine did not end the service context")
				}
				close(closed)
				<-release
			}, nil
		}); err != nil {
			t.Fatal(err)
		}
		<-closed
		other := make(chan struct{})
		go func() {
			s.Close()
			close(other)
		}()
		synctest.Wait()
		select {
		case <-other:
			t.Fatal("Close on another goroutine returned while the handler was running")
		default:
		}
		close(release)
		<-other
	})
}

// TestCloseFromStart: a Close from a handler that Start's setup calls
// returns at once and takes effect before Start returns.
func TestCloseFromStart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var s Service
		errSetup := errors.New("no bus")
		ended := make(chan struct{})
		err := s.Start(func(context.Context) (func(context.Context), error) {
			s.Close()
			if !s.Closed() || s.Running() {
				t.Error("after Close from Start: Closed() = false or Running() = true")
			}
			select {
			case <-s.Done():
			default:
				t.Error("Done is not closed after Close from Start")
			}
			if fail {
				return nil, errSetup
			}
			return func(ctx context.Context) { <-ctx.Done(); close(ended) }, nil
		})
		switch {
		case fail && !errors.Is(err, errSetup):
			t.Errorf("Start = %v, want the setup error", err)
		case !fail && !errors.Is(err, canopen.ErrClosed):
			t.Errorf("Start = %v, want ErrClosed", err)
		}
		if !fail {
			select {
			case <-ended:
			default:
				t.Error("Start returned before the service goroutine ended")
			}
		}
		if err := s.Start(idle); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("setup fails %v: Start after Close from Start = %v, want ErrClosed", fail, err)
		}
	}
}
