// SPDX-License-Identifier: MIT

// Package lifecycle is the Start/Close state of a long-running service
// with one goroutine.
//
// Close stops the goroutine and waits for it, on every call and from
// every goroutine: a second Close, concurrent or later, returns only when
// the goroutine has ended. The one exception is a Close on a goroutine
// the service itself runs application code on (a handler that closes its
// own service): it would wait for itself, so it stops the service and
// returns, and the goroutine ends when the handler returns. A Close from
// a handler that Start calls takes effect before Start returns.
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/goid"
)

// The states of a Service.
const (
	stateNew = iota
	stateRunning
	stateClosed
)

// Service is the Start/Close state of a service. The zero value is a
// service that was not started.
type Service struct {
	mu     sync.Mutex // serialises Start and Close
	state  atomic.Int32
	cancel context.CancelFunc
	done   chan struct{} // closed when the goroutine has ended; nil before Start

	loop     atomic.Uint64 // identifier of the service goroutine
	starting atomic.Uint64 // identifier of the goroutine inside Start
	closing  atomic.Bool   // Close was called from inside Start

	once   sync.Once
	closed chan struct{} // closed by the first Close
	stop   sync.Once
}

// Done returns a channel that is closed when Close is called, whether or
// not the service was started: what waits on behalf of the service (a
// transfer waiting for a response) ends with it.
func (s *Service) Done() <-chan struct{} {
	s.once.Do(func() { s.closed = make(chan struct{}) })
	return s.closed
}

// markClosed closes the channel of Done.
func (s *Service) markClosed() {
	s.Done()
	s.stop.Do(func() { close(s.closed) })
}

// Start runs setup with the service context and, when it succeeds, the
// loop it returns in a new goroutine. It fails with [canopen.ErrBusy]
// when the service is running and with [canopen.ErrClosed] after Close,
// also when a handler that setup called closed the service.
func (s *Service) Start(setup func(ctx context.Context) (func(ctx context.Context), error)) error {
	s.mu.Lock()
	switch s.state.Load() {
	case stateRunning:
		s.mu.Unlock()
		return fmt.Errorf("%w: already started", canopen.ErrBusy)
	case stateClosed:
		s.mu.Unlock()
		return canopen.ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.starting.Store(goid.ID())
	loop, err := setup(ctx)
	s.starting.Store(0)
	if err != nil {
		cancel()
		if s.closing.Load() {
			s.state.Store(stateClosed)
		}
		s.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	s.state.Store(stateRunning)
	go func() {
		defer close(done)
		s.loop.Store(goid.ID())
		loop(ctx)
	}()
	s.mu.Unlock()
	if s.closing.Load() {
		s.Close()
		return canopen.ErrClosed
	}
	return nil
}

// Close stops the goroutine and waits for it, as the package
// documentation describes. It is safe to call more than once and from
// several goroutines.
func (s *Service) Close() {
	id := goid.ID()
	if id != 0 && s.starting.Load() == id {
		// From a handler that Start's setup called: Start holds the
		// state, and closes the service when setup is done.
		s.closing.Store(true)
		s.markClosed()
		return
	}
	s.markClosed()
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.state.Store(stateClosed)
	s.mu.Unlock()
	if done == nil {
		return
	}
	cancel()
	if id != 0 && s.loop.Load() == id {
		return
	}
	<-done
}

// Closed reports whether Close was called.
func (s *Service) Closed() bool {
	return s.closing.Load() || s.state.Load() == stateClosed
}

// Running reports whether the service was started and not closed.
func (s *Service) Running() bool {
	return !s.closing.Load() && s.state.Load() == stateRunning
}
