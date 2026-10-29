// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Sentinel errors of the master (see also canopen.ErrTimeout: no slave
// answered a confirmed service, canopen.ErrClosed: the master is not
// running).
var (
	// ErrNoNodeID reports that AssignAll ran out of node-IDs.
	ErrNoNodeID = errors.New("lss: no free node-ID")
	// ErrConflict reports more than one slave in the configuration state
	// where exactly one is required, or one that could not be made to
	// leave it.
	ErrConflict = errors.New("lss: slave selection conflict")
	// ErrFastscan reports a Fastscan that found candidates but could not
	// complete the identification of one (persistent frame loss).
	ErrFastscan = errors.New("lss: fastscan did not converge")
)

// Defaults of the master.
const (
	DefaultTimeout         = time.Second           // response timeout of confirmed services (CANopenNode's default)
	DefaultRetries         = 2                     // re-sends of a confirmed request without response
	DefaultFastscanTimeout = 25 * time.Millisecond // listening window of a Fastscan query
	DefaultFastscanRetries = 1                     // re-sends of an idempotent Fastscan query without answer
)

// MasterStats counts what a [Master] did.
type MasterStats struct {
	Sent        uint64 `json:"sent"`        // frames sent
	Responses   uint64 `json:"responses"`   // responses received
	Unexpected  uint64 `json:"unexpected"`  // slave frames that answered nothing asked (late, duplicated, other slaves)
	Timeouts    uint64 `json:"timeouts"`    // confirmed services without response after all retries
	Retries     uint64 `json:"retries"`     // re-sent requests
	Queries     uint64 `json:"queries"`     // Fastscan queries
	Restarts    uint64 `json:"restarts"`    // Fastscan identifications restarted after a failed verification
	Assigned    uint64 `json:"assigned"`    // node-IDs assigned by AssignAll
	Conflicts   uint64 `json:"conflicts"`   // selections rejected because the selected slave was not alone or not the expected one
	SendErrors  uint64 `json:"sendErrors"`  // frames that could not be sent
	Refusals    uint64 `json:"refusals"`    // configuration services refused by a slave
	Inquiries   uint64 `json:"inquiries"`   // inquire services answered
	Identifies  uint64 `json:"identifies"`  // identify services answered by at least one slave
	Selections  uint64 `json:"selections"`  // successful switch state selective
	Activations uint64 `json:"activations"` // activate bit timing requests sent
}

// MasterOption configures a [Master].
type MasterOption func(*masterConfig)

type masterConfig struct {
	log        *slog.Logger
	timeout    time.Duration
	retries    int
	inhibit    time.Duration
	fsTimeout  time.Duration
	fsRetries  int
	fsAttempts int
}

// WithLogger logs master events to l. The default logs nothing.
func WithLogger(l *slog.Logger) MasterOption { return func(c *masterConfig) { c.log = l } }

// WithTimeout sets how long a confirmed service waits for its response
// (default [DefaultTimeout]; python canopen uses 0.5 s).
func WithTimeout(d time.Duration) MasterOption {
	return func(c *masterConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithRetries sets how many times a confirmed request without response is
// sent again (default [DefaultRetries]; 0: once only). Every confirmed
// LSS service is idempotent while the slave stays in its state.
func WithRetries(n int) MasterOption { return func(c *masterConfig) { c.retries = max(n, 0) } }

// WithInhibit sets a minimum gap between two frames the master sends
// (default 0). python canopen waits 200 ms after each frame of switch
// state selective because "some devices need these delays"; Lely has an
// LSS inhibit time for the same purpose.
func WithInhibit(d time.Duration) MasterOption {
	return func(c *masterConfig) { c.inhibit = max(d, 0) }
}

// WithFastscanTimeout sets the default listening window of a Fastscan
// query and of the inquiries that check a selection (default
// [DefaultFastscanTimeout]). Silence is the "no" answer of Fastscan, so
// every query costs this window when no slave matches; it must exceed
// the slaves' response time (two frame times plus processing).
func WithFastscanTimeout(d time.Duration) MasterOption {
	return func(c *masterConfig) {
		if d > 0 {
			c.fsTimeout = d
		}
	}
}

// WithFastscanRetries sets the default number of times an unanswered,
// idempotent Fastscan query is sent again before the silence counts as
// "no" (default [DefaultFastscanRetries]; 0 behaves as CANopenNode and
// python canopen do).
func WithFastscanRetries(n int) MasterOption {
	return func(c *masterConfig) { c.fsRetries = max(n, 0) }
}

// Master is an LSS master (CiA 305). Its services run on the caller's
// goroutine and are serialised: one service at a time (CiA 305 allows one
// LSS master per network, and the LSS responses carry nothing that would
// tell concurrent requests apart). It keeps one subscription to 0x7E4
// between Start and Close.
type Master struct {
	bus canopen.Bus
	cfg masterConfig

	op sync.Mutex // serialises services

	mu       sync.Mutex
	state    int // 0 new, 1 started, 2 closed
	sub      *canbus.Subscription
	lastSend time.Time
	stats    MasterStats
}

// NewMaster returns an LSS master. Options: [WithLogger], [WithTimeout],
// [WithRetries], [WithInhibit], [WithFastscanTimeout],
// [WithFastscanRetries].
func NewMaster(bus canopen.Bus, opts ...MasterOption) *Master {
	m := &Master{bus: bus, cfg: masterConfig{
		log: discard, timeout: DefaultTimeout, retries: DefaultRetries,
		fsTimeout: DefaultFastscanTimeout, fsRetries: DefaultFastscanRetries, fsAttempts: 3,
	}}
	for _, o := range opts {
		if o != nil {
			o(&m.cfg)
		}
	}
	if m.cfg.log == nil {
		m.cfg.log = discard
	}
	return m
}

// Start subscribes to the LSS slave identifier (before it returns). The
// master has no goroutine.
func (m *Master) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state {
	case 1:
		return fmt.Errorf("%w: already started", canopen.ErrBusy)
	case 2:
		return canopen.ErrClosed
	}
	flt := canbus.ExactStandard(SlaveID)
	flt.Remote = canbus.RemoteDataOnly
	sub, err := m.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{flt}}))
	if err != nil {
		return fmt.Errorf("lss: subscribe: %w", err)
	}
	m.sub, m.state = sub, 1
	return nil
}

// Close ends the subscription; a running service fails with
// canopen.ErrClosed. It is idempotent.
func (m *Master) Close() error {
	m.mu.Lock()
	sub := m.sub
	m.state, m.sub = 2, nil
	m.mu.Unlock()
	if sub != nil {
		_ = sub.Close()
	}
	return nil
}

// Stats returns the counters.
func (m *Master) Stats() MasterStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

func (m *Master) count(f func(*MasterStats)) {
	m.mu.Lock()
	f(&m.stats)
	m.mu.Unlock()
}

// session is one service in progress: the master's lock is held.
type session struct {
	m   *Master
	ctx context.Context
	sub *canbus.Subscription
}

// begin serialises a service and returns its session; end must be
// called.
func (m *Master) begin(ctx context.Context) (*session, error) {
	m.op.Lock()
	m.mu.Lock()
	sub, st := m.sub, m.state
	m.mu.Unlock()
	if st != 1 {
		m.op.Unlock()
		if st == 0 {
			return nil, fmt.Errorf("%w: lss master not started", canopen.ErrClosed)
		}
		return nil, canopen.ErrClosed
	}
	return &session{m: m, ctx: ctx, sub: sub}, nil
}

func (s *session) end() { s.m.op.Unlock() }

// send sends frames, honouring the inhibit time.
func (s *session) send(fs ...canbus.Frame) error {
	for i := range fs {
		if d := s.m.cfg.inhibit; d > 0 {
			s.m.mu.Lock()
			wait := time.Until(s.m.lastSend.Add(d))
			s.m.mu.Unlock()
			if wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-s.ctx.Done():
					t.Stop()
					return s.ctx.Err()
				case <-t.C:
				}
			}
		}
		err := s.m.bus.Send(s.ctx, &fs[i])
		s.m.mu.Lock()
		s.m.lastSend = time.Now()
		if err != nil {
			s.m.stats.SendErrors++
		} else {
			s.m.stats.Sent++
		}
		s.m.mu.Unlock()
		if err != nil {
			return fmt.Errorf("lss: send %s: %w", Command(fs[i].Data[0]), err)
		}
	}
	return nil
}

// drain discards the frames received so far (late or duplicated answers
// to earlier requests).
func (s *session) drain() {
	for {
		select {
		case <-s.sub.Frames():
			s.m.count(func(st *MasterStats) { st.Unexpected++ })
		default:
			return
		}
	}
}

// listen waits up to window for frames matching want. After the first
// match it goes on collecting for settle (within the window) and returns:
// settle 0 returns at the first match, settle >= window collects until the
// window ends. Frames that do not match are counted as unexpected.
func (s *session) listen(window time.Duration, want Command, settle time.Duration) ([]canbus.Frame, error) {
	end := time.Now().Add(window)
	t := time.NewTimer(window)
	defer t.Stop()
	var out []canbus.Frame
	for {
		select {
		case <-s.ctx.Done():
			return out, s.ctx.Err()
		case <-s.sub.Done():
			return out, canopen.ErrClosed
		case <-t.C:
			return out, nil
		case f := <-s.sub.Frames():
			if !matchResponse(&f, want) {
				s.m.count(func(st *MasterStats) { st.Unexpected++ })
				continue
			}
			s.m.count(func(st *MasterStats) { st.Responses++ })
			out = append(out, f)
			if len(out) > 1 {
				continue
			}
			if settle <= 0 {
				return out, nil
			}
			if until := time.Now().Add(settle); until.Before(end) {
				t.Reset(time.Until(until)) // Go 1.23 timers: no stale expiry after Reset
			}
		}
	}
}

// request sends req (one or more frames) and waits for the first response
// want, re-sending up to the configured retries.
func (s *session) request(want Command, req ...canbus.Frame) (canbus.Frame, error) {
	for attempt := 0; ; attempt++ {
		s.drain()
		if err := s.send(req...); err != nil {
			return canbus.Frame{}, err
		}
		got, err := s.listen(s.m.cfg.timeout, want, 0)
		if err != nil {
			return canbus.Frame{}, err
		}
		if len(got) > 0 {
			return got[0], nil
		}
		if attempt >= s.m.cfg.retries {
			s.m.count(func(st *MasterStats) { st.Timeouts++ })
			return canbus.Frame{}, fmt.Errorf("lss: %s: %w: no response after %d attempt(s)", Command(req[len(req)-1].Data[0]), canopen.ErrTimeout, attempt+1)
		}
		s.m.count(func(st *MasterStats) { st.Retries++ })
	}
}

// matchResponse reports whether f is a slave response with command want
// and the length it needs.
func matchResponse(f *canbus.Frame, want Command) bool {
	if !lssFrame(f, SlaveID) || f.Len == 0 || f.Len > 8 || Command(f.Data[0]) != want {
		return false
	}
	need := uint8(1)
	switch want {
	case CmdConfigureNodeID, CmdConfigureBitTiming, CmdStore, CmdInquireNodeID:
		need = 2
	case CmdInquireVendor, CmdInquireProduct, CmdInquireRevision, CmdInquireSerial:
		need = 5
	default: // the command byte is the whole answer
	}
	return f.Len >= need
}

// configResult decodes the response of a configuration service.
func configResult(f *canbus.Frame) error {
	if f.Data[1] == CodeOK {
		return nil
	}
	spec := uint8(0)
	if f.Len >= 3 {
		spec = f.Data[2]
	}
	return &ConfigError{Command: Command(f.Data[0]), Code: f.Data[1], Spec: spec}
}

// SwitchStateGlobal switches every slave to st (unconfirmed). Switching
// to Waiting makes a slave whose node-ID was unconfigured and has just
// been configured apply it (NMT reset communication).
func (m *Master) SwitchStateGlobal(ctx context.Context, st State) error {
	if st != Waiting && st != Configuration {
		return fmt.Errorf("lss: switch state global: invalid state %d", st)
	}
	s, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end()
	return s.send(frame(MasterID, CmdSwitchGlobal, byte(st)))
}

// SwitchStateSelective switches the slave with address a to the
// configuration state; it fails with canopen.ErrTimeout when no slave
// answers. Slaves already in the configuration state stay there.
func (m *Master) SwitchStateSelective(ctx context.Context, a Address) error {
	s, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end()
	if _, err := s.request(CmdSelectResponse, selectiveFrames(a)...); err != nil {
		return err
	}
	m.count(func(st *MasterStats) { st.Selections++ })
	return nil
}

func selectiveFrames(a Address) []canbus.Frame {
	return []canbus.Frame{
		frame32(MasterID, CmdSelectVendor, a.Vendor),
		frame32(MasterID, CmdSelectProduct, a.Product),
		frame32(MasterID, CmdSelectRevision, a.Revision),
		frame32(MasterID, CmdSelectSerial, a.Serial),
	}
}

// configure runs a confirmed configuration service.
func (m *Master) configure(ctx context.Context, req canbus.Frame) error {
	s, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end()
	f, err := s.request(Command(req.Data[0]), req)
	if err != nil {
		return err
	}
	if err := configResult(&f); err != nil {
		m.count(func(st *MasterStats) { st.Refusals++ })
		return err
	}
	return nil
}

// SetNodeID configures the node-ID of the slave in the configuration
// state (1..127, or Unconfigured to remove it). A refusal is a
// [*ConfigError]. The node-ID is pending until the device applies it: an
// unconfigured device when it leaves the configuration state, a
// configured one at its next NMT reset communication.
func (m *Master) SetNodeID(ctx context.Context, node canopen.NodeID) error {
	if !validNodeID(node) {
		return fmt.Errorf("lss: %w: %d", canopen.ErrInvalidNodeID, node)
	}
	return m.configure(ctx, frame(MasterID, CmdConfigureNodeID, byte(node)))
}

// SetBitTiming configures the bit timing of the slave in the
// configuration state from the CiA 301 table (selector 0).
func (m *Master) SetBitTiming(ctx context.Context, b BitTiming) error {
	return m.SetBitTimingTable(ctx, 0, uint8(b))
}

// SetBitTimingTable configures the bit timing of the slave in the
// configuration state with an explicit table selector and index. CiA 305
// selector 0 is the CiA 301 table; the meaning of other selectors is not
// established by the public sources used here (CANopenNode refuses them,
// Lely too), so they are passed to the slave unchanged.
func (m *Master) SetBitTimingTable(ctx context.Context, selector, index uint8) error {
	return m.configure(ctx, frame(MasterID, CmdConfigureBitTiming, selector, index))
}

// ActivateBitTiming tells every slave in the configuration state to
// switch to its configured bit timing after delay (0..65535 ms; the
// slaves stay silent for another delay after the switch). It is
// unconfirmed. The master's own interface must be switched by the caller.
func (m *Master) ActivateBitTiming(ctx context.Context, delay time.Duration) error {
	ms, err := delayMs(delay)
	if err != nil {
		return err
	}
	s, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end()
	if err := s.send(frame(MasterID, CmdActivateBitTiming, byte(ms), byte(ms>>8))); err != nil {
		return err
	}
	m.count(func(st *MasterStats) { st.Activations++ })
	return nil
}

// Store makes the slave in the configuration state store its pending
// node-ID and bit timing. A refusal is a [*ConfigError] (CodeOutOfRange:
// not supported, CodeStoreFailed: storage error).
func (m *Master) Store(ctx context.Context) error {
	return m.configure(ctx, frame(MasterID, CmdStore))
}

// Inquire reads one part of the LSS address of the slave in the
// configuration state.
func (m *Master) Inquire(ctx context.Context, p Part) (uint32, error) {
	if p > PartSerial {
		return 0, fmt.Errorf("lss: inquire: invalid part %d", p)
	}
	s, err := m.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer s.end()
	cs := CmdInquireVendor + Command(p)
	f, err := s.request(cs, frame(MasterID, cs))
	if err != nil {
		return 0, err
	}
	m.count(func(st *MasterStats) { st.Inquiries++ })
	return u32(&f), nil
}

// InquireAddress reads the whole LSS address of the slave in the
// configuration state (four inquiries).
func (m *Master) InquireAddress(ctx context.Context) (Address, error) {
	var a Address
	for p := PartVendor; p <= PartSerial; p++ {
		v, err := m.Inquire(ctx, p)
		if err != nil {
			return Address{}, err
		}
		a.SetPart(p, v)
	}
	return a, nil
}

// InquireNodeID reads the active node-ID of the slave in the
// configuration state (Unconfigured when it has none). CANopenNode answers
// the active node-ID; Lely answers the pending one while the device is
// in NMT initialisation.
func (m *Master) InquireNodeID(ctx context.Context) (canopen.NodeID, error) {
	s, err := m.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer s.end()
	f, err := s.request(CmdInquireNodeID, frame(MasterID, CmdInquireNodeID))
	if err != nil {
		return 0, err
	}
	m.count(func(st *MasterStats) { st.Inquiries++ })
	return canopen.NodeID(f.Data[1]), nil
}

// Identify reports whether at least one slave has an LSS address in the
// range lo..hi ("identify remote slave"): vendor-ID and product code must
// be equal in lo and hi, revision and serial number are inclusive
// ranges. Slaves answer in either state. No answer within the timeout
// (after the retries) is false.
func (m *Master) Identify(ctx context.Context, lo, hi Address) (bool, error) {
	if lo.Vendor != hi.Vendor || lo.Product != hi.Product || lo.Revision > hi.Revision || lo.Serial > hi.Serial {
		return false, fmt.Errorf("lss: identify: invalid range %v..%v", lo, hi)
	}
	return m.identify(ctx, CmdIdentifySlave,
		frame32(MasterID, CmdIdentifyVendor, lo.Vendor),
		frame32(MasterID, CmdIdentifyProduct, lo.Product),
		frame32(MasterID, CmdIdentifyRevisionLow, lo.Revision),
		frame32(MasterID, CmdIdentifyRevisionHigh, hi.Revision),
		frame32(MasterID, CmdIdentifySerialLow, lo.Serial),
		frame32(MasterID, CmdIdentifySerialHigh, hi.Serial))
}

// IdentifyNonConfigured reports whether at least one slave has neither an
// active nor a pending node-ID ("identify non-configured remote slave").
func (m *Master) IdentifyNonConfigured(ctx context.Context) (bool, error) {
	return m.identify(ctx, CmdNonConfigSlave, frame(MasterID, CmdIdentifyNonConfig))
}

func (m *Master) identify(ctx context.Context, want Command, req ...canbus.Frame) (bool, error) {
	s, err := m.begin(ctx)
	if err != nil {
		return false, err
	}
	defer s.end()
	_, err = s.request(want, req...)
	if errors.Is(err, canopen.ErrTimeout) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m.count(func(st *MasterStats) { st.Identifies++ })
	return true, nil
}

// Configure runs fn with the slave of address a selected, and that slave
// alone: it switches every slave to waiting and checks that none stayed
// in the configuration state (switch state global is unconfirmed: a lost
// frame would leave a slave selected, which then took the next
// configuration too), selects a with switch state selective, checks by
// inquiring the whole address that a answers alone, runs fn (which calls
// SetNodeID, SetBitTiming, Store, ...) and switches every slave back to
// waiting with the same check, also when fn fails. A selection that is
// not confirmed fails with [ErrConflict] before fn runs; the checks take
// a few Fastscan listening windows ([WithFastscanTimeout]).
func (m *Master) Configure(ctx context.Context, a Address, fn func(ctx context.Context) error) error {
	if err := m.leaveConfiguration(ctx, FastscanOptions{}); err != nil {
		return err
	}
	err := m.SwitchStateSelective(ctx, a)
	if err == nil {
		var alone bool
		if alone, err = m.checkSelected(ctx, FastscanOptions{}, a); err == nil && !alone {
			m.count(func(st *MasterStats) { st.Conflicts++ })
			err = fmt.Errorf("%w: selection of %v not confirmed", ErrConflict, a)
		}
	}
	if err == nil {
		err = fn(ctx)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.leaveTimeout())
	defer cancel()
	if werr := m.leaveConfiguration(wctx, FastscanOptions{}); err == nil {
		err = werr
	}
	return err
}

// leaveTimeout bounds the final leaveConfiguration of Configure, which
// runs after the caller's context may have ended: every round of it
// listens one Fastscan window.
func (m *Master) leaveTimeout() time.Duration {
	rounds := 2 * (m.cfg.retries + m.cfg.fsAttempts)
	return m.cfg.timeout + time.Duration(rounds+1)*(m.cfg.fsTimeout+m.cfg.inhibit)
}

// ConfigureNodeID gives the slave of address a the node-ID node:
// selection, configure node-ID, back to waiting (see [Master.Configure]).
func (m *Master) ConfigureNodeID(ctx context.Context, a Address, node canopen.NodeID) error {
	return m.Configure(ctx, a, func(ctx context.Context) error { return m.SetNodeID(ctx, node) })
}
