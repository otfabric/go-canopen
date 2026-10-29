// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func constState(s canopen.NMTState) func() canopen.NMTState {
	return func() canopen.NMTState { return s }
}

func isHeartbeat(node canopen.NodeID) func(*canbus.Frame) bool {
	return func(f *canbus.Frame) bool { return f.ID == 0x700+canbus.ID(node) && f.Len == 1 }
}

func TestHeartbeatProducer(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	var st atomic.Uint32
	st.Store(uint32(canopen.StateOperational))
	p, err := NewHeartbeatProducer(a, 3, func() canopen.NMTState { return canopen.NMTState(st.Load()) }, WithHeartbeatPeriod(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		f := recv(t, sub, nil)
		if f.ID != 0x703 || f.Len != 1 || f.Data[0] != 0x05 {
			t.Fatalf("frame %v", f)
		}
	}
	if el := time.Since(t0); el < 25*time.Millisecond {
		t.Errorf("3 heartbeats in %v", el)
	}
	// Initialising: nothing sent.
	st.Store(uint32(canopen.StateInitialising))
	eventually(t, time.Second, func() bool { return p.Stats().Skipped > 0 })
	st.Store(uint32(canopen.StateStopped))
	recv(t, sub, func(f *canbus.Frame) bool { return f.Data[0] == 0x04 })
	if err := p.SetNodeID(4); err != nil {
		t.Fatal(err)
	}
	if err := p.SetNodeID(0); err == nil {
		t.Error("node 0 accepted")
	}
	recv(t, sub, isHeartbeat(4))
	// Period 0 stops, a new period restarts.
	p.SetPeriod(-1)
	if p.Period() != 0 {
		t.Fatal(p.Period())
	}
	time.Sleep(20 * time.Millisecond)
	n := p.Stats().Sent
	time.Sleep(40 * time.Millisecond)
	if p.Stats().Sent != n {
		t.Error("heartbeat with period 0")
	}
	p.SetPeriod(5 * time.Millisecond)
	eventually(t, time.Second, func() bool { return p.Stats().Sent > n+2 })
	if err := p.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
}

func TestHeartbeatProducerDictionary(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	d := newDict(t, 3)
	_ = d.SetUint(od.IndexProducerHeartbeat, 0, 0)
	p, err := NewHeartbeatProducer(a, 3, constState(canopen.StatePreOperational), WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	// A remote write of 0x1017 takes effect at once.
	if err := d.Write(od.IndexProducerHeartbeat, 0, []byte{10, 0}); err != nil {
		t.Fatal(err)
	}
	if p.Period() != 10*time.Millisecond {
		t.Fatal(p.Period())
	}
	f := recv(t, sub, nil)
	if f.ID != 0x703 || f.Data[0] != 0x7F {
		t.Fatalf("frame %v", f)
	}
	_ = d.Write(od.IndexProducerHeartbeat, 0, []byte{0, 0})
	if p.Period() != 0 {
		t.Fatal(p.Period())
	}
	_ = p.Close()
	// After Close the watcher is gone.
	_ = d.Write(od.IndexProducerHeartbeat, 0, []byte{10, 0})
	if p.Period() != 0 {
		t.Error("watcher still active after Close")
	}

	empty := od.New(3)
	if _, err := NewHeartbeatProducer(a, 3, constState(0), WithDictionary(empty)); err == nil {
		t.Error("dictionary without 0x1017 accepted")
	}
	if _, err := NewHeartbeatProducer(a, 0, constState(0)); err == nil {
		t.Error("node 0 accepted")
	}
	if _, err := NewHeartbeatProducer(a, 1, nil); err == nil {
		t.Error("nil state accepted")
	}
	if _, err := NewHeartbeatProducer(a, 1, constState(0), WithHeartbeatPeriod(-1)); err == nil {
		t.Error("negative period accepted")
	}
	// A failing send is counted.
	fb := newFailBus(testbus.Ports(t, 1)[0])
	p, _ = NewHeartbeatProducer(fb, 1, constState(canopen.StateOperational), WithHeartbeatPeriod(time.Millisecond))
	_ = p.Start()
	eventually(t, time.Second, func() bool { return p.Stats().Errors > 0 })
	_ = p.Close()
}

func TestHeartbeatConsumer(t *testing.T) {
	ports := testbus.Ports(t, 3)
	ev := newEvents[HeartbeatEvent]()
	c, err := NewHeartbeatConsumer(ports[0], WithHeartbeatHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Monitor(7, 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if s := c.Status(7); s.Active || s.Timeout != 60*time.Millisecond || s.State != canopen.StateUnknown {
		t.Fatalf("status before heartbeat %+v", s)
	}
	// No heartbeat yet: supervision has not started, no timeout.
	ev.none(t, 100*time.Millisecond, func(HeartbeatEvent) bool { return true })

	since := time.Now()
	slave, _ := NewSlave(ports[1], 7)
	_ = slave.Start()
	defer func() { _ = slave.Close() }()
	if err := c.WaitBootUp(ctxT(t), 7, since); err != nil {
		t.Fatal(err)
	}
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventBootUp && e.Node == 7 })
	p, _ := NewHeartbeatProducer(ports[1], 7, slave.State, WithHeartbeatPeriod(10*time.Millisecond))
	_ = p.Start()
	if err := c.WaitState(ctxT(t), 7, canopen.StatePreOperational); err != nil {
		t.Fatal(err)
	}
	e := ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventStateChange })
	if e.Previous != canopen.StateInitialising || e.State != canopen.StatePreOperational {
		t.Errorf("event %+v", e)
	}
	_ = NewMaster(ports[2]).Send(context.Background(), 7, canopen.NMTStart)
	if err := c.WaitState(ctxT(t), 7, canopen.StateOperational); err != nil {
		t.Fatal(err)
	}
	if s := c.Status(7); !s.Active || s.Lost || s.BootUps != 1 || s.LastSeen.IsZero() {
		t.Errorf("status %+v", s)
	}
	// Loss: one timeout event, state Unknown.
	p.SetPeriod(0)
	e = ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout })
	if e.Node != 7 || e.State != canopen.StateUnknown || e.Previous != canopen.StateOperational {
		t.Errorf("timeout %+v", e)
	}
	ev.none(t, 150*time.Millisecond, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout })
	if c.State(7) != canopen.StateUnknown || !c.Status(7).Lost {
		t.Error(c.Status(7))
	}
	// Resumption.
	p.SetPeriod(10 * time.Millisecond)
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventResumed && e.State == canopen.StateOperational })
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventStateChange && e.State == canopen.StateOperational })

	// Reconfiguration: a longer timeout, then no supervision.
	_ = c.Monitor(7, time.Second)
	if c.Status(7).Timeout != time.Second {
		t.Error(c.Status(7))
	}
	_ = c.Monitor(7, 0)
	p.SetPeriod(0)
	ev.none(t, 100*time.Millisecond, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout })
	if err := c.Monitor(0, time.Second); err == nil {
		t.Error("node 0 accepted")
	}
	if err := c.Monitor(1, -time.Second); err == nil {
		t.Error("negative timeout accepted")
	}

	// Unsupervised nodes are tracked too; malformed frames are counted.
	f := ErrorControlFrame(9, canopen.StateStopped, false)
	send(t, ports[2], f)
	f, _ = canbus.NewStandard(0x709, []byte{0x05, 0x00})
	send(t, ports[2], f)
	if err := c.WaitState(ctxT(t), 9, canopen.StateStopped); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return c.Stats().Invalid == 1 })
	nodes := c.Nodes()
	if len(nodes) != 2 || nodes[0].Node != 7 || nodes[1].Node != 9 {
		t.Errorf("nodes %+v", nodes)
	}
	if c.State(100) != canopen.StateUnknown || c.Status(100).State != canopen.StateUnknown {
		t.Error("unknown node")
	}
	if err := c.WaitState(ctxT(t), 100, canopen.StateUnknown); err != nil {
		t.Error(err)
	}
	st := c.Stats()
	if st.BootUps != 1 || st.Timeouts != 1 || st.Received == 0 {
		t.Errorf("stats %+v", st)
	}

	// Waits end with the context or Close.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.WaitState(ctx, 9, canopen.StateOperational); !errors.Is(err, context.DeadlineExceeded) {
		t.Error(err)
	}
	done := make(chan error)
	go func() { done <- c.WaitBootUp(context.Background(), 9, time.Now()) }()
	time.Sleep(10 * time.Millisecond)
	_ = c.Close()
	if err := <-done; !errors.Is(err, canopen.ErrClosed) {
		t.Error(err)
	}
	_ = c.Close()
	if err := c.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Error(err)
	}
}

func TestHeartbeatConsumerDictionary(t *testing.T) {
	ports := testbus.Ports(t, 2)
	d := newDict(t, 1)
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(ConsumerEntry(5, 50*time.Millisecond)))
	ev := newEvents[HeartbeatEvent]()
	c, err := NewHeartbeatConsumer(ports[0], WithDictionary(d), WithHeartbeatHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if c.Status(5).Timeout != 50*time.Millisecond {
		t.Fatal(c.Status(5))
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// Duplicate node-IDs are refused; the same entry may be rewritten.
	le := func(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }
	if err := d.Write(od.IndexConsumerHeartbeat, 2, le(ConsumerEntry(5, 80*time.Millisecond))); !errors.Is(err, canopen.AbortParamIncompatible) {
		t.Errorf("duplicate: %v", err)
	}
	if err := d.Write(od.IndexConsumerHeartbeat, 1, le(ConsumerEntry(5, 80*time.Millisecond))); err != nil {
		t.Error(err)
	}
	if err := d.Write(od.IndexConsumerHeartbeat, 2, le(ConsumerEntry(6, 30*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	if err := d.Write(od.IndexConsumerHeartbeat, 3, le(ConsumerEntry(5, 0))); err != nil {
		t.Errorf("unused entry refused: %v", err)
	}
	if c.Status(5).Timeout != 80*time.Millisecond || c.Status(6).Timeout != 30*time.Millisecond {
		t.Fatal(c.Nodes())
	}
	// Explicit configuration overrides the dictionary.
	_ = c.Monitor(6, 40*time.Millisecond)
	if c.Status(6).Timeout != 40*time.Millisecond {
		t.Fatal(c.Status(6))
	}
	_ = c.Monitor(6, 0)
	if c.Status(6).Timeout != 30*time.Millisecond {
		t.Fatal(c.Status(6))
	}
	p, _ := NewHeartbeatProducer(ports[1], 6, constState(canopen.StateOperational), WithHeartbeatPeriod(5*time.Millisecond))
	_ = p.Start()
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventStateChange && e.Node == 6 })
	// Removing the entry by SDO ends the supervision.
	_ = d.Write(od.IndexConsumerHeartbeat, 2, le(0))
	_ = p.Close()
	ev.none(t, 80*time.Millisecond, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout })

	if _, err := NewHeartbeatConsumer(ports[0], WithDictionary(od.New(1))); err == nil {
		t.Error("dictionary without 0x1016 accepted")
	}
	// Close removes the 0x1016 hook: duplicates are no longer refused.
	hooks := d.HookCount()
	_ = c.Close()
	if d.HookCount() >= hooks {
		t.Errorf("hooks %d after Close, %d before", d.HookCount(), hooks)
	}
	if err := d.Write(od.IndexConsumerHeartbeat, 2, le(ConsumerEntry(5, 80*time.Millisecond))); err != nil {
		t.Errorf("hook still installed: %v", err)
	}
	// The hook ignores sub-index 0 and entries it cannot read.
	if err := checkConsumerEntry(d, 0, 0); err != nil {
		t.Error(err)
	}
}

// TestHeartbeatConsumerChaos supervises a node through a lossy, delaying,
// duplicating and reordering bridge: no false timeout and no false state
// while the heartbeat runs, and the timeout once it stops.
func TestHeartbeatConsumerChaos(t *testing.T) {
	a, b, br := testbus.Chaos(t, chaos.Config{Seed: 7, Drop: 0.2, Duplicate: 0.1, Delay: 0.2, DelayBy: 5 * time.Millisecond, Reorder: 0.1})
	ev := newEvents[HeartbeatEvent]()
	c, _ := NewHeartbeatConsumer(b, WithHeartbeatHandler(ev.add))
	// 25 periods: a false timeout needs that many heartbeats lost or
	// delayed in a row. A tighter timeout (80 ms) gave false timeouts on
	// a loaded machine (the whole module's tests in parallel) when the
	// bridge held a reordered frame back behind a run of dropped ones.
	_ = c.Monitor(2, 250*time.Millisecond)
	_ = c.Start()
	defer func() { _ = c.Close() }()
	p, _ := NewHeartbeatProducer(a, 2, constState(canopen.StateOperational), WithHeartbeatPeriod(10*time.Millisecond))
	_ = p.Start()
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventStateChange })
	ev.none(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind != EventStateChange || e.State != canopen.StateOperational })
	if br.Stats().Dropped == 0 {
		t.Error("the bridge dropped nothing")
	}
	_ = p.Close()
	e := ev.wait(t, time.Second, func(HeartbeatEvent) bool { return true })
	if e.Kind != EventTimeout || e.Node != 2 {
		t.Errorf("event %+v", e)
	}
	// A frame the bridge still holds back is released only with the next
	// one or when the bridge closes: none arrives, so no resumption.
	ev.none(t, 200*time.Millisecond, func(HeartbeatEvent) bool { return true })
}

func TestHeartbeatConsumerRestart(t *testing.T) {
	ports := testbus.Ports(t, 2)
	d := newDict(t, 1)
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(ConsumerEntry(5, 40*time.Millisecond)))
	ev := newEvents[HeartbeatEvent]()
	c, err := NewHeartbeatConsumer(ports[0], WithDictionary(d), WithHeartbeatHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	p, _ := NewHeartbeatProducer(ports[1], 5, constState(canopen.StateOperational), WithHeartbeatPeriod(5*time.Millisecond))
	_ = p.Start()
	defer func() { _ = p.Close() }()
	if err := c.WaitState(ctxT(t), 5, canopen.StateOperational); err != nil {
		t.Fatal(err)
	}
	p.SetPeriod(0)
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout && e.Node == 5 })
	if !c.Status(5).Lost {
		t.Fatal(c.Status(5))
	}
	// A restart forgets the loss and waits for the first heartbeat: no
	// timeout until then.
	_ = d.Set(od.IndexConsumerHeartbeat, 1, le32(ConsumerEntry(5, 30*time.Millisecond)))
	c.Restart()
	if s := c.Status(5); s.Lost || s.Active || s.Timeout != 30*time.Millisecond {
		t.Fatalf("status after restart %+v", s)
	}
	ev.none(t, 100*time.Millisecond, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout || e.Kind == EventResumed })
	p.SetPeriod(5 * time.Millisecond)
	eventually(t, time.Second, func() bool { return c.Status(5).Active })
	p.SetPeriod(0)
	ev.wait(t, time.Second, func(e HeartbeatEvent) bool { return e.Kind == EventTimeout && e.Node == 5 })
	// Without a dictionary Restart only resets the supervision.
	c2, _ := NewHeartbeatConsumer(ports[0])
	_ = c2.Monitor(5, time.Second)
	c2.Restart()
	if s := c2.Status(5); s.Active || s.Timeout != time.Second {
		t.Fatal(s)
	}
	_ = c2.Close()
}

func le32(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }
