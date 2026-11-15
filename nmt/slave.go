// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/callback"
	"github.com/otfabric/go-canopen/internal/goid"
	"github.com/otfabric/go-canopen/internal/lifecycle"
	"github.com/otfabric/go-canopen/od"
)

// ResetKind is the kind of an NMT reset.
type ResetKind uint8

// NMT resets (CiA 301 7.3.2.2.1).
const (
	// ResetApplication (NMT "reset node") restores the application areas
	// 0x2000..0x9FFF and then does a communication reset.
	ResetApplication ResetKind = iota + 1
	// ResetCommunication restores the communication area 0x1000..0x1FFF
	// and applies a pending node-ID.
	ResetCommunication
)

// String names the reset.
func (k ResetKind) String() string {
	switch k {
	case ResetApplication:
		return "reset-application"
	case ResetCommunication:
		return "reset-communication"
	}
	return fmt.Sprintf("ResetKind(%d)", uint8(k))
}

// Dictionary areas an NMT reset restores (CiA 301 7.3.2.2.1).
const (
	CommunicationAreaFirst uint16 = 0x1000
	CommunicationAreaLast  uint16 = 0x1FFF
	ApplicationAreaFirst   uint16 = 0x2000 // manufacturer-specific profile area
	ApplicationAreaLast    uint16 = 0x9FFF // end of the standardised profile area
)

// StateChange reports a transition of a [Slave].
type StateChange struct {
	Node canopen.NodeID   // the slave's node-ID after the change
	From canopen.NMTState // previous state
	To   canopen.NMTState // new state
	// Command is the NMT command that caused the change; 0 for a local
	// change (Start, SetState) and for the end of a reset (boot-up).
	Command canopen.NMTCommand
	Time    time.Time
}

// SlaveStats counts what a [Slave] did.
type SlaveStats struct {
	Commands uint64 `json:"commands"` // NMT commands executed
	Ignored  uint64 `json:"ignored"`  // commands for other nodes
	Invalid  uint64 `json:"invalid"`  // malformed NMT frames
	BootUps  uint64 `json:"bootUps"`  // boot-up messages sent
	Resets   uint64 `json:"resets"`   // resets done
}

// Slave is the NMT slave state machine of a node (CiA 301 7.3.2.2):
// Initialising, then (after the boot-up message) Pre-operational, and the
// transitions the NMT commands addressed to the node or to all nodes
// request. Reset node and reset communication pass through Initialising,
// restore the dictionary areas, call the reset handler and send a new
// boot-up message.
//
// A Slave is safe for concurrent use. Its state is the state source of a
// [HeartbeatProducer] and a [GuardResponder] ([Slave.State]).
type Slave struct {
	bus canopen.Bus
	cfg config
	lc  lifecycle.Service

	// ctx ends at Close: a closed slave changes no state, does no reset
	// and sends no boot-up message.
	ctx    context.Context
	cancel context.CancelFunc
	// resetSem serialises resets. It is a channel so that a reset
	// waiting for its turn ends at Close.
	resetSem  chan struct{}
	resetting atomic.Uint64 // identifier of the goroutine inside a reset

	mu      sync.Mutex
	node    canopen.NodeID
	pending canopen.NodeID // 0: none
	state   canopen.NMTState
	stats   SlaveStats
}

// Unconfigured is the node-ID of a device whose node-ID is not configured
// yet (CiA 305: 0xFF, the value of lss.Unconfigured). A [Slave] with it
// stays in Initialising: it sends no boot-up message and executes only
// reset commands sent to all nodes, until a reset applies a configured
// pending node-ID ([Slave.SetNodeID], usually from LSS).
const Unconfigured canopen.NodeID = 0xFF

// checkNodeID accepts 1..127 and [Unconfigured].
func checkNodeID(node canopen.NodeID) error {
	if node == Unconfigured {
		return nil
	}
	return node.Check()
}

// NewSlave returns the NMT slave of a node, in state Initialising until
// Start. node is 1..127, or [Unconfigured] for a device that waits for
// its node-ID (LSS). Options: [WithLogger], [WithDictionary],
// [WithStateHandler], [WithResetHandler], [WithFD].
func NewSlave(bus canopen.Bus, node canopen.NodeID, opts ...Option) (*Slave, error) {
	if err := checkNodeID(node); err != nil {
		return nil, err
	}
	s := &Slave{bus: bus, cfg: newConfig(opts), node: node, state: canopen.StateInitialising, resetSem: make(chan struct{}, 1)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// nmtFilter returns a new option each time: canbus.WithFilters's option
// writes to its captured FilterSet, so a shared one races between
// concurrent subscriptions.
func nmtFilter() canbus.SubscribeOption {
	return canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{
		ID: canopen.FuncNMT, Mask: canbus.MaxStandardID, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly,
	}}})
}

// Start subscribes to NMT commands, sends the boot-up message and enters
// Pre-operational, then executes received commands until Close. When the
// boot-up message cannot be sent, Start fails and the slave stays in
// Initialising. A slave whose node-ID is [Unconfigured] sends no boot-up
// message and stays in Initialising; the reset that applies its node-ID
// boots it.
func (s *Slave) Start() error {
	return s.lc.Start(func(ctx context.Context) (func(context.Context), error) {
		sub, err := s.bus.Subscribe(nmtFilter())
		if err != nil {
			return nil, fmt.Errorf("nmt: subscribe: %w", err)
		}
		if s.NodeID() != Unconfigured {
			if err := s.bootUp(ctx); err != nil {
				_ = sub.Close()
				return nil, err
			}
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				s.handle(ctx, &f)
			}
		}, nil
	})
}

// Close stops the slave and waits for its goroutine and for a reset in
// progress, which then sends no boot-up message. It does not change the
// state. It is safe to call more than once and from several goroutines.
//
// A handler of the slave may call Close: Close then does not wait for
// the goroutine or the reset the handler runs in, which end when the
// handler returns. From a handler called by Start, Start returns
// [canopen.ErrClosed].
func (s *Slave) Close() error {
	s.cancel()
	s.lc.Close()
	if id := goid.ID(); id == 0 || s.resetting.Load() != id {
		// A reset in progress on another goroutine: wait for it.
		s.resetSem <- struct{}{}
		<-s.resetSem
	}
	return nil
}

// bootUp sends the boot-up message and enters Pre-operational.
func (s *Slave) bootUp(ctx context.Context) error {
	node := s.NodeID()
	f := s.cfg.frame(ErrorControlFrame(node, canopen.StateInitialising, false))
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)() // Close ends the send
	if h := s.cfg.onBootUp; h != nil {
		callback.Do(s.cfg.log, "boot-up handler", h)
	}
	if s.ctx.Err() != nil {
		return canopen.ErrClosed
	}
	if err := s.bus.Send(sctx, &f); err != nil {
		if s.ctx.Err() != nil {
			return canopen.ErrClosed
		}
		return fmt.Errorf("nmt: node %d boot-up: %w", node, err)
	}
	s.mu.Lock()
	s.stats.BootUps++
	s.mu.Unlock()
	s.cfg.log.Info("nmt boot-up", "node", int(node))
	s.setState(canopen.StatePreOperational, 0)
	return nil
}

func (s *Slave) handle(ctx context.Context, f *canbus.Frame) {
	cmd, node, err := parseCommand(f, s.cfg.fd)
	s.mu.Lock()
	switch {
	case err != nil:
		s.stats.Invalid++
	case node != canopen.BroadcastNodeID && node != s.node,
		// Unconfigured: only resets (to all nodes) may apply a pending
		// node-ID; the state stays Initialising otherwise.
		s.node == Unconfigured && cmd != canopen.NMTResetNode && cmd != canopen.NMTResetCommunication:
		s.stats.Ignored++
		err = errIgnore
	default:
		s.stats.Commands++
	}
	s.mu.Unlock()
	if err != nil {
		if err != errIgnore {
			s.cfg.log.Debug("nmt invalid command frame", "error", err)
		}
		return
	}
	s.cfg.log.Debug("nmt command", "command", cmd.String(), "node", int(node))
	switch cmd {
	case canopen.NMTStart:
		s.setState(canopen.StateOperational, cmd)
	case canopen.NMTStop:
		s.setState(canopen.StateStopped, cmd)
	case canopen.NMTEnterPreOperational:
		s.setState(canopen.StatePreOperational, cmd)
	case canopen.NMTResetNode:
		_ = s.reset(ctx, ResetApplication, cmd)
	case canopen.NMTResetCommunication:
		_ = s.reset(ctx, ResetCommunication, cmd)
	}
}

var errIgnore = fmt.Errorf("ignore")

// setState changes the state and calls the state handler on a change.
func (s *Slave) setState(to canopen.NMTState, cmd canopen.NMTCommand) {
	s.mu.Lock()
	from := s.state
	s.state = to
	node := s.node
	s.mu.Unlock()
	if from == to {
		return
	}
	s.cfg.log.Info("nmt state", "node", int(node), "from", from.String(), "to", to.String())
	if h := s.cfg.onState; h != nil {
		sc := StateChange{Node: node, From: from, To: to, Command: cmd, Time: time.Now()}
		callback.Do(s.cfg.log, "state handler", func() { h(sc) })
	}
}

// reset runs an NMT reset: Initialising, dictionary restore, reset
// handler, boot-up, Pre-operational.
func (s *Slave) reset(ctx context.Context, kind ResetKind, cmd canopen.NMTCommand) error {
	select {
	case s.resetSem <- struct{}{}:
	case <-s.ctx.Done():
		return canopen.ErrClosed
	}
	defer func() { <-s.resetSem }()
	if s.ctx.Err() != nil {
		return canopen.ErrClosed
	}
	s.resetting.Store(goid.ID())
	defer s.resetting.Store(0)
	s.setState(canopen.StateInitialising, cmd)
	s.mu.Lock()
	s.stats.Resets++
	if s.pending != 0 {
		s.node, s.pending = s.pending, 0
	}
	node := s.node
	s.mu.Unlock()
	if d := s.cfg.dict; d != nil {
		if kind == ResetApplication {
			s.restore(d, ApplicationAreaFirst, ApplicationAreaLast)
		}
		d.SetNodeID(node)
		s.restore(d, CommunicationAreaFirst, CommunicationAreaLast)
	}
	if h := s.cfg.onReset; h != nil {
		callback.Do(s.cfg.log, "reset handler", func() { h(kind) })
	}
	if s.ctx.Err() != nil {
		// Closed meanwhile: no boot-up message.
		return canopen.ErrClosed
	}
	if node == Unconfigured {
		s.cfg.log.Info("nmt reset: node-ID unconfigured, no boot-up")
		return nil
	}
	err := s.bootUp(ctx)
	if err != nil {
		s.cfg.log.Warn("nmt reset", "node", int(node), "error", err)
	}
	return err
}

// restore sets the objects of an area back to their defaults. That
// notifies the dictionary's watchers, which are the application's code:
// one that does not return normally ends that pass after its value was
// restored, so the restore is taken up again until it is complete (each
// pass restores at least one more value).
func (s *Slave) restore(d *od.Dictionary, first, last uint16) {
	for !callback.Do(s.cfg.log, "dictionary watcher", func() { d.Reset(first, last) }) {
	}
}

// Reset does an NMT reset locally, as the command from the bus would:
// state Initialising, dictionary restore, reset handler, boot-up message,
// Pre-operational. It returns the error of sending the boot-up message
// (the slave then stays in Initialising) and [canopen.ErrClosed] after
// Close. A reset that leaves the node-ID [Unconfigured] ends in
// Initialising without boot-up message. A reset in progress when the
// slave is closed sends no boot-up message and returns
// [canopen.ErrClosed].
func (s *Slave) Reset(ctx context.Context, kind ResetKind) error {
	if kind != ResetApplication && kind != ResetCommunication {
		return fmt.Errorf("nmt: invalid reset kind %d", kind)
	}
	return s.reset(ctx, kind, 0)
}

// State returns the current NMT state.
func (s *Slave) State() canopen.NMTState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// SetState changes the state locally (an application that enters
// Operational on its own, or stops after a fatal error). Only Stopped,
// Operational and Pre-operational can be set; use Reset for the others.
// After Close it fails with [canopen.ErrClosed].
func (s *Slave) SetState(st canopen.NMTState) error {
	switch st {
	case canopen.StateStopped, canopen.StateOperational, canopen.StatePreOperational:
	default:
		return fmt.Errorf("nmt: cannot set state %s", st)
	}
	if s.ctx.Err() != nil {
		return canopen.ErrClosed
	}
	s.setState(st, 0)
	return nil
}

// NodeID returns the active node-ID.
func (s *Slave) NodeID() canopen.NodeID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

// SetNodeID sets a pending node-ID (as LSS configures one): it becomes
// active at the next reset communication or reset node, before the
// dictionary is restored, so that node-ID-relative defaults follow it.
// [Unconfigured] makes the node unconfigured at that reset (it then stays
// in Initialising).
func (s *Slave) SetNodeID(node canopen.NodeID) error {
	if err := checkNodeID(node); err != nil {
		return err
	}
	s.mu.Lock()
	s.pending = node
	s.mu.Unlock()
	return nil
}

// Stats returns the counters.
func (s *Slave) Stats() SlaveStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
