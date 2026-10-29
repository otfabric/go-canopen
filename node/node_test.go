// SPDX-License-Identifier: MIT

package node_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/timestamp"
)

func TestNMTStates(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	var mu sync.Mutex
	var changes []nmt.StateChange
	since := time.Now()
	n := startNode(t, r.buses[1], d, node.WithStateHandler(func(sc nmt.StateChange) {
		mu.Lock()
		changes = append(changes, sc)
		mu.Unlock()
	}))
	if err := r.m.WaitBootUp(ctx, 5, since); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "boot-up event", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 5 })
	if n.State() != canopen.StatePreOperational {
		t.Fatalf("state %s after boot-up", n.State())
	}
	c, err := r.m.SDO(5)
	if err != nil {
		t.Fatal(err)
	}
	if dt, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err != nil || uint32(dt) != nettest.DeviceType {
		t.Fatalf("device type %x, %v", dt, err)
	}

	// Stopped: the SDO server is silent.
	if err := r.m.StopNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateStopped)
	if _, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err == nil {
		t.Fatal("SDO answered in Stopped")
	}
	if err := r.m.PreOperational(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StatePreOperational)
	if _, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err != nil {
		t.Fatalf("SDO in Pre-operational: %v", err)
	}
	if err := r.m.StartNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateOperational)
	if _, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err != nil {
		t.Fatalf("SDO in Operational: %v", err)
	}
	// Initialising→Pre-op, →Stopped, →Pre-op, →Operational. The state is
	// visible before the state handler has run: wait for the handler.
	nettest.Eventually(t, 5*time.Second, "4 state changes", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(changes) == 4
	})
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	got := len(changes)
	mu.Unlock()
	if got != 4 {
		t.Fatalf("%d state changes, want 4", got)
	}
	st := n.Stats()
	if st.NMT.Commands != 3 || st.SDO.Uploads < 3 || st.State != canopen.StateOperational || st.NodeID != 5 {
		t.Fatalf("stats %+v", st)
	}
	if _, err := json.Marshal(st); err != nil {
		t.Fatal(err)
	}
}

// tpdoConfig maps one object into an event-driven TPDO.
func eventPDO(cob canbus.ID, tt pdo.TransmissionType, entries ...pdo.Entry) pdo.Config {
	return pdo.Config{COBID: canopen.NewCOBID(cob), TransmissionType: tt, Mapping: entries}
}

// listen collects the first value of the PDOs of a TPDO.
type pdoLog struct {
	mu   sync.Mutex
	vals []any
}

func (p *pdoLog) handler(_ *canbus.Frame, values []pdo.Value) {
	p.mu.Lock()
	p.vals = append(p.vals, values[0].Value)
	p.mu.Unlock()
}

func (p *pdoLog) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.vals)
}

func (p *pdoLog) last() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.vals) == 0 {
		return nil
	}
	return p.vals[len(p.vals)-1]
}

func TestPDOsOnlyOperational(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	if err := r.m.ConfigurePDO(ctx, 5, true, 1, eventPDO(0x185, 255, pdo.Entry{Index: nettest.IdxIn32, Bits: 32})); err != nil {
		t.Fatal(err)
	}
	if err := r.m.ConfigurePDO(ctx, 5, false, 1, eventPDO(0x205, 255, pdo.Entry{Index: nettest.IdxOut16, Bits: 16})); err != nil {
		t.Fatal(err)
	}
	var log pdoLog
	if _, err := r.m.ListenTPDO(ctx, 5, 1, d, log.handler); err != nil {
		t.Fatal(err)
	}
	snd, err := r.m.RPDOSender(ctx, 5, 1, d)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-operational: no PDO either way.
	if err := d.SetValue(nettest.IdxIn32, 0, int32(7)); err != nil {
		t.Fatal(err)
	}
	if err := snd.Send(ctx, uint16(0xBEEF)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if log.len() != 0 {
		t.Fatalf("TPDO sent in Pre-operational: %v", log.vals)
	}
	if v, _ := d.GetUint(nettest.IdxOut16, 0); v != 0 {
		t.Fatalf("RPDO applied in Pre-operational: %x", v)
	}
	if n.TPDO(1) == nil || n.RPDO(1) == nil || n.TPDO(99) != nil || n.RPDO(99) != nil {
		t.Fatal("PDO accessors")
	}

	if err := r.m.StartNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// Enabling sends the current value.
	nettest.Eventually(t, 2*time.Second, "TPDO after start", func() bool { return log.last() == int64(7) })
	if err := d.SetValue(nettest.IdxIn32, 0, int32(-42)); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "TPDO of a change", func() bool { return log.last() == int64(-42) })
	if err := snd.Send(ctx, uint16(0xBEEF)); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "RPDO applied", func() bool {
		v, _ := d.GetUint(nettest.IdxOut16, 0)
		return v == 0xBEEF
	})

	// Stopped: silent again.
	if err := r.m.StopNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateStopped)
	before := log.len()
	_ = d.SetValue(nettest.IdxIn32, 0, int32(99))
	_ = snd.Send(ctx, uint16(1))
	time.Sleep(100 * time.Millisecond)
	if log.len() != before {
		t.Fatal("TPDO sent in Stopped")
	}
	if v, _ := d.GetUint(nettest.IdxOut16, 0); v != 0xBEEF {
		t.Fatalf("RPDO applied in Stopped: %x", v)
	}
	if st := n.Stats(); st.TPDO[1].Sent < 2 || st.RPDO[1].Applied != 1 {
		t.Fatalf("PDO stats %+v %+v", st.TPDO[1], st.RPDO[1])
	}
}

func TestHeartbeatLossAndResume(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	dA := nettest.Dict(t, 5, nil)
	if err := dA.SetUint(od.IndexConsumerHeartbeat, 1, uint64(nmt.ConsumerEntry(6, 150*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	if err := dA.SetUint(node.IndexErrorBehaviour, 1, uint64(node.ErrorBehaviourPreOperational)); err != nil {
		t.Fatal(err)
	}
	dB := nettest.Dict(t, 6, func(c *od.Config) { c.HeartbeatMs = 30 })
	var hbEvents atomic.Int32
	b := startNode(t, r.buses[2], dB)
	a := startNode(t, r.buses[1], dA, node.WithHeartbeatHandler(func(nmt.HeartbeatEvent) { hbEvents.Add(1) }))
	if err := r.m.StartNode(ctx, 0); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, canopen.StateOperational)
	waitState(t, b, canopen.StateOperational)
	nettest.Eventually(t, 2*time.Second, "supervision active", func() bool { return a.HeartbeatConsumer().Status(6).Active })

	_ = b.Close()
	ev := r.rec.wait(t, "EMCY 0x8130", emcyFrom(5, emcy.CodeHeartbeat))
	if ev.EMCY.MSEF[0] != 6 || ev.EMCY.Register&emcy.RegCommunication == 0 {
		t.Fatalf("emergency %s", ev.EMCY.Message)
	}
	waitState(t, a, canopen.StatePreOperational) // 0x1029 = 0
	if reg, _ := dA.GetUint(od.IndexErrorRegister, 0); reg&uint64(emcy.RegCommunication) == 0 {
		t.Fatalf("error register %x", reg)
	}

	// B comes back: the error is cleared.
	startNode(t, r.buses[2], dB)
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))
	if reg, _ := dA.GetUint(od.IndexErrorRegister, 0); reg != 0 {
		t.Fatalf("error register %x after resumption", reg)
	}
	if hist, _ := dA.GetUint(od.IndexErrorField, 1); uint16(hist) != emcy.CodeHeartbeat {
		t.Fatalf("history %x", hist)
	}
	if hbEvents.Load() == 0 {
		t.Fatal("heartbeat handler not called")
	}
}

func TestErrorBehaviourStopped(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	d := nettest.Dict(t, 5, nil)
	_ = d.SetUint(node.IndexErrorBehaviour, 1, uint64(node.ErrorBehaviourStopped))
	_ = d.SetUint(od.IndexGuardTime, 0, 40)
	_ = d.SetUint(od.IndexLifeTimeFactor, 0, 3)
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	gm := nmt.NewGuardMaster(r.buses[0])
	if err := gm.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gm.Close() }()
	if err := gm.Guard(5, 40*time.Millisecond, 3); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "guarded", func() bool { return gm.State(5) == canopen.StatePreOperational })
	_ = gm.Guard(5, 0, 0)
	ev := r.rec.wait(t, "life guarding EMCY", emcyFrom(5, emcy.CodeHeartbeat))
	if ev.EMCY.MSEF[0] != 0 {
		t.Fatalf("msef %x", ev.EMCY.MSEF)
	}
	waitState(t, n, canopen.StateStopped)
	// Guarding again resolves the error, but in Stopped the error reset
	// message is not sent.
	_ = gm.Guard(5, 40*time.Millisecond, 3)
	nettest.Eventually(t, 2*time.Second, "error cleared", func() bool {
		reg, _ := d.GetUint(od.IndexErrorRegister, 0)
		return reg == 0
	})
	if n.Stats().Suppressed == 0 {
		t.Fatal("error reset not suppressed in Stopped")
	}
	if _, ok := r.rec.find(emcyFrom(5, emcy.CodeNoError)); ok {
		t.Fatal("EMCY sent in Stopped")
	}
}

func TestErrorBehaviourNoChange(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil) // 0x1029 sub 1 = 1
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(nmt.ConsumerEntry(9, 60*time.Millisecond)))
	n := startNode(t, r.buses[1], d)
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
	// One heartbeat of node 9, sent by hand, activates the supervision;
	// then nothing more comes.
	f := nmt.ErrorControlFrame(9, canopen.StateOperational, false)
	if err := r.buses[0].Send(ctx, &f); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY 0x8130", emcyFrom(5, emcy.CodeHeartbeat))
	time.Sleep(20 * time.Millisecond)
	if n.State() != canopen.StateOperational {
		t.Fatalf("state %s with error behaviour 1", n.State())
	}
}

func TestSYNCDrivenPDOs(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	if err := r.m.ConfigurePDO(ctx, 5, true, 1, eventPDO(0x185, 1, pdo.Entry{Index: nettest.IdxIn8, Bits: 8})); err != nil {
		t.Fatal(err)
	}
	if err := r.m.ConfigurePDO(ctx, 5, false, 1, eventPDO(0x205, 0, pdo.Entry{Index: nettest.IdxOut8, Bits: 8})); err != nil {
		t.Fatal(err)
	}
	var log pdoLog
	if _, err := r.m.ListenTPDO(ctx, 5, 1, d, log.handler); err != nil {
		t.Fatal(err)
	}
	snd, err := r.m.RPDOSender(ctx, 5, 1, d)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
	time.Sleep(30 * time.Millisecond)
	if log.len() != 0 {
		t.Fatal("synchronous TPDO without SYNC")
	}
	_ = d.SetValue(nettest.IdxIn8, 0, uint8(3))
	for i := 0; i < 3; i++ {
		if err := r.m.SYNC().Send(ctx); err != nil {
			t.Fatal(err)
		}
		want := i + 1
		nettest.Eventually(t, 2*time.Second, "TPDO per SYNC", func() bool { return log.len() == want })
	}
	if log.last() != uint64(3) {
		t.Fatalf("value %v", log.last())
	}
	// The RPDO waits for the SYNC.
	_ = snd.Send(ctx, uint8(77))
	nettest.Eventually(t, 2*time.Second, "RPDO received", func() bool { return n.Stats().RPDO[1].Received == 1 })
	if v, _ := d.GetUint(nettest.IdxOut8, 0); v != 0 {
		t.Fatal("synchronous RPDO applied before SYNC")
	}
	_ = r.m.SYNC().Send(ctx)
	nettest.Eventually(t, 2*time.Second, "RPDO applied on SYNC", func() bool {
		v, _ := d.GetUint(nettest.IdxOut8, 0)
		return v == 77
	})
	if st := n.Stats(); st.SYNC.Received != 4 {
		t.Fatalf("SYNC stats %+v", st.SYNC)
	}
}

func TestOwnSYNCProducer(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	_ = d.SetUint(od.IndexCOBIDSync, 0, uint64(canopen.COBGenerate|0x80))
	_ = d.SetUint(od.IndexCyclePeriod, 0, 10000) // 10 ms
	_ = d.SetUint(od.IndexSyncOverflow, 0, 4)    // SYNC with a counter
	n := startNode(t, r.buses[1], d, node.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	if err := r.m.ConfigurePDO(ctx, 5, true, 1, eventPDO(0x185, 1, pdo.Entry{Index: nettest.IdxIn8, Bits: 8})); err != nil {
		t.Fatal(err)
	}
	var log pdoLog
	if _, err := r.m.ListenTPDO(ctx, 5, 1, d, log.handler); err != nil {
		t.Fatal(err)
	}
	if n.SYNCProducer() == nil || !n.SYNCProducer().Producing() {
		t.Fatal("SYNC producer not producing")
	}
	_ = r.m.StartNode(ctx, 5)
	nettest.Eventually(t, 3*time.Second, "TPDOs on own SYNC", func() bool { return log.len() >= 3 })
	_ = r.m.StopNode(ctx, 5)
	waitState(t, n, canopen.StateStopped)
	s0 := n.Stats().Suppressed
	nettest.Eventually(t, 2*time.Second, "SYNC suppressed in Stopped", func() bool { return n.Stats().Suppressed > s0+2 })
}

func TestResetCommunicationNewNodeID(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 })
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 5 })
	c5, _ := r.m.SDO(5)
	if err := c5.WriteUint32(ctx, nettest.IdxParam, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := n.SetNodeID(10); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	if err := r.m.ResetCommunication(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := r.m.WaitBootUp(ctx, 10, since); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StatePreOperational)
	if n.NodeID() != 10 || d.NodeID() != 10 {
		t.Fatalf("node-ID %d / %d", n.NodeID(), d.NodeID())
	}
	c10, _ := r.m.SDO(10)
	if sn, err := c10.ReadUint(ctx, od.IndexIdentity, 4); err != nil || sn != 5 {
		t.Fatalf("serial %d, %v", sn, err)
	}
	if cob, err := c10.ReadUint(ctx, pdo.CommIndex(true, 1), 1); err != nil || canopen.COBID(cob).ID() != 0x18A {
		t.Fatalf("TPDO 1 COB-ID %x, %v", cob, err)
	}
	if cob, _ := d.GetUint(od.IndexCOBIDEmcy, 0); cob != 0x8A {
		t.Fatalf("EMCY COB-ID %x", cob)
	}
	if _, err := c5.ReadUint(ctx, od.IndexDeviceType, 0); err == nil {
		t.Fatal("old node-ID still answers")
	}
	// The application area survives a reset communication...
	if v, _ := c10.ReadUint(ctx, nettest.IdxParam, 0); v != 1 {
		t.Fatalf("param %x after reset communication", v)
	}
	// ...and a raised error does not.
	_ = n.EMCY().Raise(ctx, emcy.CodeGeneric, 0, [5]byte{})
	r.rec.wait(t, "EMCY from node 10", emcyFrom(10, emcy.CodeGeneric))
	nettest.Eventually(t, 2*time.Second, "heartbeat of node 10", func() bool { return r.m.Status(10).LastSeen.After(since) })

	since = time.Now()
	if err := r.m.ResetNode(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := r.m.WaitBootUp(ctx, 10, since); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StatePreOperational)
	if v, err := c10.ReadUint(ctx, nettest.IdxParam, 0); err != nil || v != 0x12345678 {
		t.Fatalf("param %x after reset node, %v", v, err)
	}
	if reg, _ := d.GetUint(od.IndexErrorRegister, 0); reg != 0 || len(n.EMCY().Active()) != 0 || len(n.EMCY().History()) != 0 {
		t.Fatalf("error state survives the reset: register %x, active %v", reg, n.EMCY().Active())
	}
	if st := n.Stats(); st.Resets != 2 {
		t.Fatalf("resets %d", st.Resets)
	}
}

func TestRPDOErrors(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	cfg := eventPDO(0x205, 255, pdo.Entry{Index: nettest.IdxOut16, Bits: 16})
	if err := r.m.ConfigurePDO(ctx, 5, false, 1, cfg); err != nil {
		t.Fatal(err)
	}
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
	send := func(data ...byte) {
		t.Helper()
		f, _ := canbus.NewStandard(0x205, data)
		if err := r.buses[0].Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	send(1)
	ev := r.rec.wait(t, "EMCY 0x8210", emcyFrom(5, emcy.CodePDOLength))
	if ev.EMCY.MSEF[0] != 1 {
		t.Fatalf("msef %x", ev.EMCY.MSEF)
	}
	send(1, 2)
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))
	r.rec.reset()
	send(1, 2, 3)
	r.rec.wait(t, "EMCY 0x8220", emcyFrom(5, emcy.CodePDOLengthExceeded))
	// Leaving Operational clears the PDO errors.
	_ = r.m.PreOperational(ctx, 5)
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))

	// Deadline monitoring.
	r.rec.reset()
	cfg.EventTimer = 40
	if err := r.m.ConfigurePDO(ctx, 5, false, 1, cfg); err != nil {
		t.Fatal(err)
	}
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
	send(1, 2)
	r.rec.wait(t, "EMCY 0x8250", emcyFrom(5, emcy.CodeRPDOTimeout))
	send(1, 2)
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))
}

func TestSYNCLengthError(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	_ = d.SetUint(od.IndexSyncOverflow, 0, 4)
	startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	if err := r.m.SYNC().Send(ctx); err != nil { // no counter: wrong length
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY 0x8240", emcyFrom(5, emcy.CodeSYNCLength))
	f, _ := canbus.NewStandard(0x80, []byte{1})
	_ = r.buses[0].Send(ctx, &f)
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))
}

func TestTIME(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	dA := nettest.Dict(t, 5, nil)
	_ = dA.SetUint(od.IndexCOBIDTime, 0, uint64(canopen.COBConsume|0x100))
	dB := nettest.Dict(t, 6, nil)
	_ = dB.SetUint(od.IndexCOBIDTime, 0, uint64(canopen.COBGenerate|0x100))
	got := make(chan time.Time, 4)
	startNode(t, r.buses[1], dA, node.WithTimeHandler(func(e timestamp.Event) {
		select {
		case got <- e.Time:
		default:
		}
	}))
	b := startNode(t, r.buses[2], dB, node.WithSDOServerOptions(sdo.WithTimeout(time.Second)))
	r.rec.wait(t, "boot-ups", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 6 })
	r.rec.wait(t, "boot-ups", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 5 })
	want := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	time.Sleep(10 * time.Millisecond)
	if err := r.m.TIME().Send(ctx, want); err != nil {
		t.Fatal(err)
	}
	if g := <-got; !g.Equal(want) {
		t.Fatalf("time %v", g)
	}
	if err := b.SendTime(ctx, want.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g := <-got; !g.Equal(want.Add(time.Hour)) {
		t.Fatalf("time %v", g)
	}
	// Periodic TIME from a third node, while it is not stopped.
	dD := nettest.Dict(t, 8, nil)
	_ = dD.SetUint(od.IndexCOBIDTime, 0, uint64(canopen.COBGenerate|0x100))
	startNode(t, r.buses[0], dD, node.WithTimePeriod(10*time.Millisecond))
	for i := 0; i < 3; i++ {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("no periodic TIME")
		}
	}
	dC := nettest.Dict(t, 7, func(c *od.Config) { c.Time = false })
	nC, err := node.New(r.buses[0], dC)
	if err != nil {
		t.Fatal(err)
	}
	if err := nC.SendTime(ctx, want); !errors.Is(err, node.ErrNoObject) {
		t.Fatalf("SendTime without 0x1012: %v", err)
	}
	_ = nC.Close()
}

func TestAutoStartAndLocalControl(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d, node.WithAutoStart())
	waitState(t, n, canopen.StateOperational)
	if err := n.SetState(canopen.StateStopped); err != nil {
		t.Fatal(err)
	}
	if n.State() != canopen.StateStopped {
		t.Fatal("SetState")
	}
	if err := n.Reset(ctx, nmt.ResetCommunication); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StateOperational)
	if n.Dictionary() != d || n.EMCY() == nil || n.Drive() != nil {
		t.Fatal("accessors")
	}
	if err := n.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Fatalf("second Start: %v", err)
	}
	_ = n.Close()
	_ = n.Close()
	if err := n.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatalf("Start after Close: %v", err)
	}
	if err := n.Reset(ctx, nmt.ResetApplication); err == nil {
		t.Fatal("Reset after Close")
	}
}

func TestNewErrors(t *testing.T) {
	buses := testbus.Ports(t, 1)
	if _, err := node.New(nil, od.New(5)); err == nil {
		t.Fatal("nil bus")
	}
	if _, err := node.New(buses[0], nil); err == nil {
		t.Fatal("nil dictionary")
	}
	if _, err := node.New(buses[0], od.New(0)); err == nil {
		t.Fatal("node-ID 0")
	}
	// A dictionary without SDO server object and with an invalid one.
	d := od.New(5)
	_ = d.Add(od.NewRecord(od.IndexSDOServer, "SDO server parameter",
		od.Sub(1, "rx", canopen.Unsigned32, od.AccessRO, uint32(canopen.COBInvalid)),
		od.Sub(2, "tx", canopen.Unsigned32, od.AccessRO, uint32(canopen.COBInvalid))))
	n, err := node.New(buses[0], d)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err == nil {
		t.Fatal("Start with invalid SDO COB-IDs")
	}
	if err := n.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatalf("Start after failure: %v", err)
	}
	// Invalid PDO configuration: mapping object without communication.
	d2 := od.New(5)
	comm, mapping := od.PDOObjects(true, 1, 8)
	_ = d2.Add(comm)
	_ = d2.Add(mapping)
	_ = d2.Add(od.NewVar(od.IndexCOBIDSync, "COB-ID SYNC", canopen.Unsigned32, od.AccessRW, uint32(0x80)))
	n2, err := node.New(buses[0], d2)
	if err != nil {
		t.Fatal(err)
	}
	if err := n2.Start(); err != nil {
		t.Fatal(err)
	}
	if n2.TPDO(1) == nil || n2.HeartbeatConsumer() != nil || n2.SYNCProducer() != nil {
		t.Fatal("services of a minimal dictionary")
	}
	_ = n2.Close()
}

func TestMinimalDictionary(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := od.New(9)
	_ = d.Add(od.NewVar(od.IndexDeviceType, "Device type", canopen.Unsigned32, od.AccessRO, uint32(0x191)))
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 9 })
	c, _ := r.m.SDO(9)
	if v, err := c.ReadUint(ctx, od.IndexDeviceType, 0); err != nil || v != 0x191 {
		t.Fatalf("device type %x, %v", v, err)
	}
	if err := n.EMCY().Raise(ctx, emcy.CodeGeneric, 0, [5]byte{}); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY on the default COB-ID", emcyFrom(9, emcy.CodeGeneric))
	if st := n.Stats(); st.Heartbeat.Sent != 0 || len(st.TPDO) != 0 {
		t.Fatalf("stats %+v", st)
	}
}
