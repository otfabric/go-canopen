// SPDX-License-Identifier: MIT

package lss

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

var slaveAddr = Address{Vendor: 0x29C, Product: 1, Revision: 1, Serial: 0xA}

// TestSlaveEveryCloseWaits: while the slave's goroutine is inside a
// handler, no Close returns; all return when the handler has returned.
func TestSlaveEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := testbus.Ports(t, 2)
		m := startMaster(t, ports[0])
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		s := startSlave(t, ports[1], slaveAddr, Unconfigured, WithStateHandler(func(State) {
			close(entered)
			<-held
		}))
		if err := m.SwitchStateGlobal(context.Background(), Configuration); err != nil {
			t.Fatal(err)
		}
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
			t.Fatalf("%d of %d Close calls returned while the handler was running", n, callers)
		}
		release()
		for range callers {
			<-returned
		}
	})
}

// TestSlaveHandlerClosesSlave: a handler may call the slave's methods and
// Close, which returns to it; a Close on another goroutine waits for the
// handler.
func TestSlaveHandlerClosesSlave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := testbus.Ports(t, 2)
		m := startMaster(t, ports[0])
		closed, held := make(chan struct{}), make(chan struct{})
		var s *Slave
		s = startSlave(t, ports[1], slaveAddr, Unconfigured, WithStateHandler(func(st State) {
			if s.State() != st || s.NodeID() != Unconfigured {
				t.Error("the handler sees another state or node-ID")
			}
			_ = s.Close()
			close(closed)
			<-held
		}))
		if err := m.SwitchStateGlobal(context.Background(), Configuration); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("Close called from the handler did not return")
		}
		other := make(chan struct{})
		go func() {
			_ = s.Close()
			close(other)
		}()
		synctest.Wait()
		select {
		case <-other:
			t.Fatal("Close on another goroutine returned while the handler was running")
		default:
		}
		close(held)
		<-other
	})
}

// TestSlaveHandlerDoesNotReturnNormally: a handler that does not return
// normally is logged with its stack and the slave goes on; a request the
// handler was to accept is refused.
func TestSlaveHandlerDoesNotReturnNormally(t *testing.T) {
	// Fake time: the windows of the selection checks do not depend on the
	// load of the machine.
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return buf.Write(p)
		}), nil))
		ports := testbus.Ports(t, 2)
		m := startMaster(t, ports[0])
		var stored bool
		s := startSlave(t, ports[1], slaveAddr, Unconfigured, WithSlaveLogger(log),
			WithStateHandler(func(State) { panic("state handler of the application") }),
			WithNodeIDHandler(func(canopen.NodeID) error { panic("node-ID handler of the application") }),
			WithBitTimingHandler(func(uint8, BitTiming) error { panic("bit timing handler of the application") }),
			WithActivateHandler(func(Settings, time.Duration) { panic("activate handler of the application") }),
			WithStoreHandler(func(Settings) error {
				if !stored {
					stored = true
					panic("store handler of the application")
				}
				return nil
			}))
		ctx := context.Background()
		err := m.Configure(ctx, slaveAddr, func(ctx context.Context) error {
			var ce *ConfigError
			if err := m.SetNodeID(ctx, 5); !errors.As(err, &ce) || ce.Code != CodeOutOfRange {
				t.Errorf("SetNodeID = %v, want a refusal with CodeOutOfRange", err)
			}
			if err := m.SetBitTiming(ctx, BitTiming(0)); !errors.As(err, &ce) || ce.Code != CodeOutOfRange {
				t.Errorf("SetBitTiming = %v, want a refusal with CodeOutOfRange", err)
			}
			if err := m.Store(ctx); !errors.As(err, &ce) || ce.Code != CodeStoreFailed {
				t.Errorf("Store = %v, want a refusal with CodeStoreFailed", err)
			}
			if err := m.ActivateBitTiming(ctx, 0); err != nil {
				t.Errorf("ActivateBitTiming: %v", err)
			}
			// The slave goes on: the store handler returns now.
			if err := m.Store(ctx); err != nil {
				t.Errorf("Store after the failures: %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Pending().NodeID; got != Unconfigured {
			t.Errorf("pending node-ID %d after a refused request", got)
		}
		mu.Lock()
		entries := buf.String()
		mu.Unlock()
		for _, want := range []string{"state handler", "node-ID handler", "bit timing handler", "activate handler", "store handler"} {
			if !strings.Contains(entries, want+" of the application") {
				t.Errorf("no log entry for the %s", want)
			}
		}
		if !strings.Contains(entries, `msg="callback did not return normally"`) || !strings.Contains(entries, "stack=") {
			t.Errorf("entries lack the message or the stack:\n%s", entries)
		}
	})
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestAssignHandlerDoesNotReturnNormally: AssignAll goes on after an
// assign handler that does not return normally.
func TestAssignHandlerDoesNotReturnNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, slaves := network(t, 1, 2)
		as, err := m.AssignAll(context.Background(), 10, AssignHandler(func(Assignment) { panic("assign handler of the application") }))
		if err != nil || len(as) != len(slaves) {
			t.Fatalf("AssignAll = %v, %v, want %d assignments", as, err, len(slaves))
		}
	})
}

// TestStateHandlerOnFastscan: the state handler is called when Fastscan
// selects the slave, and when the master switches it back.
func TestStateHandlerOnFastscan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := testbus.Ports(t, 2)
		m := startMaster(t, ports[0])
		var mu sync.Mutex
		var states []State
		startSlave(t, ports[1], slaveAddr, Unconfigured, WithStateHandler(func(st State) {
			mu.Lock()
			states = append(states, st)
			mu.Unlock()
		}))
		ctx := context.Background()
		if a, ok, err := m.FastscanSelect(ctx, FastscanOptions{}); err != nil || !ok || a != slaveAddr {
			t.Fatalf("FastscanSelect = %v, %v, %v", a, ok, err)
		}
		if err := m.SwitchStateGlobal(ctx, Waiting); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(states) != 2 || states[0] != Configuration || states[1] != Waiting {
			t.Errorf("states reported %v, want configuration, then waiting", states)
		}
	})
}
