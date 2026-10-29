// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// HeartbeatEventKind is the kind of a [HeartbeatEvent].
type HeartbeatEventKind uint8

// Heartbeat consumer events.
const (
	// EventBootUp: the node sent a boot-up message. Supervision of the
	// node restarts with its next heartbeat.
	EventBootUp HeartbeatEventKind = iota + 1
	// EventStateChange: the node reported a state other than the last one
	// (or its first state).
	EventStateChange
	// EventTimeout: no heartbeat of a supervised node within its consumer
	// heartbeat time. Reported once per loss; the state becomes Unknown.
	EventTimeout
	// EventResumed: a heartbeat or boot-up message arrived after a
	// timeout. It precedes the boot-up or state change event of the same
	// message.
	EventResumed
)

// String names the event kind.
func (k HeartbeatEventKind) String() string {
	switch k {
	case EventBootUp:
		return "boot-up"
	case EventStateChange:
		return "state-change"
	case EventTimeout:
		return "timeout"
	case EventResumed:
		return "resumed"
	}
	return fmt.Sprintf("HeartbeatEventKind(%d)", uint8(k))
}

// HeartbeatEvent is reported by a [HeartbeatConsumer].
type HeartbeatEvent struct {
	Kind     HeartbeatEventKind
	Node     canopen.NodeID
	State    canopen.NMTState // state after the event (Unknown after a timeout)
	Previous canopen.NMTState // state before the event
	Time     time.Time
}

// NodeStatus is what a [HeartbeatConsumer] knows of a node.
type NodeStatus struct {
	Node canopen.NodeID
	// State is the last reported state: Initialising after a boot-up,
	// Unknown before the first message and after a timeout.
	State canopen.NMTState
	// Timeout is the consumer heartbeat time; 0 when the node is not
	// supervised.
	Timeout time.Duration
	// Active reports that supervision runs: a heartbeat arrived since the
	// node was configured, booted or timed out.
	Active bool
	// Lost reports a timeout not yet followed by a message.
	Lost     bool
	LastSeen time.Time // zero when never heard
	BootUps  uint64
	LastBoot time.Time // zero when no boot-up was seen
}

// HeartbeatConsumerStats counts what a [HeartbeatConsumer] did.
type HeartbeatConsumerStats struct {
	Received uint64 `json:"received"` // heartbeat and boot-up messages
	Invalid  uint64 `json:"invalid"`  // malformed error control frames
	BootUps  uint64 `json:"bootUps"`
	Timeouts uint64 `json:"timeouts"`
}

// HeartbeatConsumer receives the heartbeats and boot-up messages of all
// nodes (CiA 301 7.2.8.3.2.2), tracks every node's state and supervises
// the configured ones: a node whose heartbeat does not arrive within its
// consumer heartbeat time is reported lost once ([EventTimeout]) until
// it is heard again ([EventResumed]). As CiA 301 requires, supervision of
// a node starts with its first heartbeat, not with the configuration, and
// a boot-up message suspends it until the next heartbeat.
//
// Node guarding responses (bit 7 toggling) are read as heartbeats with
// the toggle bit ignored.
type HeartbeatConsumer struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	mu       sync.Mutex
	nodes    map[canopen.NodeID]*hbNode
	explicit map[canopen.NodeID]time.Duration
	fromDict map[canopen.NodeID]time.Duration
	changed  chan struct{} // closed and replaced on every update
	done     chan struct{} // closed by Close
	stats    HeartbeatConsumerStats
}

type hbNode struct {
	state    canopen.NMTState
	timeout  time.Duration
	active   bool
	lost     bool
	last     time.Time
	deadline time.Time
	bootups  uint64
	lastBoot time.Time
}

// NewHeartbeatConsumer returns a heartbeat consumer. Options:
// [WithLogger], [WithHeartbeatHandler], [WithDictionary] (0x1016, which
// must exist; its write hook is registered at once and refuses duplicate
// node-IDs).
func NewHeartbeatConsumer(bus canopen.Bus, opts ...Option) (*HeartbeatConsumer, error) {
	c := &HeartbeatConsumer{
		bus:      bus,
		cfg:      newConfig(opts),
		wake:     make(chan struct{}, 1),
		nodes:    map[canopen.NodeID]*hbNode{},
		explicit: map[canopen.NodeID]time.Duration{},
		fromDict: map[canopen.NodeID]time.Duration{},
		changed:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	if d := c.cfg.dict; d != nil {
		if d.Object(od.IndexConsumerHeartbeat) == nil {
			return nil, fmt.Errorf("nmt: consumer heartbeat time 0x1016: %w", canopen.AbortNoObject)
		}
		d.OnWrite(od.IndexConsumerHeartbeat, -1, func(v *od.Variable, data []byte) error {
			return checkConsumerEntry(d, v.SubIndex, uint32(canopen.DecodeUint(data)))
		})
		c.loadDict()
	}
	return c, nil
}

// ConsumerEntry encodes a consumer heartbeat time entry of 0x1016.
func ConsumerEntry(node canopen.NodeID, timeout time.Duration) uint32 {
	ms := timeout.Milliseconds()
	ms = min(max(ms, 0), 0xFFFF)
	return uint32(node)<<16 | uint32(ms)
}

// ParseConsumerEntry decodes an entry of 0x1016. ok is false for an unused
// entry (node-ID 0 or above 127, or time 0).
func ParseConsumerEntry(e uint32) (node canopen.NodeID, timeout time.Duration, ok bool) {
	n, ms := uint8(e>>16), e&0xFFFF
	node, timeout = canopen.NodeID(n), time.Duration(ms)*time.Millisecond
	return node, timeout, node.Valid() && ms != 0
}

// checkConsumerEntry refuses an entry that supervises a node another
// entry supervises already (CiA 301 7.5.2.31).
func checkConsumerEntry(d *od.Dictionary, sub uint8, e uint32) error {
	if sub == 0 {
		return nil
	}
	node, _, ok := ParseConsumerEntry(e)
	if !ok {
		return nil
	}
	o := d.Object(od.IndexConsumerHeartbeat)
	for _, v := range o.Subs {
		if v.SubIndex == 0 || v.SubIndex == sub {
			continue
		}
		x, err := d.GetUint(od.IndexConsumerHeartbeat, v.SubIndex)
		if err != nil {
			continue
		}
		if n, _, ok := ParseConsumerEntry(uint32(x)); ok && n == node {
			return canopen.AbortParamIncompatible
		}
	}
	return nil
}

// loadDict reads 0x1016 into the dictionary configuration.
func (c *HeartbeatConsumer) loadDict() {
	d := c.cfg.dict
	o := d.Object(od.IndexConsumerHeartbeat)
	m := map[canopen.NodeID]time.Duration{}
	if o != nil {
		for _, v := range o.Subs {
			if v.SubIndex == 0 {
				continue
			}
			x, err := d.GetUint(od.IndexConsumerHeartbeat, v.SubIndex)
			if err != nil {
				continue
			}
			if node, t, ok := ParseConsumerEntry(uint32(x)); ok {
				if _, dup := m[node]; !dup {
					m[node] = t
				}
			}
		}
	}
	c.mu.Lock()
	c.fromDict = m
	c.reconfigureLocked()
	c.mu.Unlock()
	c.poke()
}

// reconfigureLocked applies the effective configuration (explicit entries
// take precedence over 0x1016) to the node records.
func (c *HeartbeatConsumer) reconfigureLocked() {
	eff := map[canopen.NodeID]time.Duration{}
	for n, t := range c.fromDict {
		eff[n] = t
	}
	for n, t := range c.explicit {
		eff[n] = t
	}
	for n := range eff {
		if _, ok := c.nodes[n]; !ok {
			c.nodes[n] = &hbNode{state: canopen.StateUnknown}
		}
	}
	for n, r := range c.nodes {
		t := eff[n]
		if t == r.timeout {
			continue
		}
		r.timeout = t
		if t == 0 {
			r.active, r.lost = false, false
		} else if r.active {
			r.deadline = r.last.Add(t)
		}
	}
}

func (c *HeartbeatConsumer) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Monitor supervises a node with a consumer heartbeat time; 0 ends the
// supervision (the node's state is still tracked). It overrides an entry
// of 0x1016 for the node.
func (c *HeartbeatConsumer) Monitor(node canopen.NodeID, timeout time.Duration) error {
	if err := node.Check(); err != nil {
		return err
	}
	if timeout < 0 {
		return fmt.Errorf("nmt: negative heartbeat timeout")
	}
	c.mu.Lock()
	if timeout == 0 {
		delete(c.explicit, node)
	} else {
		c.explicit[node] = timeout
	}
	c.reconfigureLocked()
	c.mu.Unlock()
	c.poke()
	return nil
}

// Start subscribes to the error control messages and starts the
// supervision; with a dictionary it follows writes of 0x1016 from then on.
func (c *HeartbeatConsumer) Start() error {
	return c.lc.start(func(context.Context) (func(context.Context), error) {
		sub, err := c.bus.Subscribe(errorControlFilter)
		if err != nil {
			return nil, fmt.Errorf("nmt: subscribe: %w", err)
		}
		var unwatch func()
		if d := c.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, _ []byte) {
				if v.Index == od.IndexConsumerHeartbeat {
					c.loadDict()
				}
			})
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			if unwatch != nil {
				defer unwatch()
			}
			c.run(ctx, sub)
		}, nil
	})
}

func (c *HeartbeatConsumer) run(ctx context.Context, sub *canbus.Subscription) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		c.armTimer(timer)
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
		case <-timer.C:
			c.emit(c.checkTimeouts(time.Now()))
		case <-sub.Done():
			return
		case f := <-sub.Frames():
			c.emit(c.receive(&f, time.Now()))
		}
	}
}

// armTimer sets the timer to the earliest supervision deadline.
func (c *HeartbeatConsumer) armTimer(t *time.Timer) {
	c.mu.Lock()
	var next time.Time
	for _, r := range c.nodes {
		if r.active && !r.lost && r.timeout > 0 && (next.IsZero() || r.deadline.Before(next)) {
			next = r.deadline
		}
	}
	c.mu.Unlock()
	resetTimer(t, next)
}

func (c *HeartbeatConsumer) checkTimeouts(now time.Time) []HeartbeatEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var evs []HeartbeatEvent
	for n, r := range c.nodes {
		if !r.active || r.lost || r.timeout == 0 || now.Before(r.deadline) {
			continue
		}
		prev := r.state
		r.active, r.lost, r.state = false, true, canopen.StateUnknown
		c.stats.Timeouts++
		evs = append(evs, HeartbeatEvent{Kind: EventTimeout, Node: n, State: r.state, Previous: prev, Time: now})
	}
	if len(evs) > 0 {
		c.notifyLocked()
	}
	slices.SortFunc(evs, func(a, b HeartbeatEvent) int { return int(a.Node) - int(b.Node) })
	return evs
}

// receive processes one frame of the subscription.
func (c *HeartbeatConsumer) receive(f *canbus.Frame, now time.Time) []HeartbeatEvent {
	node, st, _, err := ParseErrorControl(f)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.stats.Invalid++
		return nil
	}
	c.stats.Received++
	r := c.nodes[node]
	if r == nil {
		r = &hbNode{state: canopen.StateUnknown}
		c.nodes[node] = r
	}
	var evs []HeartbeatEvent
	prev := r.state
	if r.lost {
		r.lost = false
		evs = append(evs, HeartbeatEvent{Kind: EventResumed, Node: node, State: st, Previous: prev, Time: now})
	}
	r.last, r.state = now, st
	if st == canopen.StateInitialising {
		r.bootups++
		r.lastBoot = now
		r.active = false
		c.stats.BootUps++
		evs = append(evs, HeartbeatEvent{Kind: EventBootUp, Node: node, State: st, Previous: prev, Time: now})
	} else {
		if r.timeout > 0 {
			r.active, r.deadline = true, now.Add(r.timeout)
		}
		if st != prev {
			evs = append(evs, HeartbeatEvent{Kind: EventStateChange, Node: node, State: st, Previous: prev, Time: now})
		}
	}
	c.notifyLocked()
	return evs
}

func (c *HeartbeatConsumer) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *HeartbeatConsumer) emit(evs []HeartbeatEvent) {
	for _, e := range evs {
		switch e.Kind {
		case EventTimeout:
			c.cfg.log.Warn("heartbeat timeout", "node", int(e.Node), "previous", e.Previous.String())
		default:
			c.cfg.log.Debug("heartbeat event", "kind", e.Kind.String(), "node", int(e.Node), "state", e.State.String())
		}
		if h := c.cfg.onHeartbeat; h != nil {
			h(e)
		}
	}
}

// State returns the last state a node reported (Unknown when never heard
// or after a timeout).
func (c *HeartbeatConsumer) State(node canopen.NodeID) canopen.NMTState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.nodes[node]; r != nil {
		return r.state
	}
	return canopen.StateUnknown
}

// Status returns what is known of a node.
func (c *HeartbeatConsumer) Status(node canopen.NodeID) NodeStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked(node)
}

func (c *HeartbeatConsumer) statusLocked(node canopen.NodeID) NodeStatus {
	r := c.nodes[node]
	if r == nil {
		return NodeStatus{Node: node, State: canopen.StateUnknown}
	}
	return NodeStatus{Node: node, State: r.state, Timeout: r.timeout, Active: r.active, Lost: r.lost,
		LastSeen: r.last, BootUps: r.bootups, LastBoot: r.lastBoot}
}

// Nodes returns the status of every node heard or configured, in node-ID
// order.
func (c *HeartbeatConsumer) Nodes() []NodeStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]NodeStatus, 0, len(c.nodes))
	for n := range c.nodes {
		out = append(out, c.statusLocked(n))
	}
	slices.SortFunc(out, func(a, b NodeStatus) int { return int(a.Node) - int(b.Node) })
	return out
}

// wait blocks until cond (evaluated under the lock) holds.
func (c *HeartbeatConsumer) wait(ctx context.Context, cond func() bool) error {
	for {
		c.mu.Lock()
		ok, ch := cond(), c.changed
		c.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return canopen.ErrClosed
		case <-ch:
		}
	}
}

// WaitState waits until a node reports a state (at once when it is the
// current one). It returns the context's error, or [canopen.ErrClosed]
// when the consumer is closed. To wait for a node to be lost, wait for
// [canopen.StateUnknown].
func (c *HeartbeatConsumer) WaitState(ctx context.Context, node canopen.NodeID, state canopen.NMTState) error {
	return c.wait(ctx, func() bool {
		r := c.nodes[node]
		return (r != nil && r.state == state) || (r == nil && state == canopen.StateUnknown)
	})
}

// WaitBootUp waits for a boot-up message of a node received at or after
// since. Take since before sending the NMT reset, so that a quick boot-up
// is not missed.
func (c *HeartbeatConsumer) WaitBootUp(ctx context.Context, node canopen.NodeID, since time.Time) error {
	return c.wait(ctx, func() bool {
		r := c.nodes[node]
		return r != nil && !r.lastBoot.IsZero() && !r.lastBoot.Before(since)
	})
}

// Close stops the consumer and waits for it. Pending waits return
// [canopen.ErrClosed].
func (c *HeartbeatConsumer) Close() error {
	c.mu.Lock()
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	c.mu.Unlock()
	c.lc.close()
	return nil
}

// Stats returns the counters.
func (c *HeartbeatConsumer) Stats() HeartbeatConsumerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
