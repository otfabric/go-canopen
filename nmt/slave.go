// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
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
	lc  lifecycle

	resetMu sync.Mutex // serialises resets
	mu      sync.Mutex
	node    canopen.NodeID
	pending canopen.NodeID // 0: none
	state   canopen.NMTState
	stats   SlaveStats
}

// NewSlave returns the NMT slave of a node, in state Initialising until
// Start. Options: [WithLogger], [WithDictionary], [WithStateHandler],
// [WithResetHandler].
func NewSlave(bus canopen.Bus, node canopen.NodeID, opts ...Option) (*Slave, error) {
	if err := node.Check(); err != nil {
		return nil, err
	}
	return &Slave{bus: bus, cfg: newConfig(opts), node: node, state: canopen.StateInitialising}, nil
}

var nmtFilter = canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{
	ID: canopen.FuncNMT, Mask: canbus.MaxStandardID, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly,
}}})

// Start subscribes to NMT commands, sends the boot-up message and enters
// Pre-operational, then executes received commands until Close. When the
// boot-up message cannot be sent, Start fails and the slave stays in
// Initialising.
func (s *Slave) Start() error {
	return s.lc.start(func(ctx context.Context) (func(context.Context), error) {
		sub, err := s.bus.Subscribe(nmtFilter)
		if err != nil {
			return nil, fmt.Errorf("nmt: subscribe: %w", err)
		}
		if err := s.bootUp(ctx); err != nil {
			_ = sub.Close()
			return nil, err
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

// Close stops the slave and waits for it. It does not change the state.
func (s *Slave) Close() error {
	s.lc.close()
	return nil
}

// bootUp sends the boot-up message and enters Pre-operational.
func (s *Slave) bootUp(ctx context.Context) error {
	node := s.NodeID()
	f := ErrorControlFrame(node, canopen.StateInitialising, false)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.bus.Send(sctx, &f); err != nil {
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
	cmd, node, err := ParseCommand(f)
	s.mu.Lock()
	switch {
	case err != nil:
		s.stats.Invalid++
	case node != canopen.BroadcastNodeID && node != s.node:
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
		h(StateChange{Node: node, From: from, To: to, Command: cmd, Time: time.Now()})
	}
}

// reset runs an NMT reset: Initialising, dictionary restore, reset
// handler, boot-up, Pre-operational.
func (s *Slave) reset(ctx context.Context, kind ResetKind, cmd canopen.NMTCommand) error {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()
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
			d.Reset(ApplicationAreaFirst, ApplicationAreaLast)
		}
		d.SetNodeID(node)
		d.Reset(CommunicationAreaFirst, CommunicationAreaLast)
	}
	if h := s.cfg.onReset; h != nil {
		h(kind)
	}
	err := s.bootUp(ctx)
	if err != nil {
		s.cfg.log.Warn("nmt reset", "node", int(node), "error", err)
	}
	return err
}

// Reset does an NMT reset locally, as the command from the bus would:
// state Initialising, dictionary restore, reset handler, boot-up message,
// Pre-operational. It returns the error of sending the boot-up message
// (the slave then stays in Initialising) and [canopen.ErrClosed] after
// Close.
func (s *Slave) Reset(ctx context.Context, kind ResetKind) error {
	if kind != ResetApplication && kind != ResetCommunication {
		return fmt.Errorf("nmt: invalid reset kind %d", kind)
	}
	if s.lc.closed() {
		return canopen.ErrClosed
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
func (s *Slave) SetState(st canopen.NMTState) error {
	switch st {
	case canopen.StateStopped, canopen.StateOperational, canopen.StatePreOperational:
	default:
		return fmt.Errorf("nmt: cannot set state %s", st)
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
func (s *Slave) SetNodeID(node canopen.NodeID) error {
	if err := node.Check(); err != nil {
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
