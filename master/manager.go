// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
)

// cmdTimeout bounds the NMT commands the manager sends on its own.
const cmdTimeout = time.Second

// slaveState is what the manager knows of a node of the network list.
type slaveState struct {
	ended time.Time // end of the last boot slave process
	// quiet is the end of the time after a reset the manager sent within
	// a boot slave process, during which boot-ups are its own.
	quiet   time.Time
	booting bool
	booted  bool
	// reported: the boot event of the last boot slave process is queued
	// (the network boot-up waits for it, so that it reports done after
	// the events of its mandatory slaves).
	reported bool
	// cancel and done: the running boot slave process (booting), which a
	// network boot-up cancels before its reset.
	cancel context.CancelFunc
	done   chan struct{}
	status BootStatus
	err    error
	state  canopen.NMTState
}

// manager is the CiA 302 manager of a master built on a dictionary: the
// master's own node, the network boot-up, the boot slave processes, the
// request objects and the error control reactions.
type manager struct {
	m     *Master
	d     *od.Dictionary
	n     *node.Node
	guard *nmt.GuardMaster

	mu      sync.Mutex
	slaves  map[canopen.NodeID]*slaveState
	changed chan struct{} // closed and renewed when a boot ends
	net     *netRun       // the network boot-up in progress, or nil
	guarded map[canopen.NodeID]bool
	unhook  []func()
	// lssBusy is set during the LSS step of the network boot-up: the
	// boot-ups of the devices it configures are not booted (the reset
	// that follows the step boots them).
	lssBusy bool
}

// newManager builds the manager and the master's node on d.
func newManager(m *Master, d *od.Dictionary, opts []node.Option) (*manager, error) {
	g := &manager{
		m: m, d: d,
		slaves:  map[canopen.NodeID]*slaveState{},
		changed: make(chan struct{}),
		guarded: map[canopen.NodeID]bool{},
	}
	all := append([]node.Option{node.WithLogger(m.log)}, opts...)
	all = append(all, node.WithoutHeartbeatConsumer(), node.WithStateHandler(g.onOwnState))
	n, err := node.New(m.bus, d, all...)
	if err != nil {
		return nil, fmt.Errorf("master: %w", err)
	}
	g.n = n
	g.guard = nmt.NewGuardMaster(m.bus, nmt.WithLogger(m.log), nmt.WithGuardHandler(g.onGuard))
	if d.Object(IndexRequestNMT) != nil {
		g.unhook = append(g.unhook,
			d.OnWrite(IndexRequestNMT, -1, g.writeRequestNMT),
			d.OnRead(IndexRequestNMT, -1, g.readRequestNMT))
	}
	if d.Object(IndexRequestGuarding) != nil {
		g.unhook = append(g.unhook,
			d.OnWrite(IndexRequestGuarding, -1, g.writeRequestGuarding),
			d.OnRead(IndexRequestGuarding, -1, g.readRequestGuarding))
	}
	return g, nil
}

// start starts the guarding master and the node (whose boot-up starts the
// network boot-up).
func (g *manager) Start() error {
	if err := g.guard.Start(); err != nil {
		return err
	}
	return g.n.Start()
}

// close stops the node, the guarding and the hooks.
func (g *manager) close() {
	_ = g.n.Close()
	_ = g.guard.Close()
	g.mu.Lock()
	if g.net != nil {
		g.net.closed = true
		g.net.cancel()
	}
	hooks := g.unhook
	g.unhook = nil
	g.mu.Unlock()
	for _, f := range hooks {
		f()
	}
}

// u32 reads an UNSIGNED32 of the manager's dictionary (0 when missing).
func (g *manager) u32(index uint16, sub uint8) uint32 {
	v, err := g.d.GetUint(index, sub)
	if err != nil {
		return 0
	}
	return uint32(v)
}

func (g *manager) startup() uint32 { return g.u32(IndexNMTStartup, 0) }

func (g *manager) isMaster() bool { return g.startup()&StartupMaster != 0 }

func (g *manager) assignment(id canopen.NodeID) Assignment {
	return Assignment(g.u32(IndexSlaveAssignment, uint8(id)))
}

// consumerTime returns the heartbeat consumer time of a node in the
// manager's 0x1016 (0: none).
func (g *manager) consumerTime(id canopen.NodeID) time.Duration {
	o := g.d.Object(od.IndexConsumerHeartbeat)
	if o == nil {
		return 0
	}
	var t time.Duration
	for _, v := range o.Subs[1:] {
		if n, d, ok := nmt.ParseConsumerEntry(g.u32(v.Index, v.SubIndex)); ok && n == id {
			t = d
		}
	}
	return t
}

func (g *manager) slave(id canopen.NodeID) *slaveState {
	s := g.slaves[id]
	if s == nil {
		s = &slaveState{}
		g.slaves[id] = s
	}
	return s
}

// broadcastLocked wakes the waiters of a boot end.
func (g *manager) broadcastLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// onOwnState reports the master node's state changes and runs the
// network boot-up at its boot-up (Start, and every reset), and the start
// of the slaves when it enters Operational.
func (g *manager) onOwnState(sc nmt.StateChange) {
	g.m.disp.push(Event{Kind: EventStateChange, Node: sc.Node, State: sc.To, Previous: sc.From, Time: sc.Time})
	switch {
	case sc.From == canopen.StateInitialising && sc.To == canopen.StatePreOperational:
		g.startNetwork()
	case sc.To == canopen.StateOperational:
		g.startAll()
	default: // nothing to do
	}
}

// startNetwork runs the network boot-up in the background, cancelling one
// in progress.
func (g *manager) startNetwork() {
	g.m.goBoot(func(ctx context.Context) { _ = g.runNetwork(ctx) })
}

// runNetwork runs the network boot-up, cancelling one in progress.
func (g *manager) runNetwork(ctx context.Context) error {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &netRun{cancel: cancel, done: make(chan struct{})}
	g.mu.Lock()
	if prev := g.net; prev != nil {
		prev.next = r
		prev.cancel()
	}
	g.net = r
	g.mu.Unlock()
	err := g.bootNetwork(rctx)
	g.mu.Lock()
	next, closed := r.next, r.closed
	if g.net == r {
		g.net = nil
	}
	g.mu.Unlock()
	if next != nil && !closed {
		// Superseded by a newer network boot-up (the automatic one at the
		// master's boot-up, or another BootNetwork call): its result is
		// this call's.
		select {
		case <-next.done:
			err = next.err
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	r.err = err
	close(r.done)
	return err
}

// netRun is one network boot-up. A newer one cancels it and becomes its
// next: the older one then reports the newer one's result.
type netRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error   // set before done is closed
	next   *netRun // guarded by manager.mu
	closed bool    // the manager is closing; guarded by manager.mu
}

// bootNetwork is the network boot-up of CiA 302-2 (as Lely lely-core's
// NMT startup): reset the slaves, boot the mandatory and keep-alive
// slaves (the others boot when their boot-up message arrives), wait for
// the mandatory ones, then enter Operational (which starts the slaves).
func (g *manager) bootNetwork(ctx context.Context) error {
	startup := g.startup()
	if startup&StartupMaster == 0 {
		return nil
	}
	own := g.n.NodeID()
	type entry struct {
		id canopen.NodeID
		a  Assignment
	}
	var list []entry
	keep := false
	for id := canopen.NodeID(1); id <= canopen.MaxNodeID; id++ {
		a := g.assignment(id)
		if id == own || !a.Has(AssignSlave) {
			continue
		}
		list = append(list, entry{id, a})
		keep = keep || a.Has(AssignKeepAlive)
	}
	g.m.log.Info("master: network boot-up", "slaves", len(list))
	if g.m.cfg.lssBoot {
		g.mu.Lock()
		g.lssBusy = true
		g.mu.Unlock()
		g.lssAssign(ctx, own)
		now := time.Now()
		g.mu.Lock()
		g.lssBusy = false
		for _, e := range list {
			if s := g.slave(e.id); s.quiet.Before(now) {
				s.quiet = now
			}
		}
		g.mu.Unlock()
	}
	// A boot slave process started before this network boot-up (at a
	// boot-up message that arrived just before it) would configure the
	// slave before the reset below, which undoes the configuration: it is
	// cancelled, and the slave boots again after the reset.
	var running []chan struct{}
	g.mu.Lock()
	for _, e := range list {
		if s := g.slave(e.id); s.booting && s.cancel != nil {
			g.m.log.Debug("master: cancelling a boot slave process", "node", int(e.id))
			s.cancel()
			running = append(running, s.done)
		}
	}
	g.mu.Unlock()
	for _, done := range running {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	g.mu.Lock()
	for _, e := range list {
		if s := g.slave(e.id); !s.booting {
			s.booted, s.reported = false, false
		}
	}
	g.mu.Unlock()
	// Reset communication of the slaves: one broadcast, or, when some
	// slaves must keep running, every node-ID but those and the master.
	cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	if keep {
		for id := canopen.NodeID(1); id <= canopen.MaxNodeID; id++ {
			if a := g.assignment(id); id != own && !a.Has(AssignSlave|AssignKeepAlive) {
				_ = g.m.ResetCommunication(cctx, id)
			}
		}
	} else {
		_ = g.m.ResetCommunication(cctx, canopen.BroadcastNodeID)
	}
	cancel()
	// The keep-alive slaves boot now. The others boot at their boot-up
	// message; the mandatory ones that send none within the reset
	// timeout are booted then (and retried while they do not answer).
	// Lely lely-core boots the mandatory slaves at once: waiting for the
	// boot-up keeps the first SDO requests from reaching a slave before
	// it processed the reset.
	var mandatory, late []canopen.NodeID
	for _, e := range list {
		if e.a.Has(AssignMandatory) {
			mandatory = append(mandatory, e.id)
		}
		switch {
		case e.a.Has(AssignKeepAlive):
			g.request(e.id, time.Time{})
		case e.a.Has(AssignMandatory):
			late = append(late, e.id)
		}
	}
	lateTimer := time.NewTimer(g.m.cfg.timing.ResetTimeout)
	defer lateTimer.Stop()
	// Wait for the mandatory slaves.
	for {
		g.mu.Lock()
		var failed *BootError
		waiting := false
		for _, id := range mandatory {
			s := g.slave(id)
			switch {
			case s.booting || !s.booted || !s.reported:
				waiting = true
			case !s.status.OK() && failed == nil:
				failed = &BootError{Node: id, Status: s.status, Err: s.err}
			}
		}
		ch := g.changed
		g.mu.Unlock()
		if failed != nil {
			g.m.log.Warn("master: network boot-up halted", "error", failed)
			g.m.disp.push(Event{Kind: EventNetworkBootFailed, Node: failed.Node, Status: failed.Status, Err: failed})
			return failed
		}
		if !waiting {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		case <-lateTimer.C:
			for _, id := range late {
				g.mu.Lock()
				s := g.slave(id)
				idle := !s.booting && !s.booted
				g.mu.Unlock()
				if idle {
					g.request(id, time.Time{})
				}
			}
		}
	}
	g.m.log.Info("master: mandatory slaves booted")
	g.m.disp.push(Event{Kind: EventNetworkBootDone, Node: own, State: g.n.State()})
	if startup&StartupNoAutoOperational == 0 {
		_ = g.n.SetState(canopen.StateOperational)
	}
	return nil
}

// startAll starts the slaves when the master enters Operational and
// 0x1F80 says it starts all of them (bit 1 set, bit 3 clear): with one
// broadcast when every slave of the network list booted and may be
// started, else one by one (as Lely lely-core).
func (g *manager) startAll() {
	startup := g.startup()
	if startup&StartupMaster == 0 || startup&(StartupStartAll|StartupNoStartSlaves) != StartupStartAll {
		return
	}
	own := g.n.NodeID()
	all := true
	var ids []canopen.NodeID
	g.mu.Lock()
	for id := canopen.NodeID(1); id <= canopen.MaxNodeID; id++ {
		a := g.assignment(id)
		if id == own || !a.Has(AssignSlave) {
			continue
		}
		s := g.slave(id)
		ok := s.booted && !s.booting && s.status.OK()
		all = all && ok && a.Has(AssignBoot)
		if ok && a.Has(AssignBoot) && s.state != canopen.StateOperational {
			ids = append(ids, id)
		}
	}
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if all {
		_ = g.m.StartNode(ctx, canopen.BroadcastNodeID)
		return
	}
	for _, id := range ids {
		_ = g.m.StartNode(ctx, id)
	}
}

// request starts the boot slave process of a node of the network list in
// the background, unless one runs (as Lely lely-core: a boot-up during a
// boot slave process does not restart it). at is the reception time of
// the boot-up message that requests it (zero for none): a boot-up
// received while a process ran, but delivered after its end (the
// process's own resets cause such), is ignored as well.
func (g *manager) request(id canopen.NodeID, at time.Time) {
	g.mu.Lock()
	s := g.slave(id)
	if s.booting || !at.IsZero() && (g.lssBusy || at.Before(s.ended) || at.Before(s.quiet)) {
		g.mu.Unlock()
		return
	}
	pctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.booting, s.cancel, s.done = true, cancel, done
	g.mu.Unlock()
	ok := g.m.goBoot(func(ctx context.Context) {
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer close(done)
		defer cancel()
		g.bootProcess(pctx, id)
	})
	if !ok {
		g.mu.Lock()
		s.booting = false
		g.mu.Unlock()
		cancel()
		close(done)
	}
}

// bootProcess runs the boot slave process, repeating it while a node does
// not answer (status B): an optional node until it answers, a mandatory
// one until the boot time 0x1F89 has elapsed (0: no limit).
func (g *manager) bootProcess(ctx context.Context, id canopen.NodeID) {
	start := time.Now()
	for {
		st, status, err := g.bootSlave(ctx, id)
		if ctx.Err() != nil {
			g.mu.Lock()
			s := g.slave(id)
			s.booting, s.ended = false, time.Now()
			g.broadcastLocked()
			g.mu.Unlock()
			return
		}
		if status == BootNoResponse {
			limit := time.Duration(g.u32(IndexBootTime, 0)) * time.Millisecond
			if !g.assignment(id).Has(AssignMandatory) || limit == 0 || time.Since(start) < limit {
				g.m.log.Debug("master: boot slave: no response, retrying", "node", int(id))
				_ = sleep(ctx, g.m.cfg.timing.RetryDelay) // an ended context is reported above
				continue
			}
		}
		g.bootDone(id, st, status, err)
		return
	}
}

// bootDone ends a boot slave process: it records the result, starts the
// error control (heartbeat consumption from 0x1016, else node guarding
// from the assignment), starts the node when allowed, and reports it.
//
// The result is recorded before the master's state is read: startAll sets
// the state before it reads the results, so one of the two starts the
// node. The network boot-up waits for reported, set after the event is
// queued.
func (g *manager) bootDone(id canopen.NodeID, st canopen.NMTState, status BootStatus, err error) {
	a := g.assignment(id)
	startup := g.startup()
	if st == canopen.StateUnknown && status.OK() {
		st = canopen.StatePreOperational
	}
	g.mu.Lock()
	s := g.slave(id)
	s.booting, s.booted, s.reported, s.status, s.err, s.state, s.ended = false, true, false, status, err, st, time.Now()
	g.mu.Unlock()
	if status.OK() {
		if ms := g.consumerTime(id); ms > 0 {
			_ = g.m.Monitor(id, ms)
		} else if gt := a.GuardTime(); gt > 0 {
			_ = g.guardNode(id, true)
		}
	}
	if status == BootOK && a.Has(AssignSlave|AssignBoot) && startup&StartupNoStartSlaves == 0 &&
		(startup&StartupStartAll == 0 || g.n.State() == canopen.StateOperational) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		if g.m.StartNode(ctx, id) == nil {
			st = canopen.StateOperational
			g.mu.Lock()
			s.state = st
			g.mu.Unlock()
		}
		cancel()
	}
	if status.OK() {
		g.m.log.Info("master: slave booted", "node", int(id), "status", status.String())
		g.m.disp.push(Event{Kind: EventBootDone, Node: id, State: st, Status: status})
	} else {
		be := &BootError{Node: id, Status: status, Err: err}
		g.m.log.Warn("master: boot slave failed", "error", be)
		g.m.disp.push(Event{Kind: EventBootFailed, Node: id, State: st, Status: status, Err: be})
	}
	g.mu.Lock()
	s.reported = true
	g.broadcastLocked()
	g.mu.Unlock()
}

// bootSlave runs the boot slave process once (CiA 302-2, in the order of
// Lely lely-core's co_nmt_boot states) and returns the node's state when
// known, the status and its cause.
func (g *manager) bootSlave(ctx context.Context, id canopen.NodeID) (canopen.NMTState, BootStatus, error) {
	t := g.m.cfg.timing
	a := g.assignment(id)
	ms := g.consumerTime(id)
	if !a.Has(AssignSlave) {
		return canopen.StateUnknown, BootNotListed, nil
	}
	if !a.Has(AssignBoot) {
		return g.errorControl(ctx, id, ms, BootOK)
	}
	c, err := g.m.remote(id)
	if err != nil {
		return canopen.StateUnknown, BootNoResponse, err
	}
	if a.Has(AssignKeepAlive) {
		st, status, err := g.checkNode(ctx, id, ms)
		if err != nil {
			return canopen.StateUnknown, status, err
		}
		if st == canopen.StateOperational {
			return g.errorControl(ctx, id, ms, BootOperational)
		}
	}
	updated := false
identity:
	b, err := uploadRetry(ctx, c, t.SDORetries, od.IndexDeviceType, 0)
	if err != nil {
		return canopen.StateUnknown, BootNoResponse, err
	}
	if want := g.u32(IndexExpectedDeviceType, uint8(id)); want != 0 && uint32(canopen.DecodeUint(b)) != want {
		return canopen.StateUnknown, BootDeviceType, fmt.Errorf("%w: 0x1000 is 0x%08X, want 0x%08X", ErrIdentity, canopen.DecodeUint(b), want)
	}
	checks := []struct {
		index  uint16
		status BootStatus
	}{
		{IndexExpectedVendorID, BootVendorID},
		{IndexExpectedProductCode, BootProductCode},
		{IndexExpectedRevision, BootRevision},
		{IndexExpectedSerialNumber, BootSerialNumber},
	}
	for i, ck := range checks {
		want := g.u32(ck.index, uint8(id))
		if want == 0 {
			continue
		}
		got, err := readUintRetry(ctx, c, t.SDORetries, od.IndexIdentity, uint8(i+1))
		if err != nil {
			return canopen.StateUnknown, ck.status, err
		}
		if uint32(got) != want {
			return canopen.StateUnknown, ck.status, fmt.Errorf("%w: 0x1018:%02X is 0x%08X, want 0x%08X", ErrIdentity, i+1, got, want)
		}
	}
	if a.Has(AssignVersionCheck) {
		match, status, err := g.checkSoftware(ctx, id, a, updated)
		if err != nil || status != BootOK {
			return canopen.StateUnknown, status, err
		}
		if !match {
			updated = true
			goto identity
		}
	}
	if status, err := g.configure(ctx, id, a); err != nil {
		return canopen.StateUnknown, status, err
	}
	return g.errorControl(ctx, id, ms, BootOK)
}

// checkNode checks a keep-alive node (Lely's "check node state"): it
// waits for its heartbeat (or polls it with node guarding when the
// manager does not consume its heartbeat). An operational node is left
// running; another one is reset (communication) and must boot.
func (g *manager) checkNode(ctx context.Context, id canopen.NodeID, ms time.Duration) (canopen.NMTState, BootStatus, error) {
	t := g.m.cfg.timing
	status, wait := BootHeartbeat, ms
	if ms == 0 {
		status, wait = BootGuarding, t.GuardTimeout
	}
	sub, err := g.subscribeEC(id)
	if err != nil {
		return canopen.StateUnknown, status, err
	}
	defer func() { _ = sub.Close() }()
	if ms == 0 {
		f := nmt.GuardRequest(id)
		sctx, cancel := context.WithTimeout(ctx, cmdTimeout)
		err := g.m.bus.Send(sctx, &f)
		cancel()
		if err != nil {
			return canopen.StateUnknown, status, err
		}
	}
	st, err := waitEC(ctx, sub, wait, func(canopen.NMTState) bool { return true })
	if err != nil {
		return canopen.StateUnknown, status, err
	}
	if st == canopen.StateOperational {
		return st, BootOK, nil
	}
	if err := g.resetAndWait(ctx, sub, id, canopen.NMTResetCommunication); err != nil {
		return canopen.StateUnknown, status, err
	}
	return canopen.StatePreOperational, BootOK, nil
}

// checkSoftware compares the node's application software identification
// with the expected one and updates the software when allowed. match
// reports an identical software; after an update the identity is checked
// again (as Lely lely-core does), and a second mismatch fails.
func (g *manager) checkSoftware(ctx context.Context, id canopen.NodeID, a Assignment, updated bool) (match bool, status BootStatus, err error) {
	t := g.m.cfg.timing
	want := g.u32(program.IndexExpectedIdentification, uint8(id))
	if want == 0 {
		return false, BootProgramObjects, errors.New("master: no expected application software identification (0x1F55)")
	}
	c, err := g.m.remote(id)
	if err != nil {
		return false, BootProgramObjects, err
	}
	b, err := uploadRetry(ctx, c, t.SDORetries, program.IndexIdentification, 1)
	if err != nil {
		return false, BootProgramObjects, err
	}
	got := uint32(canopen.DecodeUint(b))
	if got == want {
		return true, BootOK, nil
	}
	mismatch := fmt.Errorf("%w: 0x1F56:01 is 0x%08X, want 0x%08X", ErrProgram, got, want)
	if updated {
		return false, BootUpdateFailed, mismatch
	}
	if !a.Has(AssignSoftwareUpdate) || a.Has(AssignKeepAlive) {
		return false, BootUpdateNotAllowed, mismatch
	}
	data, err := g.d.Get(program.IndexManagerData, uint8(id))
	if err != nil || len(data) == 0 {
		return false, BootUpdateFailed, fmt.Errorf("%w: no program data (0x1F58:%02X)", ErrProgram, uint8(id))
	}
	g.m.log.Info("master: updating the software of a slave", "node", int(id), "bytes", len(data))
	// The update (stop, clear, download, start) is not interrupted by a
	// network boot-up that cancels this process: stopped half-way, the
	// slave would keep the new program stopped (the next process finds
	// it up to date). Only the master's Close stops it.
	if err := g.m.DownloadProgram(g.m.bootCtx, id, data, ProgramOptions{Identification: want}); err != nil {
		return false, BootUpdateFailed, err
	}
	return false, BootOK, nil
}

// configure is the "check and update configuration" step: skipped when
// the node's 0x1020 holds the expected configuration date and time
// (0x1F26, 0x1F27, both set); otherwise the node's parameters are
// restored when its assignment says so, and its concise DCF (0x1F22) is
// written, followed by a store request when [WithStoreAfterConfigure] is
// set.
func (g *manager) configure(ctx context.Context, id canopen.NodeID, a Assignment) (BootStatus, error) {
	c, err := g.m.remote(id)
	if err != nil {
		return BootConfiguration, err
	}
	t := g.m.cfg.timing
	date, tod := g.u32(cdcf.IndexExpectedConfigDate, uint8(id)), g.u32(cdcf.IndexExpectedConfigTime, uint8(id))
	if date != 0 && tod != 0 {
		d, err := uploadRetry(ctx, c, t.SDORetries, cdcf.IndexVerifyConfiguration, 1)
		if err == nil && uint32(canopen.DecodeUint(d)) == date {
			if tm, err := readUintRetry(ctx, c, t.SDORetries, cdcf.IndexVerifyConfiguration, 2); err == nil && uint32(tm) == tod {
				g.m.log.Debug("master: configuration up to date", "node", int(id))
				return BootOK, nil
			}
		}
	}
	if sub := uint8(g.u32(IndexRestoreConfiguration, uint8(id))); a.Has(AssignRestore) && sub != 0 {
		if err := g.restore(ctx, id, sub); err != nil {
			return BootConfiguration, err
		}
	}
	// Without 0x1F22 (or its sub-index) the node has no configuration.
	blob, _ := g.d.Get(cdcf.IndexConciseDCF, uint8(id))
	entries, err := cdcf.Decode(blob)
	if err != nil {
		return BootConfiguration, err
	}
	if len(entries) == 0 {
		return BootOK, nil
	}
	retries := g.m.cfg.timing.SDORetries
	r, err := g.m.remote(id)
	if err != nil {
		return BootConfiguration, err
	}
	for i, e := range entries {
		// As ConfigureConcise, but a write that timed out is repeated
		// (it writes the same value again).
		err := retry(ctx, retries, func() error { return r.Download(ctx, e.Index, e.SubIndex, e.Data) })
		if err != nil {
			return BootConfiguration, fmt.Errorf("master: node %d: concise DCF entry %d (%04X:%02X): %w", id, i+1, e.Index, e.SubIndex, err)
		}
	}
	if grp := g.m.cfg.storeGroup; grp != 0 {
		if err := retry(ctx, retries, func() error { return g.m.StoreParameters(ctx, id, grp) }); err != nil {
			return BootConfiguration, err
		}
	}
	return BootOK, nil
}

// restore writes "load" to the node's 0x1011 sub-index and resets it
// (communication for sub-index 2, node otherwise) so that the defaults
// apply, waiting for its boot-up (Lely lely-core's configuration
// request).
func (g *manager) restore(ctx context.Context, id canopen.NodeID, sub uint8) error {
	// Writing "load" again after a timeout is harmless.
	if err := retry(ctx, g.m.cfg.timing.SDORetries, func() error { return g.m.RestoreDefaults(ctx, id, sub) }); err != nil {
		return err
	}
	es, err := g.subscribeEC(id)
	if err != nil {
		return err
	}
	defer func() { _ = es.Close() }()
	cmd := canopen.NMTResetNode
	if sub == 2 {
		cmd = canopen.NMTResetCommunication
	}
	return g.resetAndWait(ctx, es, id, cmd)
}

// resetAndWait sends a reset to a node and waits for its boot-up message
// on sub (subscribed before).
func (g *manager) resetAndWait(ctx context.Context, sub *canbus.Subscription, id canopen.NodeID, cmd canopen.NMTCommand) error {
	cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	err := g.m.NMT(cctx, id, cmd)
	cancel()
	if err != nil {
		return err
	}
	_, err = waitEC(ctx, sub, g.m.cfg.timing.ResetTimeout, func(st canopen.NMTState) bool { return st == canopen.StateInitialising })
	if err != nil {
		return fmt.Errorf("master: node %d: no boot-up after %s: %w", id, cmd, err)
	}
	// The heartbeat consumer reports the same boot-up, maybe later: it
	// must not start another boot slave process.
	g.mu.Lock()
	g.slave(id).quiet = time.Now().Add(g.m.cfg.timing.ResetTimeout)
	g.mu.Unlock()
	return nil
}

// errorControl is the "start error control" step: with a heartbeat
// consumer time for the node, its first heartbeat must arrive within it
// (status K otherwise). ok is the status to return on success.
func (g *manager) errorControl(ctx context.Context, id canopen.NodeID, ms time.Duration, ok BootStatus) (canopen.NMTState, BootStatus, error) {
	if ms <= 0 {
		return canopen.StateUnknown, ok, nil
	}
	sub, err := g.subscribeEC(id)
	if err != nil {
		return canopen.StateUnknown, BootErrorControl, err
	}
	defer func() { _ = sub.Close() }()
	st, err := waitEC(ctx, sub, ms, func(st canopen.NMTState) bool { return st != canopen.StateInitialising })
	if err != nil {
		return canopen.StateUnknown, BootErrorControl, err
	}
	return st, ok, nil
}

// subscribeEC subscribes to the error control messages of a node.
func (g *manager) subscribeEC(id canopen.NodeID) (*canbus.Subscription, error) {
	return g.m.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{
		ID: canopen.FuncHeartbeat + canbus.ID(id), Mask: canbus.MaxStandardID,
		Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly,
	}}}))
}

// waitEC waits up to timeout for an error control message whose state
// accept takes.
func waitEC(ctx context.Context, sub *canbus.Subscription, timeout time.Duration, accept func(canopen.NMTState) bool) (canopen.NMTState, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return canopen.StateUnknown, ctx.Err()
		case <-t.C:
			return canopen.StateUnknown, fmt.Errorf("%w: no error control message within %v", canopen.ErrTimeout, timeout)
		case <-sub.Done():
			return canopen.StateUnknown, canopen.ErrClosed
		case f := <-sub.Frames():
			_, st, _, err := nmt.ParseErrorControl(&f)
			if err == nil && accept(st) {
				return st, nil
			}
		}
	}
}

// guardNode starts or stops node guarding of a node with the guard time
// and retry factor of its assignment. A node guarded already keeps its
// guarding (and its loss, so that its return is reported).
func (g *manager) guardNode(id canopen.NodeID, on bool) error {
	a := g.assignment(id)
	gt := a.GuardTime()
	if !on {
		gt = 0
	} else if gt == 0 {
		return canopen.AbortDeviceState
	}
	g.mu.Lock()
	already := g.guarded[id] && on
	g.mu.Unlock()
	if already {
		return nil
	}
	if err := g.guard.Guard(id, gt, a.RetryFactor()); err != nil {
		return err
	}
	g.mu.Lock()
	if on {
		g.guarded[id] = true
	} else {
		delete(g.guarded, id)
	}
	g.mu.Unlock()
	return nil
}

// onGuard reports node guarding events and reacts to losses.
func (g *manager) onGuard(ev nmt.GuardEvent) {
	switch ev.Kind {
	case nmt.GuardTimeout:
		g.m.disp.push(Event{Kind: EventGuardingLost, Node: ev.Node, State: ev.State, Time: ev.Time})
		g.nodeError(ev.Node)
	case nmt.GuardResumed:
		g.m.disp.push(Event{Kind: EventGuardingResumed, Node: ev.Node, State: ev.State, Time: ev.Time})
	default: // state changes and boot-ups are seen by the heartbeat consumer
	}
}

// nodeError is the reaction to an error control event of a slave of the
// network list (Lely lely-core's co_nmt_node_err_ind): a mandatory slave
// stops all nodes (0x1F80 bit 6) or resets all nodes (bit 4), the master
// included; otherwise the slave alone is reset (reset node), which boots
// it again when it comes back.
func (g *manager) nodeError(id canopen.NodeID) {
	startup := g.startup()
	a := g.assignment(id)
	if startup&StartupMaster == 0 || !a.Has(AssignSlave) {
		return
	}
	g.mu.Lock()
	booting := g.slave(id).booting
	g.mu.Unlock()
	if booting {
		return
	}
	mandatory := a.Has(AssignMandatory)
	var reaction string
	switch {
	case mandatory && startup&StartupStopAllOnError != 0:
		reaction = "stop all nodes"
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		_ = g.m.StopNode(ctx, canopen.BroadcastNodeID)
		cancel()
		_ = g.n.SetState(canopen.StateStopped)
	case mandatory && startup&StartupResetAllOnError != 0:
		reaction = "reset all nodes"
		g.m.goBoot(func(ctx context.Context) {
			cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
			_ = g.m.ResetNode(cctx, canopen.BroadcastNodeID)
			cancel()
			_ = g.n.Reset(ctx, nmt.ResetApplication)
		})
	default:
		reaction = "reset node"
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		_ = g.m.ResetNode(ctx, id)
		cancel()
	}
	g.m.log.Warn("master: error control event of a slave", "node", int(id), "mandatory", mandatory, "reaction", reaction)
	g.m.disp.push(Event{Kind: EventNodeError, Node: id, Err: fmt.Errorf("master: error control event of node %d: %s", id, reaction)})
}

// requestTarget returns the node-ID a sub-index of 0x1F82/0x1F83
// addresses (0 for sub-index 128, all nodes), refusing nodes outside the
// network list.
func (g *manager) requestTarget(sub uint8) (canopen.NodeID, error) {
	if sub == 0x80 {
		return canopen.BroadcastNodeID, nil
	}
	id := canopen.NodeID(sub)
	if !id.Valid() || !g.assignment(id).Has(AssignSlave) {
		return 0, canopen.AbortInvalidValue
	}
	return id, nil
}

// writeRequestNMT executes a write of 0x1F82 (request NMT).
func (g *manager) writeRequestNMT(v *od.Variable, data []byte) error {
	id, err := g.requestTarget(v.SubIndex)
	if err != nil {
		return err
	}
	if !g.isMaster() {
		return canopen.AbortLocalControl
	}
	var cmd canopen.NMTCommand
	switch canopen.NMTState(data[0]) {
	case canopen.StateStopped:
		cmd = canopen.NMTStop
	case canopen.StateOperational:
		cmd = canopen.NMTStart
	case canopen.StatePreOperational:
		cmd = canopen.NMTEnterPreOperational
	case 6:
		cmd = canopen.NMTResetNode
	case 7:
		cmd = canopen.NMTResetCommunication
	default:
		return canopen.AbortInvalidValue
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if err := g.m.NMT(ctx, id, cmd); err != nil {
		return canopen.AbortGeneral
	}
	return nil
}

// readRequestNMT returns the state of a node for 0x1F82 (0: unknown).
func (g *manager) readRequestNMT(v *od.Variable, stored []byte) ([]byte, error) {
	if v.SubIndex == 0 || v.SubIndex > byte(canopen.MaxNodeID) {
		return stored, nil
	}
	st := g.m.State(canopen.NodeID(v.SubIndex))
	if st == canopen.StateUnknown {
		st = 0
	}
	return []byte{byte(st)}, nil
}

// writeRequestGuarding executes a write of 0x1F83 (request node
// guarding): 1 starts, 0 stops guarding with the assignment's guard time
// and retry factor; a node without a guard time refuses the start.
func (g *manager) writeRequestGuarding(v *od.Variable, data []byte) error {
	id, err := g.requestTarget(v.SubIndex)
	if err != nil {
		return err
	}
	if !g.isMaster() {
		return canopen.AbortLocalControl
	}
	if data[0] > 1 {
		return canopen.AbortInvalidValue
	}
	on := data[0] == 1
	if id != canopen.BroadcastNodeID {
		return g.guardNode(id, on)
	}
	for n := canopen.NodeID(1); n <= canopen.MaxNodeID; n++ {
		if a := g.assignment(n); a.Has(AssignSlave) && (!on || a.GuardTime() > 0) {
			_ = g.guardNode(n, on)
		}
	}
	return nil
}

// readRequestGuarding returns 1 for a guarded node.
func (g *manager) readRequestGuarding(v *od.Variable, stored []byte) ([]byte, error) {
	if v.SubIndex == 0 || v.SubIndex > byte(canopen.MaxNodeID) {
		return stored, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.guarded[canopen.NodeID(v.SubIndex)] {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

// lssAssign is the LSS step of the network boot-up (WithLSSBoot): every
// unconfigured device gets the node-ID of the slave of the network list
// whose expected identity it matches.
func (g *manager) lssAssign(ctx context.Context, own canopen.NodeID) {
	used := map[canopen.NodeID]bool{}
	// A lost frame can hide a device from a Fastscan: scan again while a
	// round assigns node-IDs (the configured devices no longer answer).
	for round := 0; round < lssRounds && ctx.Err() == nil; round++ {
		if g.lssRound(ctx, own, used) == 0 {
			return
		}
	}
}

// lssRounds bounds the Fastscan rounds of the LSS step.
const lssRounds = 4

// lssRound runs one Fastscan and assigns the node-IDs it can; it returns
// how many it assigned.
func (g *manager) lssRound(ctx context.Context, own canopen.NodeID, used map[canopen.NodeID]bool) int {
	addrs, err := g.m.lssM.Fastscan(ctx, g.m.cfg.lssFS)
	if err != nil {
		g.m.log.Warn("master: LSS Fastscan", "error", err)
	}
	assigned := 0
	for _, a := range addrs {
		var match []canopen.NodeID
		for id := canopen.NodeID(1); id <= canopen.MaxNodeID; id++ {
			if id != own && g.assignment(id).Has(AssignSlave) && g.identityMatches(id, a) {
				match = append(match, id)
			}
		}
		if len(match) != 1 || used[match[0]] {
			g.m.log.Warn("master: LSS: no single slave of the network list for an unconfigured device", "address", a.String(), "matches", len(match))
			continue
		}
		id := match[0]
		if err := g.m.lssM.ConfigureNodeID(ctx, a, id); err != nil {
			g.m.log.Warn("master: LSS: configure node-ID", "address", a.String(), "node", int(id), "error", err)
			continue
		}
		used[id] = true
		assigned++
		g.m.log.Info("master: LSS node-ID assigned", "address", a.String(), "node", int(id))
		g.m.disp.push(Event{Kind: EventLSSAssigned, Node: id, LSS: &lss.Assignment{Address: a, NodeID: id}})
	}
	return assigned
}

// identityMatches compares the non-zero expected identity of a slave
// (0x1F85..0x1F88) with an LSS address; false when none is set.
func (g *manager) identityMatches(id canopen.NodeID, a lss.Address) bool {
	parts := []struct {
		index uint16
		part  lss.Part
	}{
		{IndexExpectedVendorID, lss.PartVendor},
		{IndexExpectedProductCode, lss.PartProduct},
		{IndexExpectedRevision, lss.PartRevision},
		{IndexExpectedSerialNumber, lss.PartSerial},
	}
	set := false
	for _, p := range parts {
		want := g.u32(p.index, uint8(id))
		if want == 0 {
			continue
		}
		if a.Part(p.part) != want {
			return false
		}
		set = true
	}
	return set
}
