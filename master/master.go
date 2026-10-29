// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
	syncobj "github.com/otfabric/go-canopen/sync"
	"github.com/otfabric/go-canopen/timestamp"
)

// Option configures a [Master].
type Option func(*config)

type config struct {
	log      *slog.Logger
	handler  func(Event)
	sdoOpts  []sdo.ClientOption
	scanOpts []sdo.ClientOption
	parallel int
	id       canopen.NodeID
	hbPeriod time.Duration
	dict     *od.Dictionary
	nodeOpts []node.Option
	timing   BootTiming
	// storeGroup is the 0x1010 sub-index written after a configuration
	// download (0: none).
	storeGroup uint8
}

// WithLogger logs the master's events, and those of its services, to l.
// The default logs nothing.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.log = l
		}
	}
}

// WithEventHandler sets the function every [Event] is delivered to. Events
// are delivered one at a time, in the order they occurred, on a goroutine
// of the master's own: the handler may call the master (SDO transfers, NMT
// commands) but not [Master.Close]. A slow handler delays later events,
// never the services (the queue is unbounded).
func WithEventHandler(h func(Event)) Option {
	return func(c *config) { c.handler = h }
}

// WithSDOOptions sets the options of the SDO clients [Master.SDO] creates
// (timeout, retries, block mode...).
func WithSDOOptions(opts ...sdo.ClientOption) Option {
	return func(c *config) { c.sdoOpts = append(c.sdoOpts, opts...) }
}

// WithScanOptions sets the options of the SDO clients [Master.Scan] uses
// (default: a 100 ms timeout, so that absent nodes are passed quickly).
func WithScanOptions(opts ...sdo.ClientOption) Option {
	return func(c *config) { c.scanOpts = append(c.scanOpts, opts...) }
}

// WithScanParallelism sets how many nodes [Master.Scan] queries at once
// (default 16).
func WithScanParallelism(n int) Option {
	return func(c *config) { c.parallel = n }
}

// WithHeartbeat makes the master produce a heartbeat as node id every
// period (state Operational), for nodes that supervise the master with
// 0x1016.
func WithHeartbeat(id canopen.NodeID, period time.Duration) Option {
	return func(c *config) { c.id, c.hbPeriod = id, period }
}

// WithDictionary makes the master a CANopen device of its own, built from
// d with package node (its node-ID is d's): the master then has an SDO
// server, a heartbeat producer (0x1017) and the other services of its
// dictionary, and is a CiA 302 manager driven by the objects of d (see
// [AddManagerObjects] and the package documentation): with bit 0 of the
// NMT start-up 0x1F80 set, every boot-up of the master's node (at Start
// and after its resets) runs the network boot-up of CiA 302-2 over the
// slaves of 0x1F81, configuring them from their concise DCFs (0x1F22).
// opts are passed to node.New (e.g. node.WithStore); the master sets the
// node's state handler (the node's state changes are reported as
// EventStateChange events of its node-ID) and leaves out its heartbeat
// consumer (the master supervises the slaves of 0x1016 itself).
// [WithHeartbeat] cannot be combined with it (0x1017 sets the
// heartbeat).
func WithDictionary(d *od.Dictionary, opts ...node.Option) Option {
	return func(c *config) { c.dict, c.nodeOpts = d, opts }
}

// WithBootTiming sets the times of the CiA 302 boot-up and of
// [Master.DownloadProgram]; zero fields keep their defaults.
func WithBootTiming(t BootTiming) Option {
	return func(c *config) { c.timing = t }
}

// WithStoreAfterConfigure makes the CiA 302 boot-up ask a slave to save
// its parameters (0x1010 sub-index group, see [Master.StoreParameters])
// after a concise DCF was written to it, so that the configuration
// survives the slave's restart; with the configuration date and time in
// the concise DCF (0x1020) and in 0x1F26/0x1F27, the next boot-up then
// skips the configuration. 0 (the default) asks for nothing.
func WithStoreAfterConfigure(group uint8) Option {
	return func(c *config) { c.storeGroup = group }
}

// Master is a CANopen network manager: NMT commands, heartbeat supervision,
// emergency reception, SDO clients for every node, scanning, node
// configuration, the boot procedure, remote PDOs, SYNC and TIME
// production and CiA 402 drive control. See the package documentation.
type Master struct {
	bus canopen.Bus
	cfg config
	log *slog.Logger

	nmt    *nmt.Master
	hb     *nmt.HeartbeatConsumer
	em     *emcy.Consumer
	syncP  *syncobj.Producer
	timeP  *timestamp.Producer
	hbProd *nmt.HeartbeatProducer
	disp   *dispatcher
	mgr    *manager // CiA 302 manager of WithDictionary, or nil

	lcMu    sync.Mutex
	started bool
	closed  bool

	mu      sync.Mutex
	clients map[canopen.NodeID]*sdo.Client
	closers []closer
	boots   map[canopen.NodeID]*BootConfig
	running map[canopen.NodeID]context.CancelFunc
	bootCtx context.Context
	stopAll context.CancelFunc
	wg      sync.WaitGroup
}

type closer interface{ Close() error }

// New returns a master on bus. It does nothing on the bus until
// [Master.Start], except that NMT commands and SDO transfers work at once.
func New(bus canopen.Bus, opts ...Option) (*Master, error) {
	if bus == nil {
		return nil, errors.New("master: New needs a bus")
	}
	c := config{log: slog.New(slog.NewTextHandler(io.Discard, nil)), parallel: 16}
	for _, o := range opts {
		o(&c)
	}
	if c.parallel < 1 {
		c.parallel = 1
	}
	c.timing = c.timing.withDefaults()
	if c.dict != nil && c.hbPeriod > 0 {
		return nil, errors.New("master: WithHeartbeat and WithDictionary exclude each other (set 0x1017)")
	}
	m := &Master{
		bus: bus, cfg: c, log: c.log,
		clients: map[canopen.NodeID]*sdo.Client{},
		boots:   map[canopen.NodeID]*BootConfig{},
		running: map[canopen.NodeID]context.CancelFunc{},
	}
	m.bootCtx, m.stopAll = context.WithCancel(context.Background())
	m.disp = newDispatcher(c.handler)
	m.nmt = nmt.NewMaster(bus, nmt.WithLogger(m.log))
	var err error
	if m.hb, err = nmt.NewHeartbeatConsumer(bus, nmt.WithLogger(m.log), nmt.WithHeartbeatHandler(m.onHeartbeat)); err != nil {
		return nil, err
	}
	m.em = emcy.NewConsumer(bus, emcy.WithLogger(m.log), emcy.WithHandler(m.onEMCY))
	if m.syncP, err = syncobj.NewProducer(bus, syncobj.WithLogger(m.log)); err != nil {
		return nil, err
	}
	if m.timeP, err = timestamp.NewProducer(bus, timestamp.WithLogger(m.log)); err != nil {
		return nil, err
	}
	if c.hbPeriod > 0 {
		op := func() canopen.NMTState { return canopen.StateOperational }
		if m.hbProd, err = nmt.NewHeartbeatProducer(bus, c.id, op, nmt.WithLogger(m.log), nmt.WithHeartbeatPeriod(c.hbPeriod)); err != nil {
			return nil, err
		}
	}
	if c.dict != nil {
		if m.mgr, err = newManager(m, c.dict, c.nodeOpts); err != nil {
			_ = m.hb.Close()
			_ = m.em.Close()
			_ = m.syncP.Close()
			_ = m.timeP.Close()
			return nil, err
		}
	}
	return m, nil
}

// Start starts the heartbeat consumer, the emergency consumer, the event
// delivery, the SYNC and TIME producers and the master's heartbeat.
func (m *Master) Start() error {
	m.lcMu.Lock()
	defer m.lcMu.Unlock()
	switch {
	case m.closed:
		return canopen.ErrClosed
	case m.started:
		return fmt.Errorf("master: %w: already started", canopen.ErrBusy)
	}
	m.started = true
	go m.disp.run()
	svcs := []interface{ Start() error }{m.hb, m.em, m.syncP, m.timeP}
	if m.hbProd != nil {
		svcs = append(svcs, m.hbProd)
	}
	if m.mgr != nil {
		// Last: the node's boot-up starts the network boot-up.
		svcs = append(svcs, m.mgr)
	}
	for _, s := range svcs {
		if err := s.Start(); err != nil {
			m.lcMu.Unlock()
			_ = m.Close()
			m.lcMu.Lock()
			return err
		}
	}
	return nil
}

// Close stops the services, the boot procedures in progress, the remote
// PDO listeners and the SDO clients, and waits for them. Queued events are
// delivered before it returns.
func (m *Master) Close() error {
	m.lcMu.Lock()
	if m.closed {
		m.lcMu.Unlock()
		return nil
	}
	m.closed = true
	started := m.started
	m.lcMu.Unlock()
	// The heartbeat consumer starts boot procedures: close it first.
	_ = m.hb.Close()
	_ = m.em.Close()
	if m.hbProd != nil {
		_ = m.hbProd.Close()
	}
	if m.mgr != nil {
		m.mgr.close()
	}
	m.mu.Lock()
	m.stopAll()
	m.mu.Unlock()
	m.wg.Wait()
	_ = m.syncP.Close()
	_ = m.timeP.Close()
	m.mu.Lock()
	cl := m.closers
	m.closers = nil
	clients := m.clients
	m.clients = map[canopen.NodeID]*sdo.Client{}
	m.mu.Unlock()
	for _, c := range cl {
		_ = c.Close()
	}
	for _, c := range clients {
		_ = c.Close()
	}
	if started {
		m.disp.close()
	}
	return nil
}

func (m *Master) isClosed() bool {
	m.lcMu.Lock()
	defer m.lcMu.Unlock()
	return m.closed
}

// track registers a service to close with the master; it closes it at
// once when the master is closed.
func (m *Master) track(c closer) error {
	m.mu.Lock()
	if m.isClosed() {
		m.mu.Unlock()
		_ = c.Close()
		return canopen.ErrClosed
	}
	m.closers = append(m.closers, c)
	m.mu.Unlock()
	return nil
}

// onHeartbeat turns heartbeat consumer events into master events and
// starts the boot procedure of a node that booted.
func (m *Master) onHeartbeat(ev nmt.HeartbeatEvent) {
	e := Event{Node: ev.Node, State: ev.State, Previous: ev.Previous, Time: ev.Time}
	switch ev.Kind {
	case nmt.EventBootUp:
		e.Kind = EventBootUp
	case nmt.EventStateChange:
		e.Kind = EventStateChange
	case nmt.EventTimeout:
		e.Kind = EventHeartbeatLost
	case nmt.EventResumed:
		e.Kind = EventHeartbeatResumed
	default:
		return
	}
	m.disp.push(e)
	switch {
	case ev.Kind == nmt.EventBootUp:
		m.bootUp(ev.Node, ev.Time)
	case ev.Kind == nmt.EventTimeout && m.mgr != nil:
		m.mgr.nodeError(ev.Node)
	}
}

// goBoot runs f on a goroutine of the master's (waited for by Close) with
// the master's background context; it returns false after Close.
func (m *Master) goBoot(f func(ctx context.Context)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bootCtx.Err() != nil {
		return false
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		f(m.bootCtx)
	}()
	return true
}

func (m *Master) onEMCY(ev emcy.Event) {
	m.disp.push(Event{Kind: EventEMCY, Node: ev.Node, EMCY: &ev, Time: ev.Time})
}

// NMT sends an NMT command to a node, or to all nodes with node-ID 0.
func (m *Master) NMT(ctx context.Context, node canopen.NodeID, cmd canopen.NMTCommand) error {
	return m.nmt.Send(ctx, node, cmd)
}

// StartNode sends NMT start (enter Operational) to a node (0: all).
func (m *Master) StartNode(ctx context.Context, node canopen.NodeID) error {
	return m.NMT(ctx, node, canopen.NMTStart)
}

// StopNode sends NMT stop (enter Stopped) to a node (0: all).
func (m *Master) StopNode(ctx context.Context, node canopen.NodeID) error {
	return m.NMT(ctx, node, canopen.NMTStop)
}

// PreOperational sends NMT enter pre-operational to a node (0: all).
func (m *Master) PreOperational(ctx context.Context, node canopen.NodeID) error {
	return m.NMT(ctx, node, canopen.NMTEnterPreOperational)
}

// ResetNode sends NMT reset node to a node (0: all).
func (m *Master) ResetNode(ctx context.Context, node canopen.NodeID) error {
	return m.NMT(ctx, node, canopen.NMTResetNode)
}

// ResetCommunication sends NMT reset communication to a node (0: all).
func (m *Master) ResetCommunication(ctx context.Context, node canopen.NodeID) error {
	return m.NMT(ctx, node, canopen.NMTResetCommunication)
}

// Monitor supervises a node's heartbeat with a consumer heartbeat time (at
// least the node's producer time plus a margin); 0 ends the supervision.
// The node's state is tracked whether supervised or not.
func (m *Master) Monitor(node canopen.NodeID, timeout time.Duration) error {
	return m.hb.Monitor(node, timeout)
}

// State returns a node's NMT state as its last heartbeat or boot-up
// reported it: Unknown before any and after a loss.
func (m *Master) State(node canopen.NodeID) canopen.NMTState { return m.hb.State(node) }

// Status returns what the heartbeat consumer knows of a node.
func (m *Master) Status(node canopen.NodeID) nmt.NodeStatus { return m.hb.Status(node) }

// Nodes returns the status of every node heard or supervised.
func (m *Master) Nodes() []nmt.NodeStatus { return m.hb.Nodes() }

// WaitState waits until a node reports a state by heartbeat (at once when
// it is the current one).
func (m *Master) WaitState(ctx context.Context, node canopen.NodeID, st canopen.NMTState) error {
	return m.hb.WaitState(ctx, node, st)
}

// WaitBootUp waits for a boot-up message of a node received at or after
// since (take it before sending the reset).
func (m *Master) WaitBootUp(ctx context.Context, node canopen.NodeID, since time.Time) error {
	return m.hb.WaitBootUp(ctx, node, since)
}

// LastEMCY returns the last emergency message of a node.
func (m *Master) LastEMCY(node canopen.NodeID) (emcy.Event, bool) { return m.em.Last(node) }

// SDO returns the SDO client of a node (default COB-IDs 0x600/0x580 +
// node-ID), created on first use with the options of [WithSDOOptions] and
// kept until Close. A client serialises its transfers; clients of
// different nodes work in parallel.
func (m *Master) SDO(node canopen.NodeID) (*sdo.Client, error) {
	if err := node.Check(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.isClosed() {
		return nil, canopen.ErrClosed
	}
	if c := m.clients[node]; c != nil {
		return c, nil
	}
	opts := append([]sdo.ClientOption{sdo.WithLogger(m.log)}, m.cfg.sdoOpts...)
	c, err := sdo.NewClient(m.bus, node, opts...)
	if err != nil {
		return nil, err
	}
	m.clients[node] = c
	return c, nil
}

// SYNC returns the master's SYNC producer (COB-ID 0x080): set a period
// ([syncobj.Producer.SetPeriod]) to produce SYNC cyclically, or send one
// with [syncobj.Producer.Send].
func (m *Master) SYNC() *syncobj.Producer { return m.syncP }

// TIME returns the master's TIME producer (COB-ID 0x100), which sends the
// time on [timestamp.Producer.Send].
func (m *Master) TIME() *timestamp.Producer { return m.timeP }

// Stats is a snapshot of the master's counters.
type Stats struct {
	NMT       nmt.MasterStats                    `json:"nmt"`
	Heartbeat nmt.HeartbeatConsumerStats         `json:"heartbeat"`
	EMCY      emcy.ConsumerStats                 `json:"emcy"`
	SYNC      syncobj.ProducerStats              `json:"sync"`
	TIME      timestamp.ProducerStats            `json:"time"`
	SDO       map[canopen.NodeID]sdo.ClientStats `json:"sdo"`
}

// Stats returns a snapshot of the counters.
func (m *Master) Stats() Stats {
	s := Stats{
		NMT: m.nmt.Stats(), Heartbeat: m.hb.Stats(), EMCY: m.em.Stats(),
		SYNC: m.syncP.Stats(), TIME: m.timeP.Stats(), SDO: map[canopen.NodeID]sdo.ClientStats{},
	}
	m.mu.Lock()
	for n, c := range m.clients {
		s.SDO[n] = c.Stats()
	}
	m.mu.Unlock()
	return s
}
