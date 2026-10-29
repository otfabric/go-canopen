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

// GuardEventKind is the kind of a [GuardEvent].
type GuardEventKind uint8

// Node guarding events.
const (
	// GuardTimeout: a [GuardMaster] got no valid response for
	// lifeTimeFactor guard periods (a node guarding event), or a
	// [GuardResponder] got no guard request within its node life time (a
	// life guarding event). Reported once per loss.
	GuardTimeout GuardEventKind = iota + 1
	// GuardResumed: communication is back after a GuardTimeout.
	GuardResumed
	// GuardStateChange: a guarded node reported another state.
	GuardStateChange
	// GuardToggleError: a guarded node's response did not alternate the
	// toggle bit. The response counts as missing.
	GuardToggleError
	// GuardBootUp: a guarded node sent a boot-up message; its next
	// response must have the toggle bit clear.
	GuardBootUp
)

// String names the event kind.
func (k GuardEventKind) String() string {
	switch k {
	case GuardTimeout:
		return "timeout"
	case GuardResumed:
		return "resumed"
	case GuardStateChange:
		return "state-change"
	case GuardToggleError:
		return "toggle-error"
	case GuardBootUp:
		return "boot-up"
	}
	return fmt.Sprintf("GuardEventKind(%d)", uint8(k))
}

// GuardEvent is reported by a [GuardMaster] (about a guarded node) or a
// [GuardResponder] (about its own node: life guarding).
type GuardEvent struct {
	Kind  GuardEventKind
	Node  canopen.NodeID
	State canopen.NMTState // the node's state after the event (Unknown after a GuardMaster timeout)
	Time  time.Time
}

// GuardStats counts what a [GuardMaster] or a [GuardResponder] did.
type GuardStats struct {
	Requests     uint64 `json:"requests"`     // remote frames sent (master) or answered (responder)
	Responses    uint64 `json:"responses"`    // valid responses received (master)
	Missed       uint64 `json:"missed"`       // guard periods without a valid response (master)
	ToggleErrors uint64 `json:"toggleErrors"` // responses with a wrong toggle bit (master)
	Timeouts     uint64 `json:"timeouts"`     // GuardTimeout events
	Errors       uint64 `json:"errors"`       // send failures
}

// GuardMaster guards nodes with the legacy node guarding protocol (CiA 301
// 7.2.8.3.2.1): every guard time it sends a remote frame to 0x700 +
// node-ID and expects the node's state with an alternating toggle bit
// before the next request. A missing response or one with a wrong toggle
// bit is a miss; lifeTimeFactor consecutive misses (at least one) are a
// node guarding event ([GuardTimeout]).
//
// The first response after Guard may have either toggle value; after a
// boot-up message the next one must have the toggle bit clear.
type GuardMaster struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	mu    sync.Mutex
	nodes map[canopen.NodeID]*guarded
	stats GuardStats
}

type guarded struct {
	guard       time.Duration
	factor      uint8
	next        time.Time
	outstanding bool
	misses      int
	toggleKnown bool
	toggle      bool // expected toggle bit of the next response
	state       canopen.NMTState
	lost        bool
}

// NewGuardMaster returns a node guarding master. Options: [WithLogger],
// [WithGuardHandler].
func NewGuardMaster(bus canopen.Bus, opts ...Option) *GuardMaster {
	return &GuardMaster{bus: bus, cfg: newConfig(opts), wake: make(chan struct{}, 1), nodes: map[canopen.NodeID]*guarded{}}
}

// Guard starts (or reconfigures) guarding a node: the first request is
// sent at once, then every guardTime. A guardTime of 0 stops guarding the
// node.
func (g *GuardMaster) Guard(node canopen.NodeID, guardTime time.Duration, lifeTimeFactor uint8) error {
	if err := node.Check(); err != nil {
		return err
	}
	if guardTime < 0 {
		return fmt.Errorf("nmt: negative guard time")
	}
	g.mu.Lock()
	if guardTime == 0 {
		delete(g.nodes, node)
	} else {
		g.nodes[node] = &guarded{guard: guardTime, factor: lifeTimeFactor, next: time.Now(), state: canopen.StateUnknown}
	}
	g.mu.Unlock()
	g.poke()
	return nil
}

func (g *GuardMaster) poke() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// State returns the last state a guarded node reported; Unknown before its
// first response, after a timeout and for nodes not guarded.
func (g *GuardMaster) State(node canopen.NodeID) canopen.NMTState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r := g.nodes[node]; r != nil {
		return r.state
	}
	return canopen.StateUnknown
}

// Start subscribes to the responses and starts guarding.
func (g *GuardMaster) Start() error {
	return g.lc.start(func(context.Context) (func(context.Context), error) {
		sub, err := g.bus.Subscribe(errorControlFilter)
		if err != nil {
			return nil, fmt.Errorf("nmt: subscribe: %w", err)
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			g.run(ctx, sub)
		}, nil
	})
}

func (g *GuardMaster) run(ctx context.Context, sub *canbus.Subscription) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		g.mu.Lock()
		var next time.Time
		for _, r := range g.nodes {
			if next.IsZero() || r.next.Before(next) {
				next = r.next
			}
		}
		g.mu.Unlock()
		resetTimer(timer, next)
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case <-g.wake:
		case <-timer.C:
			g.tick(ctx, time.Now())
		case f := <-sub.Frames():
			g.emit(g.receive(&f, time.Now()))
		}
	}
}

// resetTimer sets t to fire at next (in an hour when next is zero).
func resetTimer(t *time.Timer, next time.Time) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	if next.IsZero() {
		t.Reset(time.Hour)
		return
	}
	t.Reset(max(time.Until(next), 0))
}

// tick sends the requests that are due and counts unanswered ones.
func (g *GuardMaster) tick(ctx context.Context, now time.Time) {
	var (
		evs  []GuardEvent
		send []canopen.NodeID
	)
	g.mu.Lock()
	for n, r := range g.nodes {
		if now.Before(r.next) {
			continue
		}
		if r.outstanding {
			g.stats.Missed++
			evs = append(evs, r.miss(g, n, now)...)
		}
		r.outstanding = true
		r.next = r.next.Add(r.guard)
		if r.next.Before(now) {
			r.next = now.Add(r.guard)
		}
		send = append(send, n)
	}
	g.mu.Unlock()
	slices.Sort(send)
	g.emit(evs)
	for _, n := range send {
		f := GuardRequest(n)
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := g.bus.Send(sctx, &f)
		cancel()
		g.mu.Lock()
		if err != nil {
			g.stats.Errors++
		} else {
			g.stats.Requests++
		}
		g.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			g.cfg.log.Warn("node guarding request", "node", int(n), "error", err)
		}
	}
}

// miss counts a missing or invalid response; called with g.mu held.
func (r *guarded) miss(g *GuardMaster, n canopen.NodeID, now time.Time) []GuardEvent {
	r.misses++
	if r.lost || r.misses < max(int(r.factor), 1) {
		return nil
	}
	r.lost, r.state = true, canopen.StateUnknown
	g.stats.Timeouts++
	return []GuardEvent{{Kind: GuardTimeout, Node: n, State: r.state, Time: now}}
}

func (g *GuardMaster) receive(f *canbus.Frame, now time.Time) []GuardEvent {
	node, st, toggle, err := ParseErrorControl(f)
	if err != nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.nodes[node]
	if r == nil {
		return nil
	}
	if st == canopen.StateInitialising && !toggle {
		// A boot-up message: the node restarts its toggle bit.
		r.toggleKnown, r.toggle = true, false
		return []GuardEvent{{Kind: GuardBootUp, Node: node, State: st, Time: now}}
	}
	if !r.outstanding {
		return nil // a heartbeat or a late response
	}
	r.outstanding = false
	if r.toggleKnown && toggle != r.toggle {
		g.stats.ToggleErrors++
		r.toggle = !toggle
		evs := []GuardEvent{{Kind: GuardToggleError, Node: node, State: r.state, Time: now}}
		return append(evs, r.miss(g, node, now)...)
	}
	g.stats.Responses++
	r.toggleKnown, r.toggle, r.misses = true, !toggle, 0
	var evs []GuardEvent
	if r.lost {
		r.lost = false
		evs = append(evs, GuardEvent{Kind: GuardResumed, Node: node, State: st, Time: now})
	}
	if st != r.state {
		r.state = st
		evs = append(evs, GuardEvent{Kind: GuardStateChange, Node: node, State: st, Time: now})
	}
	return evs
}

func (g *GuardMaster) emit(evs []GuardEvent) { emitGuard(g.cfg, evs) }

func emitGuard(cfg config, evs []GuardEvent) {
	for _, e := range evs {
		if e.Kind == GuardTimeout || e.Kind == GuardToggleError {
			cfg.log.Warn("node guarding", "kind", e.Kind.String(), "node", int(e.Node))
		} else {
			cfg.log.Debug("node guarding", "kind", e.Kind.String(), "node", int(e.Node), "state", e.State.String())
		}
		if h := cfg.onGuard; h != nil {
			h(e)
		}
	}
}

// Close stops guarding and waits for the goroutine.
func (g *GuardMaster) Close() error {
	g.lc.close()
	return nil
}

// Stats returns the counters.
func (g *GuardMaster) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// GuardResponder answers the node guarding requests of an NMT master for
// its node: each remote frame on 0x700 + node-ID gets the NMT state with a
// toggle bit that alternates, starting at 0 (and again at 0 after
// ResetToggle, which a reset communication calls for). Nothing is sent
// while the state is Initialising.
//
// With a guard time and a life time factor configured it also does life
// guarding: after the first request, no request within guard time × life
// time factor is a life guarding event ([GuardTimeout]), reported once
// until the next request ([GuardResumed]).
//
// CiA 301 does not allow node guarding and the heartbeat at once: run the
// responder only while the producer heartbeat time is 0.
type GuardResponder struct {
	bus   canopen.Bus
	cfg   config
	lc    lifecycle
	state func() canopen.NMTState
	wake  chan struct{}

	mu        sync.Mutex
	node      canopen.NodeID
	toggle    bool
	guardTime time.Duration
	factor    uint8
	armed     bool
	lost      bool
	deadline  time.Time
	stats     GuardStats
}

// NewGuardResponder returns the node guarding responder of a node.
// Options: [WithLogger], [WithGuardHandler], [WithLifeGuarding],
// [WithDictionary] (0x100C and 0x100D, which must exist).
func NewGuardResponder(bus canopen.Bus, node canopen.NodeID, state func() canopen.NMTState, opts ...Option) (*GuardResponder, error) {
	if err := node.Check(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("nmt: guard responder needs a state source")
	}
	r := &GuardResponder{bus: bus, cfg: newConfig(opts), state: state, node: node, wake: make(chan struct{}, 1)}
	if d := r.cfg.dict; d != nil {
		gt, err := d.GetUint(od.IndexGuardTime, 0)
		if err != nil {
			return nil, fmt.Errorf("nmt: guard time 0x100C: %w", err)
		}
		lf, err := d.GetUint(od.IndexLifeTimeFactor, 0)
		if err != nil {
			return nil, fmt.Errorf("nmt: life time factor 0x100D: %w", err)
		}
		r.guardTime, r.factor = time.Duration(gt)*time.Millisecond, uint8(lf)
	}
	if r.cfg.guardSet {
		r.guardTime, r.factor = r.cfg.guardTime, r.cfg.lifeFactor
	}
	return r, nil
}

// SetLifeGuarding changes the guard time and life time factor; either 0
// turns life guarding off. A running supervision restarts from now.
func (r *GuardResponder) SetLifeGuarding(guardTime time.Duration, lifeTimeFactor uint8) {
	r.mu.Lock()
	r.guardTime, r.factor = max(guardTime, 0), lifeTimeFactor
	if r.armed {
		r.deadline = time.Now().Add(r.lifeTimeLocked())
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *GuardResponder) lifeTimeLocked() time.Duration {
	return r.guardTime * time.Duration(r.factor)
}

// ResetToggle makes the next response start with the toggle bit clear, as
// after a boot-up.
func (r *GuardResponder) ResetToggle() {
	r.mu.Lock()
	r.toggle = false
	r.mu.Unlock()
}

// SetNodeID changes the node-ID the responder answers for.
func (r *GuardResponder) SetNodeID(node canopen.NodeID) error {
	if err := node.Check(); err != nil {
		return err
	}
	r.mu.Lock()
	r.node = node
	r.mu.Unlock()
	return nil
}

// Start subscribes to the guard requests and starts answering; with a
// dictionary it follows writes of 0x100C and 0x100D from then on.
func (r *GuardResponder) Start() error {
	return r.lc.start(func(context.Context) (func(context.Context), error) {
		sub, err := r.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{
			ID: canopen.FuncHeartbeat, Mask: 0x780, Format: canbus.FormatStandard, Remote: canbus.RemoteOnly,
		}}}))
		if err != nil {
			return nil, fmt.Errorf("nmt: subscribe: %w", err)
		}
		var unwatch func()
		if d := r.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, _ []byte) {
				if v.Index == od.IndexGuardTime || v.Index == od.IndexLifeTimeFactor {
					gt, _ := d.GetUint(od.IndexGuardTime, 0)
					lf, _ := d.GetUint(od.IndexLifeTimeFactor, 0)
					r.SetLifeGuarding(time.Duration(gt)*time.Millisecond, uint8(lf))
				}
			})
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			if unwatch != nil {
				defer unwatch()
			}
			r.run(ctx, sub)
		}, nil
	})
}

func (r *GuardResponder) run(ctx context.Context, sub *canbus.Subscription) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		r.mu.Lock()
		var next time.Time
		if r.armed && !r.lost && r.lifeTimeLocked() > 0 {
			next = r.deadline
		}
		r.mu.Unlock()
		resetTimer(timer, next)
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case <-r.wake:
		case <-timer.C:
			emitGuard(r.cfg, r.checkLife(time.Now()))
		case f := <-sub.Frames():
			r.answer(ctx, &f)
		}
	}
}

func (r *GuardResponder) checkLife(now time.Time) []GuardEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.armed || r.lost || r.lifeTimeLocked() == 0 || now.Before(r.deadline) {
		return nil
	}
	r.lost = true
	r.stats.Timeouts++
	return []GuardEvent{{Kind: GuardTimeout, Node: r.node, State: r.state(), Time: now}}
}

func (r *GuardResponder) answer(ctx context.Context, f *canbus.Frame) {
	r.mu.Lock()
	node := r.node
	if f.ID != canopen.FuncHeartbeat+canbus.ID(node) {
		r.mu.Unlock()
		return
	}
	now := time.Now()
	var evs []GuardEvent
	st := r.state()
	if r.lost {
		r.lost = false
		evs = append(evs, GuardEvent{Kind: GuardResumed, Node: node, State: st, Time: now})
	}
	r.armed, r.deadline = true, now.Add(r.lifeTimeLocked())
	if st == canopen.StateInitialising || st == canopen.StateUnknown {
		r.mu.Unlock()
		emitGuard(r.cfg, evs)
		return
	}
	toggle := r.toggle
	r.toggle = !r.toggle
	r.mu.Unlock()
	emitGuard(r.cfg, evs)
	resp := ErrorControlFrame(node, st, toggle)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := r.bus.Send(sctx, &resp)
	cancel()
	r.mu.Lock()
	if err != nil {
		r.stats.Errors++
	} else {
		r.stats.Requests++
	}
	r.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		r.cfg.log.Warn("node guarding response", "node", int(node), "error", err)
	}
}

// Close stops answering and waits for the goroutine.
func (r *GuardResponder) Close() error {
	r.lc.close()
	return nil
}

// Stats returns the counters.
func (r *GuardResponder) Stats() GuardStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}
