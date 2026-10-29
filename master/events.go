// SPDX-License-Identifier: MIT

package master

import (
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
)

// EventKind is the kind of an [Event].
type EventKind uint8

// Event kinds.
const (
	// EventBootUp: a node sent its boot-up message.
	EventBootUp EventKind = iota + 1
	// EventStateChange: a node's heartbeat reports another state.
	EventStateChange
	// EventHeartbeatLost: a supervised node's heartbeat timed out
	// (reported once per loss).
	EventHeartbeatLost
	// EventHeartbeatResumed: a lost node is heard again.
	EventHeartbeatResumed
	// EventEMCY: a node sent an emergency message (EMCY is set).
	EventEMCY
	// EventBootDone: the boot procedure of a node succeeded.
	EventBootDone
	// EventBootFailed: the boot procedure of a node failed (Err says why).
	EventBootFailed
)

// String names the event kind.
func (k EventKind) String() string {
	switch k {
	case EventBootUp:
		return "boot-up"
	case EventStateChange:
		return "state-change"
	case EventHeartbeatLost:
		return "heartbeat-lost"
	case EventHeartbeatResumed:
		return "heartbeat-resumed"
	case EventEMCY:
		return "emcy"
	case EventBootDone:
		return "boot-done"
	case EventBootFailed:
		return "boot-failed"
	}
	return fmt.Sprintf("EventKind(%d)", uint8(k))
}

// Event is something the master observed on the network.
type Event struct {
	Kind EventKind
	Node canopen.NodeID
	// State is the node's NMT state after the event and Previous the one
	// before it (heartbeat events); Unknown after a loss.
	State, Previous canopen.NMTState
	// EMCY is the emergency message of an EventEMCY.
	EMCY *emcy.Event
	// Err is the failure of an EventBootFailed.
	Err  error
	Time time.Time
}

// String formats the event for logs.
func (e Event) String() string {
	switch e.Kind {
	case EventEMCY:
		return fmt.Sprintf("node %d %s: %s", e.Node, e.Kind, e.EMCY.Message)
	case EventBootFailed:
		return fmt.Sprintf("node %d %s: %v", e.Node, e.Kind, e.Err)
	case EventStateChange:
		return fmt.Sprintf("node %d %s: %s -> %s", e.Node, e.Kind, e.Previous, e.State)
	}
	return fmt.Sprintf("node %d %s", e.Node, e.Kind)
}

// dispatcher delivers events to the handler in order on its own
// goroutine, through an unbounded queue, so that services never wait for
// the handler and the handler may call the master (SDO transfers, NMT).
type dispatcher struct {
	h    func(Event)
	mu   sync.Mutex
	cond *sync.Cond
	q    []Event
	stop bool
	done chan struct{}
}

func newDispatcher(h func(Event)) *dispatcher {
	d := &dispatcher{h: h, done: make(chan struct{})}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *dispatcher) run() {
	defer close(d.done)
	for {
		d.mu.Lock()
		for len(d.q) == 0 && !d.stop {
			d.cond.Wait()
		}
		if len(d.q) == 0 {
			d.mu.Unlock()
			return
		}
		ev := d.q[0]
		d.q[0] = Event{}
		d.q = d.q[1:]
		d.mu.Unlock()
		if d.h != nil {
			d.h(ev)
		}
	}
}

func (d *dispatcher) push(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	d.mu.Lock()
	if !d.stop {
		d.q = append(d.q, ev)
	}
	d.mu.Unlock()
	d.cond.Signal()
}

// close delivers the queued events and stops.
func (d *dispatcher) close() {
	d.mu.Lock()
	d.stop = true
	d.mu.Unlock()
	d.cond.Broadcast()
	<-d.done
}
