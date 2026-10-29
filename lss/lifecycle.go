// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
)

// sendTimeout bounds every frame a service sends on its own.
const sendTimeout = time.Second

// lifecycle is the Start/Close state of a long-running service.
type lifecycle struct {
	mu     sync.Mutex
	state  int // 0 new, 1 running, 2 closed
	cancel context.CancelFunc
	done   chan struct{}
}

// start runs setup with the service context and, when it succeeds, the
// loop it returns in a new goroutine.
func (l *lifecycle) start(setup func(ctx context.Context) (func(ctx context.Context), error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state {
	case 1:
		return fmt.Errorf("%w: already started", canopen.ErrBusy)
	case 2:
		return canopen.ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop, err := setup(ctx)
	if err != nil {
		cancel()
		return err
	}
	l.cancel, l.done, l.state = cancel, make(chan struct{}), 1
	go func() {
		defer close(l.done)
		loop(ctx)
	}()
	return nil
}

// close stops the loop and waits for it; later calls do nothing.
func (l *lifecycle) close() {
	l.mu.Lock()
	st, cancel, done := l.state, l.cancel, l.done
	l.state = 2
	l.mu.Unlock()
	if st == 1 {
		cancel()
		<-done
	}
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
