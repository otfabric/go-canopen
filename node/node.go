// SPDX-License-Identifier: MIT

package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	syncobj "github.com/otfabric/go-canopen/sync"
	"github.com/otfabric/go-canopen/timestamp"
	"github.com/otfabric/go-canopen/usdo"
)

// IndexErrorBehaviour is the error behaviour object (CiA 301 7.5.2.31):
// sub-index 1 selects the reaction to a communication error (heartbeat or
// life guarding loss): 0 enter Pre-operational (only from Operational),
// 1 no state change, 2 enter Stopped.
const IndexErrorBehaviour uint16 = 0x1029

// Error behaviour values of 0x1029 sub-index 1.
const (
	ErrorBehaviourPreOperational uint8 = 0
	ErrorBehaviourNoChange       uint8 = 1
	ErrorBehaviourStopped        uint8 = 2
)

// opTimeout bounds the emergency messages the node sends on its own.
const opTimeout = time.Second

// ErrNoObject reports a service the dictionary does not configure (for
// example [Node.SendTime] without 0x1012).
var ErrNoObject = errors.New("node: the dictionary lacks the object of this service")

// Node is a CANopen device built from an object dictionary: the NMT slave
// with its state machine and resets, the SDO server, heartbeat producer
// and consumer, node guarding, EMCY, SYNC, TIME and the PDOs of the
// dictionary, enabled and disabled as the NMT state requires. See the
// package documentation.
type Node struct {
	bus canopen.Bus
	d   *od.Dictionary
	cfg config
	log *slog.Logger

	slave  *nmt.Slave
	hbProd *nmt.HeartbeatProducer
	hbCons *nmt.HeartbeatConsumer
	guard  *nmt.GuardResponder
	emcy   *emcy.Producer
	syncC  *syncobj.Consumer
	syncP  *syncobj.Producer
	timeC  *timestamp.Consumer
	timeP  *timestamp.Producer
	tpdos  []*pdo.TPDO
	rpdos  []*pdo.RPDO
	drive  *cia402.Drive
	// program serves the program download objects of WithProgram.
	program *program.Device
	lssS    *lss.Slave    // WithLSS, or nil
	usdoS   *usdo.Server  // WithFD, or nil
	mp      mpdoServices  // the MPDO producers and consumers
	lssSet  *lss.Settings // LSS settings found in the store at New

	lcMu    sync.Mutex // serialises Start and Close
	started bool
	closed  atomic.Bool

	mu   sync.Mutex // guards srv, stop and deferred
	srv  *sdo.Server
	stop context.CancelFunc
	wg   sync.WaitGroup
	// deferred is set while the services wait for a node-ID (started
	// unconfigured): the reset that applies one starts them.
	deferred bool

	errMu      sync.Mutex
	lost       map[canopen.NodeID]bool // heartbeat (node-ID) and life guarding (0) losses
	pdoErr     map[uint16]map[int]bool // active PDO error codes and the PDOs reporting them
	syncLenErr bool
	driveErr   uint16

	resets     atomic.Uint64
	suppressed atomic.Uint64

	storeMu sync.Mutex // serialises parameter saves and restores
	unhook  []func()   // removes the dictionary hooks the node registered
}

// New returns a node for the dictionary d on bus. The node-ID is the
// dictionary's ([od.Dictionary.NodeID], 1..127). The services are chosen
// from the objects d has:
//
//   - always: NMT slave, SDO server (0x1200, or the default COB-IDs), EMCY
//     producer (0x1014/0x1015 and 0x1001/0x1003 when present)
//   - 0x1017: heartbeat producer; 0x1016: heartbeat consumer
//   - 0x100C and 0x100D: node guarding responder (life guarding)
//   - 0x1005: SYNC consumer; with 0x1006 and 0x1019 also the SYNC
//     producer (producing while bit 30 is set and the period is not 0)
//   - 0x1012: TIME consumer and producer (following bits 31 and 30)
//   - every TPDO and RPDO whose communication and mapping objects exist
//
// The node does nothing on the bus until [Node.Start].
func New(bus canopen.Bus, d *od.Dictionary, opts ...Option) (*Node, error) {
	if bus == nil || d == nil {
		return nil, errors.New("node: New needs a bus and a dictionary")
	}
	n := &Node{
		bus: bus, d: d, cfg: newConfig(opts),
		lost: map[canopen.NodeID]bool{}, pdoErr: map[uint16]map[int]bool{},
	}
	if err := n.loadLSS(); err != nil {
		return nil, err
	}
	id := d.NodeID()
	if err := id.Check(); err != nil && (id != lss.Unconfigured || !n.cfg.lssOn) {
		return nil, fmt.Errorf("node: dictionary node-ID %d: %w", id, err)
	}
	n.log = n.cfg.log.With("node", int(id))
	if err := n.loadStored(0); err != nil {
		return nil, err
	}
	if err := n.build(id); err != nil {
		n.closeServices()
		return nil, err
	}
	n.installStore()
	if err := n.installProgram(); err != nil {
		n.closeServices()
		return nil, err
	}
	if err := n.installMPDO(); err != nil {
		n.closeServices()
		return nil, err
	}
	return n, nil
}

// gated returns a bus that drops frames unless the node is
// pre-operational or operational.
func (n *Node) gated(sent func(*canbus.Frame)) canopen.Bus {
	return &gatedBus{Bus: n.bus, allow: n.mayProduce, dropped: func() { n.suppressed.Add(1) }, sent: sent}
}

// mayProduce reports whether EMCY, SYNC and TIME may be sent.
func (n *Node) mayProduce() bool {
	st := n.slave.State()
	return st == canopen.StatePreOperational || st == canopen.StateOperational
}

func (n *Node) has(idx ...uint16) bool {
	for _, i := range idx {
		if n.d.Object(i) == nil {
			return false
		}
	}
	return true
}

// build creates the services. An unconfigured node (LSS) builds them
// with node-ID 1 in place of its own: they stay silent (not started, then
// gated by the NMT state) until a reset gives them the real one.
func (n *Node) build(id canopen.NodeID) error {
	var err error
	lg := n.log
	nopts := []nmt.Option{nmt.WithLogger(lg), nmt.WithDictionary(n.d)}
	if n.cfg.fd {
		nopts = append(nopts, nmt.WithFD(n.cfg.brs))
	}
	n.slave, err = nmt.NewSlave(n.bus, id, append(nopts, nmt.WithStateHandler(n.onState), nmt.WithResetHandler(n.onReset))...)
	if err != nil {
		return err
	}
	if err := n.buildLSS(id); err != nil {
		return err
	}
	if id == lss.Unconfigured {
		id = 1
	}
	if n.cfg.drive != nil {
		dc := *n.cfg.drive
		user := dc.OnError
		dc.OnError = func(code uint16) {
			n.onDriveError(code)
			if user != nil {
				user(code)
			}
		}
		if n.drive, err = cia402.NewDrive(n.d, dc); err != nil {
			return fmt.Errorf("node: drive: %w", err)
		}
	}
	eopts := []emcy.Option{emcy.WithLogger(lg)}
	if n.has(od.IndexCOBIDEmcy) {
		eopts = append(eopts, emcy.WithDictionary(n.d))
	}
	if n.cfg.fd {
		eopts = append(eopts, emcy.WithFD(n.cfg.brs), emcy.WithFDInfo(0, 301), emcy.WithFDClock(time.Now))
	}
	eopts = append(eopts, n.cfg.emcyOpts...)
	if n.emcy, err = emcy.NewProducer(n.gated(nil), id, eopts...); err != nil {
		return err
	}
	if n.has(od.IndexProducerHeartbeat) {
		if n.hbProd, err = nmt.NewHeartbeatProducer(n.bus, id, n.slave.State, nopts...); err != nil {
			return err
		}
	}
	if n.has(od.IndexConsumerHeartbeat) && !n.cfg.noHBCons {
		if n.hbCons, err = nmt.NewHeartbeatConsumer(n.bus, append(nopts, nmt.WithHeartbeatHandler(n.onHeartbeat))...); err != nil {
			return err
		}
	}
	if n.has(od.IndexGuardTime, od.IndexLifeTimeFactor) && !n.cfg.fd {
		if n.guard, err = nmt.NewGuardResponder(n.bus, id, n.slave.State, nmt.WithLogger(lg), nmt.WithDictionary(n.d), nmt.WithGuardHandler(n.onGuard)); err != nil {
			return err
		}
	}
	sopts := []syncobj.Option{syncobj.WithLogger(lg), syncobj.WithDictionary(n.d)}
	if n.cfg.fd {
		sopts = append(sopts, syncobj.WithFD(n.cfg.brs))
	}
	if n.has(od.IndexCOBIDSync) {
		if n.syncC, err = syncobj.NewConsumer(n.bus, append(sopts, syncobj.WithHandler(n.onSync))...); err != nil {
			return err
		}
	}
	if n.has(od.IndexCOBIDSync, od.IndexCyclePeriod, od.IndexSyncOverflow) {
		if n.syncP, err = syncobj.NewProducer(n.gated(n.ownSync), sopts...); err != nil {
			return err
		}
	}
	if n.has(od.IndexCOBIDTime) {
		if n.timeC, err = timestamp.NewConsumer(n.bus, timestamp.WithLogger(lg), timestamp.WithDictionary(n.d), timestamp.WithHandler(n.onTime)); err != nil {
			return err
		}
		if n.timeP, err = timestamp.NewProducer(n.gated(nil), timestamp.WithLogger(lg), timestamp.WithDictionary(n.d), timestamp.WithPeriod(n.cfg.timePer)); err != nil {
			return err
		}
	}
	popts := []pdo.Option{pdo.WithLogger(lg), pdo.WithErrorHandler(n.onPDOError)}
	if n.cfg.fd {
		popts = append(popts, pdo.WithFD(n.cfg.brs))
		uopts := append([]usdo.Option{usdo.WithLogger(lg), usdo.WithBRS(n.cfg.brs)}, n.cfg.usdoOpts...)
		if n.usdoS, err = usdo.NewServer(n.bus, n.d, uopts...); err != nil {
			return err
		}
	}
	for i := 1; i <= od.MaxPDOs; i++ {
		if n.has(pdo.CommIndex(false, i), pdo.MappingIndex(false, i)) {
			r, err := pdo.NewRPDO(n.bus, n.d, i, popts...)
			if err != nil {
				return err
			}
			n.rpdos = append(n.rpdos, r)
		}
		if n.has(pdo.CommIndex(true, i), pdo.MappingIndex(true, i)) {
			t, err := pdo.NewTPDO(n.bus, n.d, i, popts...)
			if err != nil {
				return err
			}
			n.tpdos = append(n.tpdos, t)
		}
	}
	return nil
}

// newServer returns a started SDO server on the COB-IDs of the
// dictionary. It is enabled at once: it is created just before the
// boot-up message (at Start and at a reset), and the boot-up message
// announces Pre-operational, where the server answers. A master that
// reacts to the boot-up message at once (a boot procedure) must not find
// the server still disabled; the state handler keeps it enabled in
// Pre-operational.
func (n *Node) newServer() (*sdo.Server, error) {
	opts := append([]sdo.ServerOption{sdo.WithLogger(n.log)}, n.cfg.sdoOpts...)
	srv, err := sdo.NewServer(n.bus, n.d, opts...)
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	return srv, nil
}

type starter interface{ Start() error }

// Start starts the services, sends the boot-up message and enters
// Pre-operational. It fails when a service cannot start (the node is then
// closed) and after Close.
func (n *Node) Start() error {
	n.lcMu.Lock()
	defer n.lcMu.Unlock()
	switch {
	case n.closed.Load():
		return canopen.ErrClosed
	case n.started:
		return fmt.Errorf("node: %w: already started", canopen.ErrBusy)
	}
	n.started = true
	if err := n.startServices(); err != nil {
		n.closed.Store(true)
		n.shutdown()
		return err
	}
	return nil
}

// startServices starts the services, then the NMT slave (boot-up) and
// the LSS slave. An unconfigured node starts the NMT and LSS slaves only:
// the reset that gives it a node-ID starts the others.
func (n *Node) startServices() error {
	if n.slave.NodeID() == lss.Unconfigured {
		n.mu.Lock()
		n.deferred = true
		n.mu.Unlock()
		n.log.Info("node unconfigured: waiting for a node-ID (LSS)")
	} else {
		if !n.cfg.fd {
			srv, err := n.newServer()
			if err != nil {
				return err
			}
			n.mu.Lock()
			n.srv = srv
			n.mu.Unlock()
		}
		if err := n.startProtocol(); err != nil {
			return err
		}
	}
	if err := n.slave.Start(); err != nil {
		return err
	}
	if n.lssS != nil {
		return n.lssS.Start()
	}
	return nil
}

// startProtocol starts every service but the NMT, LSS and SDO servers.
func (n *Node) startProtocol() error {
	var svcs []starter
	if n.usdoS != nil {
		svcs = append(svcs, n.usdoS)
	}
	svcs = append(svcs, n.emcy)
	if n.hbCons != nil {
		svcs = append(svcs, n.hbCons)
	}
	if n.guard != nil {
		svcs = append(svcs, n.guard)
	}
	if n.syncC != nil {
		svcs = append(svcs, n.syncC)
	}
	if n.syncP != nil {
		svcs = append(svcs, n.syncP)
	}
	if n.timeC != nil {
		svcs = append(svcs, n.timeC, n.timeP)
	}
	for _, r := range n.rpdos {
		svcs = append(svcs, r)
	}
	for _, t := range n.tpdos {
		svcs = append(svcs, t)
	}
	if n.hbProd != nil {
		svcs = append(svcs, n.hbProd)
	}
	for _, s := range svcs {
		if err := s.Start(); err != nil {
			return err
		}
	}
	if err := n.mp.start(); err != nil {
		return err
	}
	if n.drive != nil && n.cfg.drivePer > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		n.mu.Lock()
		n.stop = cancel
		n.mu.Unlock()
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			_ = n.drive.Run(ctx, n.cfg.drivePer)
		}()
	}
	return nil
}

// Close stops the node and its services and waits for them. The node
// sends nothing more (in particular no state change is announced).
func (n *Node) Close() error {
	n.lcMu.Lock()
	if n.closed.Load() {
		n.lcMu.Unlock()
		return nil
	}
	n.closed.Store(true)
	n.lcMu.Unlock()
	n.shutdown()
	return nil
}

func (n *Node) shutdown() {
	if n.lssS != nil {
		_ = n.lssS.Close()
	}
	_ = n.slave.Close()
	n.mu.Lock()
	stop, srv := n.stop, n.srv
	n.srv = nil
	n.mu.Unlock()
	if stop != nil {
		stop()
	}
	n.wg.Wait()
	if srv != nil {
		_ = srv.Close()
	}
	n.closeServices()
}

// removeHooks removes the dictionary hooks the node registered.
func (n *Node) removeHooks() {
	for _, f := range n.unhook {
		f()
	}
	n.unhook = nil
}

// closeServices closes every service but the slave and the SDO server,
// and removes the node's dictionary hooks.
func (n *Node) closeServices() {
	n.removeHooks()
	n.mp.close()
	if n.lssS != nil {
		_ = n.lssS.Close()
	}
	if n.usdoS != nil {
		_ = n.usdoS.Close()
	}
	for _, t := range n.tpdos {
		_ = t.Close()
	}
	for _, r := range n.rpdos {
		_ = r.Close()
	}
	if n.hbProd != nil {
		_ = n.hbProd.Close()
	}
	if n.hbCons != nil {
		_ = n.hbCons.Close()
	}
	if n.guard != nil {
		_ = n.guard.Close()
	}
	if n.syncP != nil {
		_ = n.syncP.Close()
	}
	if n.syncC != nil {
		_ = n.syncC.Close()
	}
	if n.timeP != nil {
		_ = n.timeP.Close()
	}
	if n.timeC != nil {
		_ = n.timeC.Close()
	}
	if n.emcy != nil {
		_ = n.emcy.Close()
	}
	if n.drive != nil {
		n.drive.Close()
	}
}

// onState enables and disables the services for a new NMT state (CiA
// 301 table 79): SDO in Pre-operational and Operational, PDOs in
// Operational only.
func (n *Node) onState(sc nmt.StateChange) {
	sdoOn := sc.To == canopen.StatePreOperational || sc.To == canopen.StateOperational
	pdoOn := sc.To == canopen.StateOperational
	n.mu.Lock()
	srv := n.srv
	n.mu.Unlock()
	if srv != nil {
		srv.SetEnabled(sdoOn)
	}
	if n.usdoS != nil {
		n.usdoS.SetEnabled(sdoOn)
	}
	for _, r := range n.rpdos {
		r.SetEnabled(pdoOn)
	}
	for _, t := range n.tpdos {
		t.SetEnabled(pdoOn)
	}
	n.mp.setEnabled(pdoOn)
	if !pdoOn {
		// Disabled RPDOs forget their errors: clear their emergencies.
		n.clearPDOErrors()
	}
	bootUp := sc.From == canopen.StateInitialising && sc.To == canopen.StatePreOperational
	if bootUp && n.drive != nil {
		if code := n.drive.ErrorCode(); code != 0 && isFault(n.drive.State()) {
			n.onDriveError(code)
		}
	}
	if h := n.cfg.onState; h != nil {
		h(sc)
	}
	if bootUp && n.cfg.autoStart {
		_ = n.slave.SetState(canopen.StateOperational)
	}
}

func isFault(s cia402.State) bool { return s == cia402.Fault || s == cia402.FaultReactionActive }

// onReset runs between the restore of the dictionary and the new boot-up
// message of an NMT reset: the services take the restored configuration
// and a new node-ID.
func (n *Node) onReset(kind nmt.ResetKind) {
	n.resets.Add(1)
	id := n.slave.NodeID()
	n.log.Info("node reset", "kind", kind.String(), "nodeId", int(id))
	// The dictionary holds the defaults of the reset areas: the saved
	// parameters of those areas replace them before the services read
	// their configuration.
	if err := n.loadStored(kind); err != nil {
		n.log.Warn("node reset: stored parameters", "error", err)
	}
	configured := id != lss.Unconfigured
	if configured {
		if n.hbProd != nil {
			_ = n.hbProd.SetNodeID(id)
		}
		if n.guard != nil {
			_ = n.guard.SetNodeID(id)
			n.guard.ResetToggle()
		}
		if !n.has(od.IndexCOBIDEmcy) {
			n.emcy.SetCOBID(canopen.NewCOBID(canopen.FuncEMCY + canbus.ID(id)))
		} else if x, err := n.d.GetUint(od.IndexCOBIDEmcy, 0); err == nil {
			// Services started now have not watched the restore.
			n.emcy.SetCOBID(canopen.COBID(x))
		}
		if n.usdoS != nil {
			_ = n.usdoS.SetNodeID(id)
		}
	}
	if n.lssS != nil {
		_ = n.lssS.SetNodeID(id)
	}
	if n.hbCons != nil {
		// The supervision restarts: each node waits for its first
		// heartbeat again.
		n.hbCons.Restart()
	}
	// The error state restarts: the emergencies of the reset (sent while
	// Initialising) are suppressed by the gate.
	n.errMu.Lock()
	clear(n.lost)
	clear(n.pdoErr)
	n.syncLenErr = false
	n.driveErr = 0
	n.errMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	_ = n.emcy.ClearAll(ctx)
	cancel()
	n.emcy.ClearHistory()
	// The SDO server reads its COB-IDs at start: replace it.
	if n.closed.Load() {
		return
	}
	n.mu.Lock()
	old := n.srv
	n.srv = nil
	n.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if !configured {
		return
	}
	if !n.cfg.fd {
		srv, err := n.newServer()
		if err != nil {
			n.log.Warn("node reset: SDO server", "error", err)
			return
		}
		n.mu.Lock()
		if n.closed.Load() {
			// Closed meanwhile: shutdown has already run.
			n.mu.Unlock()
			_ = srv.Close()
			return
		}
		n.srv = srv
		n.mu.Unlock()
	}
	n.mu.Lock()
	start := n.deferred && !n.closed.Load()
	n.deferred = false
	n.mu.Unlock()
	if start {
		n.log.Info("node configured: starting the services", "nodeId", int(id))
		if err := n.startProtocol(); err != nil {
			n.log.Warn("node reset: services", "error", err)
		}
	}
}

// onHeartbeat reacts to the heartbeat consumer's events.
func (n *Node) onHeartbeat(ev nmt.HeartbeatEvent) {
	switch ev.Kind {
	case nmt.EventTimeout:
		n.commLost(ev.Node)
	case nmt.EventResumed:
		n.commBack(ev.Node)
	default: // no special case
	}
	if h := n.cfg.onHB; h != nil {
		h(ev)
	}
}

// onGuard reacts to life guarding events.
func (n *Node) onGuard(ev nmt.GuardEvent) {
	switch ev.Kind {
	case nmt.GuardTimeout:
		n.commLost(0)
	case nmt.GuardResumed:
		n.commBack(0)
	default: // no special case
	}
}

// commLost raises EMCY 0x8130 for a heartbeat (node) or life guarding (0)
// loss and applies the error behaviour of 0x1029.
func (n *Node) commLost(key canopen.NodeID) {
	n.errMu.Lock()
	n.lost[key] = true
	n.errMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	_ = n.emcy.Raise(ctx, emcy.CodeHeartbeat, emcy.RegCommunication, [5]byte{byte(key)})
	cancel()
	n.errorBehaviour()
}

// commBack clears EMCY 0x8130 once every loss is resolved.
func (n *Node) commBack(key canopen.NodeID) {
	n.errMu.Lock()
	if !n.lost[key] {
		n.errMu.Unlock()
		return
	}
	delete(n.lost, key)
	none := len(n.lost) == 0
	n.errMu.Unlock()
	if none {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		_ = n.emcy.Clear(ctx, emcy.CodeHeartbeat)
		cancel()
	}
}

// errorBehaviour applies 0x1029 sub-index 1 to a communication error.
// Without the object the state does not change.
func (n *Node) errorBehaviour() {
	v, err := n.d.GetUint(IndexErrorBehaviour, 1)
	if err != nil {
		return
	}
	switch uint8(v) {
	case ErrorBehaviourPreOperational:
		if n.slave.State() == canopen.StateOperational {
			_ = n.slave.SetState(canopen.StatePreOperational)
		}
	case ErrorBehaviourStopped:
		_ = n.slave.SetState(canopen.StateStopped)
	}
}

// onPDOError turns RPDO errors into emergencies: one per error code,
// raised when the first PDO reports it and cleared when the last one
// resolves it.
func (n *Node) onPDOError(ev pdo.ErrorEvent) {
	n.errMu.Lock()
	set := n.pdoErr[ev.Code]
	raise, clr := false, false
	if ev.Active {
		if set == nil {
			set = map[int]bool{}
			n.pdoErr[ev.Code] = set
		}
		raise = !set[ev.PDO]
		set[ev.PDO] = true
	} else if set[ev.PDO] {
		delete(set, ev.PDO)
		if len(set) == 0 {
			delete(n.pdoErr, ev.Code)
			clr = true
		}
	}
	n.errMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	switch {
	case raise:
		_ = n.emcy.Raise(ctx, ev.Code, emcy.RegCommunication, [5]byte{byte(ev.PDO), byte(ev.PDO >> 8)})
	case clr:
		_ = n.emcy.Clear(ctx, ev.Code)
	}
}

// clearPDOErrors clears every PDO emergency.
func (n *Node) clearPDOErrors() {
	n.errMu.Lock()
	codes := make([]uint16, 0, len(n.pdoErr))
	for c := range n.pdoErr {
		codes = append(codes, c)
	}
	clear(n.pdoErr)
	n.errMu.Unlock()
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	for _, c := range codes {
		_ = n.emcy.Clear(ctx, c)
	}
}

// onSync handles a received SYNC: a length error is an emergency (0x8240,
// cleared by the next correct SYNC), and the PDOs are synchronised.
func (n *Node) onSync(ev syncobj.Event) {
	n.errMu.Lock()
	was := n.syncLenErr
	n.syncLenErr = ev.LengthError
	n.errMu.Unlock()
	if ev.LengthError != was {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		if ev.LengthError {
			_ = n.emcy.Raise(ctx, emcy.CodeSYNCLength, emcy.RegCommunication, [5]byte{})
		} else {
			_ = n.emcy.Clear(ctx, emcy.CodeSYNCLength)
		}
		cancel()
	}
	n.syncPDOs(ev.Counter, ev.WindowEnd, true)
}

// ownSync processes a SYNC this node produced.
func (n *Node) ownSync(f *canbus.Frame) {
	var counter uint8
	if f.Len > 0 {
		counter = f.Data[0]
	}
	n.syncPDOs(counter, time.Time{}, false)
}

// syncPDOs passes a SYNC to the PDOs: RPDOs apply their buffered data
// first, then the TPDOs sample and send. A received SYNC carries the end
// of its synchronous window (0x1007, measured from its reception); the
// node's own SYNC opens the window now.
func (n *Node) syncPDOs(counter uint8, windowEnd time.Time, received bool) {
	if n.slave.State() != canopen.StateOperational {
		return
	}
	for _, r := range n.rpdos {
		if received {
			r.OnSyncWindow(counter, windowEnd)
		} else {
			r.OnSync(counter)
		}
	}
	for _, t := range n.tpdos {
		if received {
			t.OnSyncWindow(counter, windowEnd)
		} else {
			t.OnSync(counter)
		}
	}
}

// onTime delivers a TIME message to the application.
func (n *Node) onTime(ev timestamp.Event) {
	if h := n.cfg.onTime; h != nil && n.mayProduce() {
		h(ev)
	}
}

// onDriveError raises (code ≠ 0) or clears (0) the drive's emergency.
func (n *Node) onDriveError(code uint16) {
	n.errMu.Lock()
	prev := n.driveErr
	n.driveErr = code
	n.errMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if prev != 0 && prev != code {
		_ = n.emcy.Clear(ctx, prev)
	}
	if code != 0 {
		_ = n.emcy.Raise(ctx, code, emcy.ClassRegister(code)|emcy.RegDeviceProfile, [5]byte{})
	}
}

// NodeID returns the active node-ID (it changes at a reset after
// [Node.SetNodeID]).
func (n *Node) NodeID() canopen.NodeID { return n.slave.NodeID() }

// State returns the NMT state.
func (n *Node) State() canopen.NMTState { return n.slave.State() }

// SetState changes the NMT state locally: Operational, Pre-operational or
// Stopped (an application that starts on its own, or stops after a fatal
// error).
func (n *Node) SetState(st canopen.NMTState) error { return n.slave.SetState(st) }

// Reset does an NMT reset locally, as the command from the bus does.
func (n *Node) Reset(ctx context.Context, kind nmt.ResetKind) error { return n.slave.Reset(ctx, kind) }

// SetNodeID sets a pending node-ID (as LSS configures one), which becomes
// active at the next reset communication or reset node: the services then
// use the new node-ID and the node-ID-relative COB-IDs it implies.
func (n *Node) SetNodeID(id canopen.NodeID) error { return n.slave.SetNodeID(id) }

// Dictionary returns the node's object dictionary.
func (n *Node) Dictionary() *od.Dictionary { return n.d }

// EMCY returns the node's emergency producer, for the application to raise
// and clear its errors.
func (n *Node) EMCY() *emcy.Producer { return n.emcy }

// USDOServer returns the USDO server of [WithFD], or nil.
func (n *Node) USDOServer() *usdo.Server { return n.usdoS }

// Drive returns the CiA 402 drive of [WithDrive], or nil.
func (n *Node) Drive() *cia402.Drive { return n.drive }

// HeartbeatConsumer returns the heartbeat consumer (0x1016), or nil.
func (n *Node) HeartbeatConsumer() *nmt.HeartbeatConsumer { return n.hbCons }

// SYNCProducer returns the SYNC producer (0x1005), or nil.
func (n *Node) SYNCProducer() *syncobj.Producer { return n.syncP }

// TPDO returns transmit PDO number i (1..512), or nil when the dictionary
// lacks it.
func (n *Node) TPDO(i int) *pdo.TPDO {
	for _, t := range n.tpdos {
		if t.Number() == i {
			return t
		}
	}
	return nil
}

// RPDO returns receive PDO number i (1..512), or nil when the dictionary
// lacks it.
func (n *Node) RPDO(i int) *pdo.RPDO {
	for _, r := range n.rpdos {
		if r.Number() == i {
			return r
		}
	}
	return nil
}

// SendTime sends a TIME message with t. It fails with [ErrNoObject]
// without 0x1012 and with [timestamp.ErrNotProducer] while 0x1012 bit 30
// is clear; outside Pre-operational and Operational nothing is sent.
func (n *Node) SendTime(ctx context.Context, t time.Time) error {
	if n.timeP == nil {
		return ErrNoObject
	}
	return n.timeP.Send(ctx, t)
}
