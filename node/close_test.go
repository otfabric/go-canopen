// SPDX-License-Identifier: MIT

package node_test

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
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/timestamp"
	"github.com/otfabric/go-canopen/usdo"
)

// markerID is the identifier of the frame a test sends after the calls
// it checks: on one port frames arrive in the order they were sent.
const markerID canbus.ID = 0x7FD

// sentBefore returns the frames that reached sub before a marker frame
// sent now from bus.
func sentBefore(t *testing.T, bus *canbus.Bus, sub *canbus.Subscription) []canbus.Frame {
	t.Helper()
	ctx := nettest.Ctx(t)
	mark, _ := canbus.NewStandard(markerID, nil)
	if err := bus.Send(ctx, &mark); err != nil {
		t.Fatal(err)
	}
	var out []canbus.Frame
	for {
		f, err := sub.Receive(ctx)
		if err != nil {
			t.Fatalf("no marker frame: %v", err)
		}
		if f.ID == markerID {
			return out
		}
		out = append(out, f)
	}
}

// TestNodeClosed: after Close the node's operations fail with ErrClosed
// and the node sends nothing.
func TestNodeClosed(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	sub, err := buses[1].Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	n := startNode(t, buses[0], nettest.Dict(t, 5, nil))
	waitState(t, n, canopen.StatePreOperational)
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	sentBefore(t, buses[0], sub) // what the node sent until it was closed
	ctx := nettest.Ctx(t)
	for _, op := range []struct {
		name string
		call func() error
	}{
		{"SetState", func() error { return n.SetState(canopen.StateOperational) }},
		{"Reset", func() error { return n.Reset(ctx, nmt.ResetCommunication) }},
		{"Start", n.Start},
		{"SendTime", func() error { return n.SendTime(ctx, time.Now()) }},
		{"EMCY.Raise", func() error { return n.EMCY().Raise(ctx, 0x5000, emcy.RegGeneric, [5]byte{}) }},
		{"EMCY.Send", func() error { return n.EMCY().Send(ctx, emcy.Message{Code: 0x5000}) }},
		{"TPDO.Transmit", func() error { return n.TPDO(1).Transmit(ctx) }},
	} {
		if err := op.call(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("%s after Close = %v, want ErrClosed", op.name, err)
		}
	}
	if st := n.State(); st != canopen.StatePreOperational {
		t.Errorf("state %s after operations on a closed node", st)
	}
	if st := n.EMCY().Stats(); st.Queued != 0 || st.Sent != 0 {
		t.Errorf("emergency producer after Close: %+v", st)
	}
	if sent := sentBefore(t, buses[0], sub); len(sent) != 0 {
		t.Errorf("frames sent after Close: %v", sent)
	}
}

// TestNodeEveryCloseWaits: while a handler of the node is running, no
// Close returns, a second one no more than the first; all return when the
// handler has returned. The handler is the state handler of a reset, so
// Close also ends that reset: no boot-up message follows.
func TestNodeEveryCloseWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		buses := testbus.Ports(t, 2)
		sub, err := buses[1].Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{
			canbus.ExactStandard(canopen.FuncHeartbeat + 5), canbus.ExactStandard(markerID),
		}}))
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		n := startNode(t, buses[0], nettest.Dict(t, 5, nil), node.WithStateHandler(func(sc nmt.StateChange) {
			if sc.To == canopen.StateInitialising {
				close(entered)
				<-held
			}
		}))
		reset := make(chan error, 1)
		go func() { reset <- n.Reset(context.Background(), nmt.ResetCommunication) }()
		<-entered

		const callers = 3
		returned := make(chan struct{}, callers)
		for range callers {
			go func() {
				_ = n.Close()
				returned <- struct{}{}
			}()
		}
		synctest.Wait()
		if got := len(returned); got != 0 {
			t.Fatalf("%d of %d Close calls returned while the handler was running", got, callers)
		}
		release()
		for range callers {
			<-returned
		}
		if err := <-reset; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Reset in progress at Close = %v, want ErrClosed", err)
		}
		if sent := sentBefore(t, buses[0], sub); len(sent) != 1 {
			t.Errorf("error control frames sent: %v, want the boot-up message of Start only", sent)
		}
	})
}

// TestNodeHandlerClosesNode: a handler of the node, and a dictionary
// hook the node's SDO server runs, may close the node. Close returns to
// the handler, and a later Close waits for the node's goroutines.
func TestNodeHandlerClosesNode(t *testing.T) {
	t.Run("state handler", func(t *testing.T) {
		nettest.CheckLeaks(t)
		buses := testbus.Ports(t, 2)
		closed := make(chan struct{})
		var n *node.Node
		n = startNode(t, buses[0], nettest.Dict(t, 5, nil), node.WithStateHandler(func(sc nmt.StateChange) {
			if sc.To == canopen.StateStopped {
				if n.State() != canopen.StateStopped || n.Stats().NMT.Commands != 1 {
					t.Error("the handler sees another state, or no command")
				}
				_ = n.Close()
				close(closed)
			}
		}))
		ctx := nettest.Ctx(t)
		if err := nmt.NewMaster(buses[1]).Send(ctx, 5, canopen.NMTStop); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("Close called from the state handler did not return")
		}
		_ = n.Close()
		if err := n.SetState(canopen.StateOperational); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("SetState = %v, want ErrClosed", err)
		}
	})
	t.Run("dictionary hook", func(t *testing.T) {
		nettest.CheckLeaks(t)
		buses := testbus.Ports(t, 2)
		d := nettest.Dict(t, 5, nil)
		closed := make(chan struct{})
		var n *node.Node
		d.OnWrite(nettest.IdxParam, 0, func(*od.Variable, []byte) error {
			_ = n.Close()
			close(closed)
			return nil
		})
		n = startNode(t, buses[0], d)
		c, err := sdo.NewClient(buses[1], 5, sdo.WithTimeout(100*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := nettest.Ctx(t)
		go func() { _ = c.WriteUint32(ctx, nettest.IdxParam, 0, 1) }()
		select {
		case <-closed:
		case <-ctx.Done():
			t.Fatal("Close called from the write hook did not return")
		}
		_ = n.Close()
	})
	t.Run("state handler of Start", func(t *testing.T) {
		nettest.CheckLeaks(t)
		buses := testbus.Ports(t, 1)
		var n *node.Node
		n, err := node.New(buses[0], nettest.Dict(t, 5, nil), node.WithStateHandler(func(nmt.StateChange) { _ = n.Close() }))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- n.Start() }()
		select {
		case err := <-done:
			if !errors.Is(err, canopen.ErrClosed) {
				t.Errorf("Start whose state handler closed the node = %v, want ErrClosed", err)
			}
		case <-nettest.Ctx(t).Done():
			t.Fatal("Close called from the state handler of Start did not return")
		}
		if err := n.Start(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("second Start = %v, want ErrClosed", err)
		}
	})
}

// syncBuffer is a log destination a test reads while the node writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestNodeHandlerDoesNotReturnNormally: handlers of the node that do not
// return normally are logged with their stack, and the node goes on: it
// follows NMT commands and serves SDO.
func TestNodeHandlerDoesNotReturnNormally(t *testing.T) {
	nettest.CheckLeaks(t)
	var log syncBuffer
	buses := testbus.Ports(t, 2)
	d := nettest.Dict(t, 5, nil)
	// Node 9 is supervised: its heartbeat makes heartbeat events.
	if err := d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(nmt.ConsumerEntry(9, time.Hour))); err != nil {
		t.Fatal(err)
	}
	// TIME is consumed (0x1012 bit 31).
	if err := d.SetUint(od.IndexCOBIDTime, 0, uint64(canopen.COBConsume)|0x100); err != nil {
		t.Fatal(err)
	}
	var states, beats, times atomic.Int32
	n := startNode(t, buses[0], d, node.WithLogger(slog.New(slog.NewTextHandler(&log, nil))),
		node.WithStateHandler(func(nmt.StateChange) {
			states.Add(1)
			panic("state handler of the application")
		}),
		node.WithHeartbeatHandler(func(nmt.HeartbeatEvent) {
			beats.Add(1)
			panic("heartbeat handler of the application")
		}),
		node.WithTimeHandler(func(timestamp.Event) {
			times.Add(1)
			panic("time handler of the application")
		}),
		node.WithDrive(cia402.DriveConfig{
			OnError:      func(uint16) { panic("drive error handler of the application") },
			OnTransition: func(_, _ cia402.State, _ int) { panic("drive transition handler of the application") },
		}, 0))
	waitState(t, n, canopen.StatePreOperational)
	// A fault of the drive: its error and transition handlers are called.
	n.Drive().InjectFault(0x2310)
	if st := n.Drive().State(); st != cia402.FaultReactionActive && st != cia402.Fault {
		t.Errorf("drive state %v after a fault", st)
	}
	ctx := nettest.Ctx(t)
	m := nmt.NewMaster(buses[1])
	if err := m.Send(ctx, 5, canopen.NMTStart); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateOperational)
	hb := nmt.ErrorControlFrame(9, canopen.StateOperational, false)
	td, _ := timestamp.FromTime(time.Now())
	tf := timestamp.Frame(canopen.COBID(0x100), td)
	for range 2 {
		if err := errors.Join(buses[1].Send(ctx, &hb), buses[1].Send(ctx, &tf)); err != nil {
			t.Fatal(err)
		}
		hb = nmt.ErrorControlFrame(9, canopen.StatePreOperational, false) // another state: another event
	}
	nettest.Eventually(t, 5*time.Second, "two heartbeat and two TIME events", func() bool {
		return beats.Load() >= 2 && times.Load() == 2
	})
	// The node goes on.
	c, err := sdo.NewClient(buses[1], 5, sdo.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err != nil {
		t.Errorf("SDO after the failures: %v", err)
	}
	if err := m.Send(ctx, 5, canopen.NMTStop); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateStopped)
	entries := log.String()
	for _, want := range []string{"state handler", "heartbeat handler", "time handler", "drive error handler", "drive transition handler"} {
		if !strings.Contains(entries, want+" of the application") {
			t.Errorf("no log entry for the %s", want)
		}
	}
	if !strings.Contains(entries, `msg="callback did not return normally"`) || !strings.Contains(entries, "stack=") {
		t.Errorf("entries lack the message or the stack")
	}
}

// holdBus holds one Subscribe call of a node until released: the node is
// then in the middle of Start.
type holdBus struct {
	*canbus.Bus
	calls   atomic.Int32
	at      int32 // the call to hold, counted from 1
	entered chan struct{}
	release chan struct{}
}

func (h *holdBus) Subscribe(opts ...canbus.SubscribeOption) (*canbus.Subscription, error) {
	if h.calls.Add(1) == h.at {
		close(h.entered)
		<-h.release
	}
	return h.Bus.Subscribe(opts...)
}

// TestFDServerSilentBeforeBootUp: the USDO server of a CANopen FD node
// answers nothing until the node's boot-up message, like the SDO server
// of a classical node: the node is Initialising until then.
func TestFDServerSilentBeforeBootUp(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2, virtual.WithFD())
	// Start is held at the subscription that follows the USDO server's:
	// the USDO server runs, the boot-up message is still to come.
	bus := &holdBus{Bus: buses[0], at: 2, entered: make(chan struct{}), release: make(chan struct{})}
	n, err := node.New(bus, fdDict(t, 5), node.WithFD(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	started := make(chan error, 1)
	go func() { started <- n.Start() }()
	<-bus.entered
	c, err := usdo.NewClient(buses[1], 1, usdo.WithTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := nettest.Ctx(t)
	if _, err := c.Upload(ctx, 5, od.IndexDeviceType, 0); !errors.Is(err, canopen.ErrTimeout) {
		t.Errorf("USDO upload before the boot-up message = %v, want no answer (ErrTimeout)", err)
	}
	if st := n.Stats().USDO; st.Uploads != 0 || st.Ignored == 0 {
		t.Errorf("USDO server before the boot-up message: %+v, want the request ignored", st)
	}
	close(bus.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StatePreOperational)
	if _, err := c.Upload(ctx, 5, od.IndexDeviceType, 0, usdo.WithCallTimeout(10*time.Second)); err != nil {
		t.Errorf("USDO upload after the boot-up message: %v", err)
	}
}
