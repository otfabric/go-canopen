// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Settings are the pending layer settings of a [Slave]: what "configure
// node-ID" and "configure bit timing parameters" set, what "store
// configuration" persists and what "activate bit timing parameters"
// switches to.
type Settings struct {
	// NodeID is the pending node-ID (1..127, or Unconfigured).
	NodeID canopen.NodeID `json:"nodeId"`
	// BitTimingSet reports whether a bit timing was configured; Selector
	// and BitTiming are meaningful only then.
	BitTimingSet bool      `json:"bitTimingSet"`
	Selector     uint8     `json:"tableSelector"`
	BitTiming    BitTiming `json:"tableIndex"`
}

// SlaveStats counts what a [Slave] did.
type SlaveStats struct {
	Received   uint64 `json:"received"`   // LSS master frames processed
	Ignored    uint64 `json:"ignored"`    // frames not valid in the current state, malformed or not addressed to this slave
	Responses  uint64 `json:"responses"`  // responses sent
	SendErrors uint64 `json:"sendErrors"` // responses that could not be sent
	Selected   uint64 `json:"selected"`   // entries into the configuration state
	NodeIDs    uint64 `json:"nodeIds"`    // node-IDs configured
	Refused    uint64 `json:"refused"`    // configuration services answered with an error code
	Stores     uint64 `json:"stores"`     // configurations stored
}

// SlaveOption configures a [Slave].
type SlaveOption func(*slaveConfig)

type slaveConfig struct {
	log      *slog.Logger
	state    func(State)
	nodeID   func(canopen.NodeID) error
	reset    func(canopen.NodeID)
	bitRate  func(selector uint8, b BitTiming) error
	activate func(s Settings, delay time.Duration)
	store    func(s Settings) error
	// fastscanAny makes the slave answer Fastscan whatever its node-ID,
	// as Lely's does (tests of the master).
	fastscanAny bool
}

// WithSlaveLogger logs slave events to l. The default logs nothing.
func WithSlaveLogger(l *slog.Logger) SlaveOption { return func(c *slaveConfig) { c.log = l } }

// WithStateHandler calls h with every change of the LSS state.
func WithStateHandler(h func(State)) SlaveOption { return func(c *slaveConfig) { c.state = h } }

// WithNodeIDHandler calls h when the master configures a node-ID (1..127
// or Unconfigured; other values are refused with CodeOutOfRange without
// calling h). An error refuses it: a [*ConfigError] chooses the error
// code, any other error answers CodeOutOfRange. When h accepts, the value
// becomes the pending node-ID. The node integrator stores it as the
// node-ID the next NMT reset communication applies (nmt.Slave.SetNodeID)
// and then reports the applied node-ID with [Slave.SetNodeID].
func WithNodeIDHandler(h func(canopen.NodeID) error) SlaveOption {
	return func(c *slaveConfig) { c.nodeID = h }
}

// WithResetHandler calls h when a slave whose active node-ID is
// Unconfigured leaves the configuration state ("switch state global" to
// waiting) with a configured pending node-ID: as CANopenNode does, the
// device then enters NMT reset communication on its own, with the pending
// node-ID (h does that). The slave takes the node-ID as its active one
// after h returns.
func WithResetHandler(h func(canopen.NodeID)) SlaveOption {
	return func(c *slaveConfig) { c.reset = h }
}

// WithBitTimingHandler calls h when the master configures bit timing
// parameters (table selector, table index). An error refuses them (a
// [*ConfigError] chooses the code, others answer CodeOutOfRange); success
// makes them pending. Without h every request is refused with
// CodeOutOfRange, as is index > 9 of table 0 (h is not called).
func WithBitTimingHandler(h func(selector uint8, b BitTiming) error) SlaveOption {
	return func(c *slaveConfig) { c.bitRate = h }
}

// WithActivateHandler calls h on "activate bit timing parameters" with the
// pending settings and the switch delay. CiA 305 (as python canopen and
// CANopenNode describe it) has the device wait the switch delay, switch
// to the new bit rate, and send nothing for another switch delay; h does
// that. The service has no response.
func WithActivateHandler(h func(s Settings, delay time.Duration)) SlaveOption {
	return func(c *slaveConfig) { c.activate = h }
}

// WithStoreHandler calls h on "store configuration" with the pending
// settings, to persist them. An error answers CodeStoreFailed (or the
// code of a [*ConfigError]). Without h the slave answers CodeOutOfRange
// ("store configuration not supported").
func WithStoreHandler(h func(Settings) error) SlaveOption {
	return func(c *slaveConfig) { c.store = h }
}

// Slave is an LSS slave (CiA 305): the LSS state machine of one device.
// It answers on 0x7E4 the LSS requests the master sends on 0x7E5. The
// layer settings it receives reach the device through handlers: the
// slave keeps them pending and never changes the device itself.
type Slave struct {
	bus canopen.Bus
	cfg slaveConfig
	lc  lifecycle

	mu      sync.Mutex
	addr    Address
	state   State
	active  canopen.NodeID
	pending Settings
	selNext Command // next expected "switch state selective" command; 0: none
	idNext  Command // next expected "identify remote slave" command; 0: none
	fsPos   Part    // Fastscan position (LSSPos)
	stats   SlaveStats
}

// NewSlave returns the LSS slave of a device with LSS address addr and
// node-ID node (1..127, or Unconfigured). The slave starts in the waiting
// state. Options: [WithSlaveLogger], [WithStateHandler],
// [WithNodeIDHandler], [WithResetHandler], [WithBitTimingHandler],
// [WithActivateHandler], [WithStoreHandler].
func NewSlave(bus canopen.Bus, addr Address, node canopen.NodeID, opts ...SlaveOption) (*Slave, error) {
	if !validNodeID(node) {
		return nil, fmt.Errorf("lss: %w: %d", canopen.ErrInvalidNodeID, node)
	}
	s := &Slave{bus: bus, addr: addr, active: node, pending: Settings{NodeID: node}}
	for _, o := range opts {
		if o != nil {
			o(&s.cfg)
		}
	}
	if s.cfg.log == nil {
		s.cfg.log = discard
	}
	return s, nil
}

func validNodeID(n canopen.NodeID) bool { return n.Valid() || n == Unconfigured }

// Start subscribes to the LSS master identifier (before it returns) and
// starts answering.
func (s *Slave) Start() error {
	return s.lc.start(func(context.Context) (func(context.Context), error) {
		flt := canbus.ExactStandard(MasterID)
		flt.Remote = canbus.RemoteDataOnly
		sub, err := s.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{flt}}))
		if err != nil {
			return nil, fmt.Errorf("lss: subscribe: %w", err)
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			for {
				select {
				case <-ctx.Done():
					return
				case <-sub.Done():
					return
				case f := <-sub.Frames():
					if r, ok := s.handle(&f); ok {
						s.send(ctx, &r)
					}
				}
			}
		}, nil
	})
}

func (s *Slave) send(ctx context.Context, f *canbus.Frame) {
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	err := s.bus.Send(sctx, f)
	s.mu.Lock()
	if err != nil {
		s.stats.SendErrors++
	} else {
		s.stats.Responses++
	}
	s.mu.Unlock()
	if err != nil {
		s.cfg.log.Warn("lss response", "cs", Command(f.Data[0]), "error", err)
	}
}

// Close stops the slave and waits for it. It is idempotent.
func (s *Slave) Close() error {
	s.lc.close()
	return nil
}

// State returns the LSS state.
func (s *Slave) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// NodeID returns the active node-ID (Unconfigured before one was
// applied).
func (s *Slave) NodeID() canopen.NodeID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// SetNodeID sets the active and the pending node-ID, as the device has
// applied them (after an NMT reset communication, or loaded from
// persistent storage at boot).
func (s *Slave) SetNodeID(node canopen.NodeID) error {
	if !validNodeID(node) {
		return fmt.Errorf("lss: %w: %d", canopen.ErrInvalidNodeID, node)
	}
	s.mu.Lock()
	s.active, s.pending.NodeID = node, node
	s.mu.Unlock()
	return nil
}

// Pending returns the pending settings.
func (s *Slave) Pending() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Address returns the slave's LSS address.
func (s *Slave) Address() Address { return s.addr }

// Stats returns the counters.
func (s *Slave) Stats() SlaveStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// setState changes the state (with s.mu held) and reports whether it
// changed.
func (s *Slave) setState(st State) bool {
	if s.state == st {
		return false
	}
	s.state = st
	if st == Configuration {
		s.stats.Selected++
	}
	return true
}

// unconfigured reports (with s.mu held) whether neither an active nor a
// pending node-ID is configured: the condition for Fastscan and for
// "identify non-configured remote slave".
func (s *Slave) unconfigured() bool {
	return s.active == Unconfigured && s.pending.NodeID == Unconfigured
}

// handle processes one frame from the master. It runs on the service
// goroutine (or a test's) and calls the handlers without the lock. It
// returns the response to send, if any.
func (s *Slave) handle(f *canbus.Frame) (resp canbus.Frame, ok bool) {
	if !lssFrame(f, MasterID) || f.Len == 0 || f.Len > 8 {
		s.count(false)
		return resp, false
	}
	cs := Command(f.Data[0])
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()

	switch {
	case cs == CmdSwitchGlobal:
		s.count(s.switchGlobal(f))
		return resp, false
	case cs >= CmdIdentifyVendor && cs <= CmdIdentifySerialHigh:
		return s.identify(f, cs)
	case cs == CmdIdentifyNonConfig:
		s.mu.Lock()
		nc := s.unconfigured()
		s.mu.Unlock()
		return frame(SlaveID, CmdNonConfigSlave), s.count(nc)
	case state == Waiting && cs >= CmdSelectVendor && cs <= CmdSelectSerial:
		return s.selective(f, cs)
	case state == Waiting && cs == CmdFastscan:
		return s.fastscan(f)
	case state == Configuration:
		return s.configure(f, cs)
	default:
		return resp, s.count(false)
	}
}

// count counts a processed frame: ok frames as received, others as
// received and ignored. It returns ok.
func (s *Slave) count(ok bool) bool {
	s.mu.Lock()
	s.stats.Received++
	if !ok {
		s.stats.Ignored++
	}
	s.mu.Unlock()
	return ok
}

// switchGlobal processes "switch state global".
func (s *Slave) switchGlobal(f *canbus.Frame) bool {
	if f.Len < 2 {
		return false
	}
	var to State
	switch State(f.Data[1]) {
	case Waiting:
		to = Waiting
	case Configuration:
		to = Configuration
	default:
		return false
	}
	s.mu.Lock()
	from := s.state
	changed := s.setState(to)
	s.selNext, s.idNext = 0, 0
	reset := from == Configuration && to == Waiting && s.active == Unconfigured && s.pending.NodeID != Unconfigured
	pending := s.pending.NodeID
	s.mu.Unlock()
	if changed {
		s.cfg.log.Debug("lss state", "state", to)
		if h := s.cfg.state; h != nil {
			h(to)
		}
	}
	if reset {
		s.cfg.log.Info("lss node-ID configured, reset communication", "node", pending)
		if h := s.cfg.reset; h != nil {
			h(pending)
		}
		s.mu.Lock()
		if s.active == Unconfigured {
			s.active = pending
		}
		s.mu.Unlock()
	}
	return true
}

// step advances a sequence of commands first..last (switch state
// selective, identify remote slave) and reports whether the sequence is
// complete. A repetition of the frame just accepted (a duplicate on the
// bus) is tolerated; any other deviation restarts the sequence.
func step(seq *Command, first, last, cs Command, match bool) (complete, ok bool) {
	switch {
	case cs == first:
		*seq = 0
		if match {
			*seq = first + 1
		}
		return false, match
	case *seq != 0 && cs == *seq && match:
		if cs == last {
			*seq = 0
			return true, true
		}
		*seq++
		return false, true
	case *seq != 0 && cs == *seq-1 && match:
		return false, true
	default:
		*seq = 0
		return false, false
	}
}

// selective processes "switch state selective" (waiting state). Like
// Lely, it requires the four frames in order (CANopenNode stores each
// value and compares at the serial number).
func (s *Slave) selective(f *canbus.Frame, cs Command) (canbus.Frame, bool) {
	if f.Len < 5 {
		return canbus.Frame{}, s.count(false)
	}
	v := u32(f)
	s.mu.Lock()
	complete, ok := step(&s.selNext, CmdSelectVendor, CmdSelectSerial, cs, v == s.addr.Part(Part(cs-CmdSelectVendor)))
	changed := complete && s.setState(Configuration)
	s.stats.Received++
	if !ok {
		s.stats.Ignored++
	}
	s.mu.Unlock()
	if changed {
		s.cfg.log.Debug("lss state", "state", Configuration, "by", "switch state selective")
		if h := s.cfg.state; h != nil {
			h(Configuration)
		}
	}
	return frame(SlaveID, CmdSelectResponse), complete
}

// identify processes "identify remote slave" (both states, as in Lely):
// vendor-ID and product code must be equal, revision and serial number
// within the inclusive bounds.
func (s *Slave) identify(f *canbus.Frame, cs Command) (canbus.Frame, bool) {
	if f.Len < 5 {
		return canbus.Frame{}, s.count(false)
	}
	v := u32(f)
	s.mu.Lock()
	var match bool
	switch cs {
	case CmdIdentifyVendor:
		match = v == s.addr.Vendor
	case CmdIdentifyProduct:
		match = v == s.addr.Product
	case CmdIdentifyRevisionLow:
		match = v <= s.addr.Revision
	case CmdIdentifyRevisionHigh:
		match = v >= s.addr.Revision
	case CmdIdentifySerialLow:
		match = v <= s.addr.Serial
	default: // CmdIdentifySerialHigh
		match = v >= s.addr.Serial
	}
	complete, ok := step(&s.idNext, CmdIdentifyVendor, CmdIdentifySerialHigh, cs, match)
	s.stats.Received++
	if !ok {
		s.stats.Ignored++
	}
	s.mu.Unlock()
	return frame(SlaveID, CmdIdentifySlave), complete
}

// fastscan processes a Fastscan request (waiting state, unconfigured
// devices only, as in CANopenNode).
func (s *Slave) fastscan(f *canbus.Frame) (canbus.Frame, bool) {
	if f.Len < 8 {
		return canbus.Frame{}, s.count(false)
	}
	id, bitCheck, sub, next := u32(f), f.Data[5], Part(f.Data[6]), Part(f.Data[7])
	if (bitCheck > 31 && bitCheck != fastscanConfirm) || sub > PartSerial || next > PartSerial {
		return canbus.Frame{}, s.count(false)
	}
	s.mu.Lock()
	ack, changed := false, false
	switch {
	case !s.unconfigured() && !s.cfg.fastscanAny:
	case bitCheck == fastscanConfirm:
		s.fsPos, ack = PartVendor, true
	case s.fsPos == sub:
		mask := ^uint32(0) << bitCheck
		if (s.addr.Part(sub)^id)&mask == 0 {
			ack, s.fsPos = true, next
			if bitCheck == 0 && next < sub {
				changed = s.setState(Configuration)
			}
		}
	default: // another position: not addressed
	}
	s.stats.Received++
	if !ack {
		s.stats.Ignored++
	}
	s.mu.Unlock()
	if changed {
		s.cfg.log.Debug("lss state", "state", Configuration, "by", "fastscan")
		if h := s.cfg.state; h != nil {
			h(Configuration)
		}
	}
	return frame(SlaveID, CmdIdentifySlave), ack
}

// refusal converts a handler's error to an error code and specific
// error.
func refusal(err error, def uint8) (code, spec uint8) {
	var ce *ConfigError
	if errors.As(err, &ce) && ce.Code != CodeOK {
		return ce.Code, ce.Spec
	}
	return def, 0
}

// configure processes the services of the configuration state.
func (s *Slave) configure(f *canbus.Frame, cs Command) (canbus.Frame, bool) {
	s.mu.Lock()
	addr, active, pending := s.addr, s.active, s.pending
	s.mu.Unlock()
	switch cs {
	case CmdConfigureNodeID:
		if f.Len < 2 {
			break
		}
		node := canopen.NodeID(f.Data[1])
		code, spec := CodeOK, uint8(0)
		if !validNodeID(node) {
			code = CodeOutOfRange
		} else if h := s.cfg.nodeID; h != nil {
			if err := h(node); err != nil {
				code, spec = refusal(err, CodeOutOfRange)
			}
		}
		s.mu.Lock()
		if code == CodeOK {
			s.pending.NodeID = node
			s.stats.NodeIDs++
		} else {
			s.stats.Refused++
		}
		s.stats.Received++
		s.mu.Unlock()
		s.cfg.log.Debug("lss configure node-ID", "node", node, "code", code)
		return frame(SlaveID, cs, code, spec), true
	case CmdConfigureBitTiming:
		if f.Len < 3 {
			break
		}
		sel, idx := f.Data[1], BitTiming(f.Data[2])
		code, spec := CodeOK, uint8(0)
		switch h := s.cfg.bitRate; {
		case h == nil, sel == 0 && !idx.Valid():
			code = CodeOutOfRange
		default:
			if err := h(sel, idx); err != nil {
				code, spec = refusal(err, CodeOutOfRange)
			}
		}
		s.mu.Lock()
		if code == CodeOK {
			s.pending.BitTimingSet, s.pending.Selector, s.pending.BitTiming = true, sel, idx
		} else {
			s.stats.Refused++
		}
		s.stats.Received++
		s.mu.Unlock()
		return frame(SlaveID, cs, code, spec), true
	case CmdActivateBitTiming:
		if f.Len < 3 {
			break
		}
		delay := time.Duration(binary.LittleEndian.Uint16(f.Data[1:3])) * time.Millisecond
		s.count(true)
		if h := s.cfg.activate; h != nil {
			h(pending, delay)
		}
		return canbus.Frame{}, false
	case CmdStore:
		code, spec := CodeOK, uint8(0)
		if h := s.cfg.store; h == nil {
			code = CodeOutOfRange
		} else if err := h(pending); err != nil {
			code, spec = refusal(err, CodeStoreFailed)
		}
		s.mu.Lock()
		if code == CodeOK {
			s.stats.Stores++
		} else {
			s.stats.Refused++
		}
		s.stats.Received++
		s.mu.Unlock()
		return frame(SlaveID, cs, code, spec), true
	case CmdInquireVendor, CmdInquireProduct, CmdInquireRevision, CmdInquireSerial:
		s.count(true)
		return frame32(SlaveID, cs, addr.Part(Part(cs-CmdInquireVendor))), true
	case CmdInquireNodeID:
		s.count(true)
		return frame(SlaveID, cs, byte(active)), true
	default: // not a service of the configuration state
	}
	return canbus.Frame{}, s.count(false)
}
