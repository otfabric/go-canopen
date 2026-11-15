// SPDX-License-Identifier: MIT

package node_test

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// stateGate is a log handler that holds the goroutine that logs the
// change to one NMT state: the slave writes that entry after it set the
// state and before it calls the state handler.
type stateGate struct {
	to      string
	once    *sync.Once
	held    chan struct{} // closed when the goroutine is held
	release chan struct{}
}

func newStateGate(to canopen.NMTState) *stateGate {
	return &stateGate{to: to.String(), once: new(sync.Once), held: make(chan struct{}), release: make(chan struct{})}
}

func (g *stateGate) Enabled(context.Context, slog.Level) bool { return true }
func (g *stateGate) WithAttrs([]slog.Attr) slog.Handler       { return g }
func (g *stateGate) WithGroup(string) slog.Handler            { return g }

func (g *stateGate) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "nmt state" {
		return nil
	}
	match := false
	r.Attrs(func(a slog.Attr) bool {
		match = a.Key == "to" && a.Value.String() == g.to
		return !match
	})
	if match {
		g.once.Do(func() {
			close(g.held)
			<-g.release
		})
	}
	return nil
}

// TestServicesFollowTheStateThatHolds: two goroutines change the NMT
// state, and the state handler of the first change runs after that of the
// second. The services are enabled for the state the node is in, not for
// the state of the handler that ran last.
func TestServicesFollowTheStateThatHolds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second canopen.NMTState
	}{
		{"start then stop", canopen.StateOperational, canopen.StateStopped},
		{"stop then start", canopen.StateStopped, canopen.StateOperational},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nettest.CheckLeaks(t)
			r := newRig(t, 1)
			ctx := nettest.Ctx(t)
			gate := newStateGate(tc.first)
			d := nettest.Dict(t, 5, nil)
			n := startNode(t, r.buses[1], d, node.WithLogger(slog.New(gate)))

			// The first change sets its state and is held before its
			// state handler.
			firstDone := make(chan error, 1)
			go func() { firstDone <- n.SetState(tc.first) }()
			<-gate.held
			// The second change runs to its end: the node is in its
			// state, with the services for it.
			if err := n.SetState(tc.second); err != nil {
				t.Fatal(err)
			}
			// Now the state handler of the first change runs.
			close(gate.release)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}

			if st := n.State(); st != tc.second {
				t.Fatalf("state %s, want %s", st, tc.second)
			}
			// An answer has all the time it needs; no answer is waited
			// for briefly.
			timeout := 10 * time.Second
			if tc.second == canopen.StateStopped {
				timeout = 150 * time.Millisecond
			}
			c, err := sdo.NewClient(r.buses[0], 5, sdo.WithTimeout(timeout))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			err = n.TPDO(1).Transmit(ctx)
			_, sdoErr := c.ReadUint(ctx, od.IndexDeviceType, 0)
			if tc.second == canopen.StateOperational {
				if errors.Is(err, pdo.ErrDisabled) {
					t.Errorf("TPDO disabled in %s", tc.second)
				}
				if sdoErr != nil {
					t.Errorf("SDO in %s: %v", tc.second, sdoErr)
				}
				return
			}
			if !errors.Is(err, pdo.ErrDisabled) {
				t.Errorf("TPDO Transmit in %s = %v, want ErrDisabled", tc.second, err)
			}
			if !errors.Is(sdoErr, canopen.ErrTimeout) {
				t.Errorf("SDO read in %s = %v, want no answer (ErrTimeout)", tc.second, sdoErr)
			}
		})
	}
}

// TestErrorBehaviourOnlyWhenRunning: the error behaviour of 0x1029
// changes the state of a node that is Pre-operational or Operational
// only. A node that lost its node-ID (Initialising, silent) stays as it
// is when a supervised node is lost: it does not become Stopped, and its
// heartbeat producer sends nothing.
func TestErrorBehaviourOnlyWhenRunning(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 10 })
	// Defaults, so that they hold after a reset communication: lose node
	// 9 after 100 ms without heartbeat, then enter Stopped.
	hb := binary.LittleEndian.AppendUint32(nil, 9<<16|100)
	d.Object(od.IndexConsumerHeartbeat).Sub(1).Default = hb
	d.Object(node.IndexErrorBehaviour).Sub(1).Default = []byte{node.ErrorBehaviourStopped}
	if err := errors.Join(d.Set(od.IndexConsumerHeartbeat, 1, hb), d.Set(node.IndexErrorBehaviour, 1, []byte{node.ErrorBehaviourStopped})); err != nil {
		t.Fatal(err)
	}
	sub, err := r.buses[2].Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{
		canbus.ExactStandard(canopen.FuncHeartbeat + 5), canbus.ExactStandard(markerID),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	lost := make(chan struct{}, 8)
	n := startNode(t, r.buses[1], d, node.WithLSS(), node.WithHeartbeatHandler(func(ev nmt.HeartbeatEvent) {
		if ev.Kind == nmt.EventTimeout && ev.Node == 9 {
			lost <- struct{}{}
		}
	}))
	waitState(t, n, canopen.StatePreOperational)

	// The node loses its node-ID (as LSS does): Initialising, no boot-up.
	if err := n.SetNodeID(nmt.Unconfigured); err != nil {
		t.Fatal(err)
	}
	if err := n.Reset(ctx, nmt.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	if n.State() != canopen.StateInitialising || n.NodeID() != nmt.Unconfigured {
		t.Fatalf("after the reset: state %s, node-ID %d", n.State(), n.NodeID())
	}
	// What node 5 sent while it had its node-ID: the boot-up message,
	// and heartbeats.
	sentBefore(t, r.buses[1], sub)
	before := n.Stats().Heartbeat

	// Node 9 sends one heartbeat and is then lost.
	f := nmt.ErrorControlFrame(9, canopen.StateOperational, false)
	if err := r.buses[2].Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost: // the error behaviour was applied before the handler ran
	case <-ctx.Done():
		t.Fatal("node 9 was not reported lost")
	}
	if st := n.State(); st != canopen.StateInitialising {
		t.Fatalf("state %s after the loss of a supervised node, want Initialising", st)
	}
	// Three more heartbeat periods pass without a heartbeat.
	nettest.Eventually(t, 5*time.Second, "three heartbeat periods", func() bool {
		return n.Stats().Heartbeat.Skipped >= before.Skipped+3
	})
	if after := n.Stats().Heartbeat; after.Sent != before.Sent {
		t.Errorf("%d heartbeats sent while Initialising", after.Sent-before.Sent)
	}
	if sent := sentBefore(t, r.buses[1], sub); len(sent) != 0 {
		t.Errorf("frames on the node's former heartbeat COB-ID: %v", sent)
	}
}
