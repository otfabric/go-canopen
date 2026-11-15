// SPDX-License-Identifier: MIT

package master_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
)

// bootUp sends the boot-up message of a node, which the master reports
// as an event.
func bootUp(t *testing.T, r *rig, node canopen.NodeID) {
	t.Helper()
	f := nmt.ErrorControlFrame(node, canopen.StateInitialising, false)
	if err := r.buses[1].Send(nettest.Ctx(t), &f); err != nil {
		t.Fatal(err)
	}
}

// TestMasterEveryCloseWaits: while the event handler is running, no
// Close returns, a second one no more than the first; all return when
// the handler has returned.
func TestMasterEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		var once sync.Once
		r := newRig(t, 1, master.WithEventHandler(func(master.Event) {
			once.Do(func() {
				close(entered)
				<-held
			})
		}))
		bootUp(t, r, 5)
		<-entered
		const callers = 3
		returned := make(chan struct{}, callers)
		for range callers {
			go func() {
				_ = r.m.Close()
				returned <- struct{}{}
			}()
		}
		synctest.Wait()
		if n := len(returned); n != 0 {
			t.Fatalf("%d of %d Close calls returned while the event handler was running", n, callers)
		}
		release()
		for range callers {
			<-returned
		}
	})
}

// TestEventHandlerClosesMaster: the event handler may close the master.
// Close returns to it, the events still queued are delivered when it has
// returned, and a Close on another goroutine waits for that.
func TestEventHandlerClosesMaster(t *testing.T) {
	nettest.CheckLeaks(t)
	var m *master.Master
	var events atomic.Int32
	closed := make(chan struct{})
	r := newRig(t, 1, master.WithEventHandler(func(e master.Event) {
		if events.Add(1) == 1 {
			if m.State(e.Node) != canopen.StateInitialising {
				t.Error("the handler sees another state of the node")
			}
			_ = m.Close()
			close(closed)
		}
	}))
	m = r.m
	bootUp(t, r, 5)
	select {
	case <-closed:
	case <-nettest.Ctx(t).Done():
		t.Fatal("Close called from the event handler did not return")
	}
	_ = m.Close() // waits for the delivery to end
	if err := m.NMT(nettest.Ctx(t), 5, canopen.NMTStart); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("NMT = %v, want ErrClosed", err)
	}
}

// TestEventHandlerDoesNotReturnNormally: an event handler that does not
// return normally is logged with its stack, and the next event is
// delivered.
func TestEventHandlerDoesNotReturnNormally(t *testing.T) {
	nettest.CheckLeaks(t)
	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	var events atomic.Int32
	second := make(chan master.Event, 1)
	buses := testbus.Ports(t, 2)
	r := newRigOn(t, buses, master.WithLogger(log), master.WithEventHandler(func(e master.Event) {
		if events.Add(1) == 1 {
			panic("event handler of the application")
		}
		select {
		case second <- e:
		default:
		}
	}))
	bootUp(t, r, 5)
	bootUp(t, r, 6)
	select {
	case e := <-second:
		if e.Kind != master.EventBootUp || e.Node != 6 {
			t.Errorf("second event %v", e)
		}
	case <-time.After(waitTimeout):
		t.Fatal("no event after the handler did not return normally")
	}
	mu.Lock()
	entries := buf.String()
	mu.Unlock()
	if n := strings.Count(entries, `msg="callback did not return normally"`); n != 1 ||
		!strings.Contains(entries, "event handler of the application") || !strings.Contains(entries, `callback="event handler"`) || !strings.Contains(entries, "stack=") {
		t.Errorf("%d failures logged, want 1 with the handler's value and stack", n)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
