// SPDX-License-Identifier: MIT

package master

import (
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/lss"
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
	// EventBootFailed: the boot procedure of a node failed (Err says why;
	// for the CiA 302 boot slave process Status is the error status and
	// Err a *BootError).
	EventBootFailed
	// EventNetworkBootDone: the CiA 302 network boot-up ended, every
	// mandatory slave booted (Node is the master's node-ID).
	EventNetworkBootDone
	// EventNetworkBootFailed: the CiA 302 network boot-up halted because
	// the boot slave process of a mandatory slave (Node) failed; Err is
	// its *BootError.
	EventNetworkBootFailed
	// EventGuardingLost: a node guarded by the CiA 302 manager did not
	// answer (node guarding event).
	EventGuardingLost
	// EventGuardingResumed: a lost guarded node answers again.
	EventGuardingResumed
	// EventNodeError: the CiA 302 manager reacted to the error control
	// event of a slave of the network list (Err says how: reset node,
	// reset all nodes, stop all nodes).
	EventNodeError
	// EventLSSAssigned: the LSS step of the CiA 302 network boot-up
	// ([WithLSSBoot]) gave an unconfigured device the node-ID Node (LSS
	// holds the device's address and the node-ID).
	EventLSSAssigned
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
	case EventNetworkBootDone:
		return "network-boot-done"
	case EventNetworkBootFailed:
		return "network-boot-failed"
	case EventGuardingLost:
		return "guarding-lost"
	case EventGuardingResumed:
		return "guarding-resumed"
	case EventNodeError:
		return "node-error"
	case EventLSSAssigned:
		return "lss-assigned"
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
	// Err is the failure of an EventBootFailed or EventNetworkBootFailed
	// and the reaction of an EventNodeError.
	Err error
	// Status is the result of a CiA 302 boot slave process
	// (EventBootDone, EventBootFailed, EventNetworkBootFailed).
	Status BootStatus
	// LSS is the assignment of an EventLSSAssigned.
	LSS  *lss.Assignment
	Time time.Time
}

// String formats the event for logs.
func (e Event) String() string {
	switch e.Kind {
	case EventEMCY:
		return fmt.Sprintf("node %d %s: %s", e.Node, e.Kind, e.EMCY.Message)
	case EventBootFailed, EventNetworkBootFailed, EventNodeError:
		return fmt.Sprintf("node %d %s: %v", e.Node, e.Kind, e.Err)
	case EventStateChange:
		return fmt.Sprintf("node %d %s: %s -> %s", e.Node, e.Kind, e.Previous, e.State)
	case EventLSSAssigned:
		return fmt.Sprintf("node %d %s: %s", e.Node, e.Kind, e.LSS.Address)
	default: // no special case
	}
	return fmt.Sprintf("node %d %s", e.Node, e.Kind)
}

// DefaultEventQueue is the number of bus-driven events ([lossy]) the
// master holds for a handler that does not keep up, without
// [WithEventQueue].
const DefaultEventQueue = 4096

// lossy reports whether events of kind k come straight from frames on the
// bus, at a rate a device (or a flood) sets: boot-up, state change and
// EMCY. They are the ones [WithEventQueue] bounds. The others report the
// master's own processes or supervision timeouts and are never dropped.
func lossy(k EventKind) bool {
	switch k {
	case EventBootUp, EventStateChange, EventEMCY:
		return true
	default: // results of the master's processes and timeouts
	}
	return false
}

// queued is an event with its place in the order of all events.
type queued struct {
	seq uint64
	ev  Event
}

// dispatcher delivers events to the handler in order on its own
// goroutine, so that services never wait for the handler and the handler
// may call the master (SDO transfers, NMT). Bus-driven events ([lossy])
// wait in a queue of at most max, which drops its oldest when full; the
// others wait in a queue of their own, never dropped. The two are merged
// back into the order the events occurred in.
type dispatcher struct {
	h       func(Event)
	max     int
	mu      sync.Mutex
	cond    *sync.Cond
	q       []queued // not lossy
	lq      []queued // lossy, at most max
	seq     uint64
	dropped uint64
	stop    bool
	done    chan struct{}
}

func newDispatcher(h func(Event), maxLossy int) *dispatcher {
	if maxLossy <= 0 {
		maxLossy = DefaultEventQueue
	}
	d := &dispatcher{h: h, max: maxLossy, done: make(chan struct{})}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *dispatcher) run() {
	defer close(d.done)
	for {
		d.mu.Lock()
		for len(d.q) == 0 && len(d.lq) == 0 && !d.stop {
			d.cond.Wait()
		}
		ev, ok := d.nextLocked()
		d.mu.Unlock()
		if !ok {
			return
		}
		if d.h != nil {
			d.h(ev)
		}
	}
}

// nextLocked removes the earliest queued event; ok is false when both
// queues are empty.
func (d *dispatcher) nextLocked() (ev Event, ok bool) {
	switch {
	case len(d.q) == 0 && len(d.lq) == 0:
		return Event{}, false
	case len(d.lq) == 0 || len(d.q) > 0 && d.q[0].seq < d.lq[0].seq:
		ev = d.q[0].ev
		d.q[0] = queued{}
		d.q = d.q[1:]
	default:
		ev = d.lq[0].ev
		d.lq[0] = queued{}
		d.lq = d.lq[1:]
	}
	return ev, true
}

func (d *dispatcher) push(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	d.mu.Lock()
	if !d.stop {
		d.seq++
		e := queued{seq: d.seq, ev: ev}
		if lossy(ev.Kind) {
			if len(d.lq) >= d.max {
				d.lq[0] = queued{}
				d.lq = d.lq[1:]
				d.dropped++
			}
			d.lq = append(d.lq, e)
		} else {
			d.q = append(d.q, e)
		}
	}
	d.mu.Unlock()
	d.cond.Signal()
}

// droppedEvents returns the number of events dropped from a full queue.
func (d *dispatcher) droppedEvents() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// close delivers the queued events and stops.
func (d *dispatcher) close() {
	d.mu.Lock()
	d.stop = true
	d.mu.Unlock()
	d.cond.Broadcast()
	<-d.done
}
