// SPDX-License-Identifier: MIT

package nmt

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

// markerID is the identifier of the frame a test sends after the calls
// it checks: on one port frames arrive in the order they were sent.
const markerID canbus.ID = 0x7FE

// sentBefore returns the frames that reached sub before a marker frame
// sent now from bus.
func sentBefore(t *testing.T, bus *canbus.Bus, sub *canbus.Subscription) []canbus.Frame {
	t.Helper()
	mark, _ := canbus.NewStandard(markerID, nil)
	send(t, bus, mark)
	var out []canbus.Frame
	for {
		f := recv(t, sub, nil)
		if f.ID == markerID {
			return out
		}
		out = append(out, f)
	}
}

// TestSlaveClosed: after Close the slave changes no state, does no reset
// and sends nothing.
func TestSlaveClosed(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	s, err := NewSlave(a, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if sent := sentBefore(t, a, sub); len(sent) != 1 || !isBootUp(5)(&sent[0]) {
		t.Fatalf("until Close: %v, want the boot-up message", sent)
	}
	if err := s.SetState(canopen.StateOperational); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("SetState after Close = %v, want ErrClosed", err)
	}
	if st := s.State(); st != canopen.StatePreOperational {
		t.Errorf("state %s after SetState on a closed slave", st)
	}
	if err := s.Reset(context.Background(), ResetCommunication); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Reset after Close = %v, want ErrClosed", err)
	}
	if st := s.Stats(); st.Resets != 0 || st.BootUps != 1 {
		t.Errorf("after a Reset on a closed slave: %+v", st)
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close = %v, want ErrClosed", err)
	}
	if sent := sentBefore(t, a, sub); len(sent) != 0 {
		t.Errorf("frames sent after Close: %v", sent)
	}
}

// TestCloseEndsReset: Close waits for a reset in progress on another
// goroutine, and that reset sends no boot-up message: it fails with
// ErrClosed.
func TestCloseEndsReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := testbus.Pair(t)
		sub := subscribe(t, b)
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		s, err := NewSlave(a, 5, WithResetHandler(func(ResetKind) {
			close(entered)
			<-held
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		reset := make(chan error, 1)
		go func() { reset <- s.Reset(context.Background(), ResetCommunication) }()
		<-entered
		closed := make(chan struct{})
		go func() {
			_ = s.Close()
			close(closed)
		}()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned while a reset was in progress")
		default:
		}
		release()
		if err := <-reset; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Reset in progress at Close = %v, want ErrClosed", err)
		}
		<-closed
		if sent := sentBefore(t, a, sub); len(sent) != 1 {
			t.Errorf("frames sent: %v, want the boot-up message of Start only", sent)
		}
		if st := s.State(); st != canopen.StateInitialising {
			t.Errorf("state %s after a reset that was closed, want Initialising", st)
		}
	})
}

// TestSlaveHandlerClosesSlave: the state, reset and boot-up handlers may
// call Close, on whichever goroutine they run: the slave's (an NMT
// command), the caller's of Reset, the caller's of Start.
func TestSlaveHandlerClosesSlave(t *testing.T) {
	t.Run("NMT command", func(t *testing.T) {
		a, b := testbus.Pair(t)
		closed := make(chan struct{})
		var s *Slave
		s, _ = NewSlave(a, 5, WithStateHandler(func(sc StateChange) {
			if sc.To == canopen.StateStopped {
				if s.State() != canopen.StateStopped || s.NodeID() != 5 {
					t.Error("the handler sees another state or node-ID")
				}
				_ = s.Close()
				close(closed)
			}
		}))
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		send(t, b, CommandFrame(5, canopen.NMTStop))
		select {
		case <-closed:
		case <-ctxT(t).Done():
			t.Fatal("Close called from the state handler did not return")
		}
		_ = s.Close() // waits for the slave's goroutine
		if err := s.SetState(canopen.StateOperational); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("SetState = %v, want ErrClosed", err)
		}
	})
	t.Run("Reset", func(t *testing.T) {
		a, b := testbus.Pair(t)
		sub := subscribe(t, b)
		var s *Slave
		s, _ = NewSlave(a, 5, WithResetHandler(func(ResetKind) { _ = s.Close() }))
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- s.Reset(context.Background(), ResetApplication) }()
		select {
		case err := <-done:
			if !errors.Is(err, canopen.ErrClosed) {
				t.Errorf("Reset whose handler closed the slave = %v, want ErrClosed", err)
			}
		case <-ctxT(t).Done():
			t.Fatal("Close called from the reset handler did not return")
		}
		if sent := sentBefore(t, a, sub); len(sent) != 1 {
			t.Errorf("frames sent: %v, want the boot-up message of Start only", sent)
		}
	})
	t.Run("Start", func(t *testing.T) {
		for _, name := range []string{"boot-up handler", "state handler"} {
			a, b := testbus.Pair(t)
			sub := subscribe(t, b)
			var s *Slave
			closer := func() { _ = s.Close() }
			opt := WithBootUpHandler(closer)
			if name == "state handler" {
				opt = WithStateHandler(func(StateChange) { closer() })
			}
			s, _ = NewSlave(a, 5, opt)
			done := make(chan error, 1)
			go func() { done <- s.Start() }()
			select {
			case err := <-done:
				if !errors.Is(err, canopen.ErrClosed) {
					t.Errorf("Start whose %s closed the slave = %v, want ErrClosed", name, err)
				}
			case <-ctxT(t).Done():
				t.Fatalf("Close called from the %s of Start did not return", name)
			}
			want := 0 // closed before the boot-up message
			if name == "state handler" {
				want = 1 // closed after it
			}
			if sent := sentBefore(t, a, sub); len(sent) != want {
				t.Errorf("%s: frames sent %v, want %d", name, sent, want)
			}
			if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
				t.Errorf("%s: second Start = %v, want ErrClosed", name, err)
			}
		}
	})
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

func (w *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// failures returns how many entries report a callback that did not
// return normally.
func (w *logBuffer) failures() int {
	return strings.Count(w.String(), `msg="callback did not return normally"`)
}

// TestSlaveHandlerDoesNotReturnNormally: handlers that do not return
// normally are logged with their stack; the state change and the reset
// they belong to complete.
func TestSlaveHandlerDoesNotReturnNormally(t *testing.T) {
	var log logBuffer
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	d := newDict(t, 5)
	var watching atomic.Bool // the watcher fails for the changes of the reset only
	d.Watch(func(*od.Variable, []byte) {
		if watching.Load() {
			panic("watcher of the application")
		}
	})
	s, err := NewSlave(a, 5, WithLogger(log.logger()), WithDictionary(d),
		WithStateHandler(func(StateChange) { panic("state handler of the application") }),
		WithResetHandler(func(ResetKind) { panic("reset handler of the application") }),
		WithBootUpHandler(func() { panic("boot-up handler of the application") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Close() }()
	recv(t, sub, isBootUp(5))
	if st := s.State(); st != canopen.StatePreOperational {
		t.Fatalf("state %s after Start", st)
	}
	// An NMT command, on the slave's goroutine.
	send(t, b, CommandFrame(5, canopen.NMTStart))
	eventually(t, 2*time.Second, func() bool { return s.State() == canopen.StateOperational })
	// A reset: the application object changes back, which notifies the
	// watcher.
	_ = d.Set(0x2000, 0, []byte{1, 0})
	watching.Store(true)
	if err := s.Reset(context.Background(), ResetApplication); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	recv(t, sub, isBootUp(5))
	if st := s.State(); st != canopen.StatePreOperational {
		t.Errorf("state %s after the reset", st)
	}
	// The restore is complete although the watcher ended every pass.
	if v, _ := d.GetUint(0x2000, 0); v != 7 {
		t.Errorf("application object %d after the reset, want its default 7", v)
	}
	// The slave still executes commands.
	send(t, b, CommandFrame(5, canopen.NMTStop))
	eventually(t, 2*time.Second, func() bool { return s.State() == canopen.StateStopped })
	entries := log.String()
	for _, want := range []string{"state handler", "reset handler", "boot-up handler", "watcher"} {
		if !strings.Contains(entries, want+" of the application") {
			t.Errorf("no log entry for the %s", want)
		}
	}
	if !strings.Contains(entries, "level=ERROR") || !strings.Contains(entries, "stack=") {
		t.Errorf("entries lack the level or the stack:\n%s", entries)
	}
}

// TestEventHandlers: the heartbeat and guard handlers may close their
// service from the service's goroutine, and one that does not return
// normally is logged while the next event is still delivered.
func TestEventHandlers(t *testing.T) {
	heartbeat := func(node canopen.NodeID) canbus.Frame {
		return ErrorControlFrame(node, canopen.StateOperational, false)
	}
	t.Run("heartbeat consumer", func(t *testing.T) {
		var log logBuffer
		a, b := testbus.Pair(t)
		var events atomic.Int32
		closed := make(chan struct{})
		var c *HeartbeatConsumer
		c, _ = NewHeartbeatConsumer(a, WithLogger(log.logger()), WithHeartbeatHandler(func(ev HeartbeatEvent) {
			switch events.Add(1) {
			case 1:
				panic("heartbeat handler of the application")
			case 2:
				if c.State(ev.Node) != ev.State {
					t.Error("the handler sees another state")
				}
				_ = c.Close()
				close(closed)
			}
		}))
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		send(t, b, heartbeat(7)) // the first event: the handler does not return normally
		send(t, b, heartbeat(8)) // the second: it closes the consumer
		select {
		case <-closed:
		case <-ctxT(t).Done():
			t.Fatal("the second event was not delivered, or Close called from the handler did not return")
		}
		_ = c.Close()
		if log.failures() != 1 {
			t.Errorf("log: %s", log.String())
		}
	})
	t.Run("guard master", func(t *testing.T) {
		var log logBuffer
		a, b := testbus.Pair(t)
		var events atomic.Int32
		closed := make(chan struct{})
		var g *GuardMaster
		g = NewGuardMaster(a, WithLogger(log.logger()), WithGuardHandler(func(GuardEvent) {
			switch events.Add(1) {
			case 1:
				panic("guard handler of the application")
			case 2:
				_ = g.State(7)
				_ = g.Close()
				close(closed)
			}
		}))
		if err := g.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Close() })
		_ = g.Guard(7, time.Hour, 1)
		_ = g.Guard(8, time.Hour, 1)
		// The boot-up messages of the two nodes: an event each, whether
		// or not a guard request is outstanding.
		send(t, b, ErrorControlFrame(7, canopen.StateInitialising, false))
		send(t, b, ErrorControlFrame(8, canopen.StateInitialising, false))
		select {
		case <-closed:
		case <-ctxT(t).Done():
			t.Fatal("the second event was not delivered, or Close called from the handler did not return")
		}
		_ = g.Close()
		if log.failures() != 1 {
			t.Errorf("log: %s", log.String())
		}
	})
	t.Run("guard responder", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var log logBuffer
			a, b := testbus.Pair(t)
			var events atomic.Int32
			closed := make(chan struct{})
			var r *GuardResponder
			state := func() canopen.NMTState { return canopen.StateOperational }
			r, _ = NewGuardResponder(a, 5, state, WithLogger(log.logger()), WithLifeGuarding(10*time.Millisecond, 2),
				WithGuardHandler(func(GuardEvent) {
					switch events.Add(1) {
					case 1: // the life guarding event
						panic("guard handler of the application")
					case 2: // guarding resumed
						_ = r.Stats()
						_ = r.Close()
						close(closed)
					}
				}))
			if err := r.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			send(t, b, GuardRequest(5))
			time.Sleep(time.Second) // no request within the life time
			send(t, b, GuardRequest(5))
			<-closed
			_ = r.Close()
			if log.failures() != 1 {
				t.Errorf("log: %s", log.String())
			}
		})
	})
}

// TestStateSourceDoesNotReturnNormally: a state source that does not
// return normally counts as Unknown: no heartbeat, no guarding response.
func TestStateSourceDoesNotReturnNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var log logBuffer
		a, b := testbus.Pair(t)
		sub := subscribe(t, b)
		state := func() canopen.NMTState { panic("state source of the application") }
		p, err := NewHeartbeatProducer(a, 5, state, WithLogger(log.logger()), WithHeartbeatPeriod(10*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewGuardResponder(a, 5, state, WithLogger(log.logger()), WithLifeGuarding(10*time.Millisecond, 2))
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(p.Start(), r.Start()); err != nil {
			t.Fatal(err)
		}
		send(t, b, GuardRequest(5))
		time.Sleep(55 * time.Millisecond)
		_ = p.Close()
		_ = r.Close()
		if st := p.Stats(); st.Skipped != 5 || st.Sent != 0 {
			t.Errorf("heartbeat producer: %+v, want 5 periods skipped", st)
		}
		if st := r.Stats(); st.Requests != 0 || st.Timeouts != 1 {
			t.Errorf("guard responder: %+v, want no answer and the life guarding event", st)
		}
		if sent := sentBefore(t, a, sub); len(sent) != 0 {
			t.Errorf("frames sent: %v", sent)
		}
		if n := log.failures(); n != 7 || !strings.Contains(log.String(), `callback="state source"`) {
			t.Errorf("%d failures logged, want 7 (5 heartbeat periods, the request, the life time):\n%s", n, log.String())
		}
	})
}

// heldBus sends the first frame, then holds every send until its context
// ends; it reports the first send it holds.
type heldBus struct {
	*canbus.Bus
	sent    atomic.Int32
	entered chan struct{}
}

func (h *heldBus) Send(ctx context.Context, f *canbus.Frame) error {
	switch h.sent.Add(1) {
	case 1:
		return h.Bus.Send(ctx, f)
	case 2:
		close(h.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestCloseEndsBootUpSend: Close ends a boot-up message that a reset is
// sending, whatever the context of that reset; the reset fails with
// ErrClosed and the slave stays Initialising.
func TestCloseEndsBootUpSend(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	bus := &heldBus{Bus: a, entered: make(chan struct{})}
	s, err := NewSlave(bus, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	reset := make(chan error, 1)
	go func() { reset <- s.Reset(context.Background(), ResetCommunication) }()
	<-bus.entered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reset:
		if !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Reset whose boot-up message was being sent at Close = %v, want ErrClosed", err)
		}
	default:
		t.Error("Close returned before the reset in progress")
	}
	if st := s.State(); st != canopen.StateInitialising {
		t.Errorf("state %s, want Initialising", st)
	}
	if sent := sentBefore(t, a, sub); len(sent) != 1 {
		t.Errorf("frames sent: %v, want the boot-up message of Start only", sent)
	}
}
