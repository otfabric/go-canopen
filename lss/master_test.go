// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

func TestMasterConfigureNodeID(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0])
	var reset canopen.NodeID
	other := testAddr
	other.Serial++
	s := startSlave(t, ports[1], testAddr, Unconfigured, WithResetHandler(func(n canopen.NodeID) { reset = n }))
	s2 := startSlave(t, ports[2], other, Unconfigured)

	if err := m.ConfigureNodeID(ctx, testAddr, 33); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return s.NodeID() == 33 })
	if reset != 33 || s.State() != Waiting || s2.NodeID() != Unconfigured || s2.State() != Waiting {
		t.Fatalf("reset %d, state %v, other %d %v", reset, s.State(), s2.NodeID(), s2.State())
	}
	// Unknown address: no slave answers the selection.
	missing := testAddr
	missing.Vendor++
	if err := m.ConfigureNodeID(ctx, missing, 34); !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("%v", err)
	}
	if err := m.ConfigureNodeID(ctx, testAddr, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	st := m.Stats()
	if st.Selections != 2 || st.Timeouts != 1 || st.Retries != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestMasterServices(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0], WithInhibit(time.Millisecond), WithLogger(nil))
	var stored Settings
	var delay time.Duration
	activated := make(chan struct{}, 1)
	s := startSlave(t, ports[1], testAddr, 5,
		WithBitTimingHandler(func(sel uint8, b BitTiming) error {
			if b == BitTiming800 {
				return &ConfigError{Code: CodeImplementSpec, Spec: 1}
			}
			return nil
		}),
		WithStoreHandler(func(st Settings) error { stored = st; return nil }),
		WithActivateHandler(func(_ Settings, d time.Duration) { delay = d; activated <- struct{}{} }))

	// Nothing answers in the waiting state.
	if _, err := m.InquireNodeID(ctx); !errors.Is(err, canopen.ErrTimeout) {
		t.Fatal(err)
	}
	if err := m.SwitchStateSelective(ctx, testAddr); err != nil {
		t.Fatal(err)
	}
	a, err := m.InquireAddress(ctx)
	if err != nil || a != testAddr {
		t.Fatalf("%v %v", a, err)
	}
	if n, err := m.InquireNodeID(ctx); err != nil || n != 5 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := m.Inquire(ctx, Part(4)); err == nil {
		t.Fatal("invalid part accepted")
	}
	if err := m.SetNodeID(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if err := m.SetBitTiming(ctx, BitTiming250); err != nil {
		t.Fatal(err)
	}
	var ce *ConfigError
	if err := m.SetBitTiming(ctx, BitTiming800); !errors.As(err, &ce) || ce.Code != CodeImplementSpec || ce.Spec != 1 {
		t.Fatalf("%v", err)
	}
	if err := m.SetBitTimingTable(ctx, 0, 42); !errors.As(err, &ce) || ce.Code != CodeOutOfRange {
		t.Fatalf("%v", err)
	}
	if err := m.Store(ctx); err != nil {
		t.Fatal(err)
	}
	if stored != (Settings{NodeID: 6, BitTimingSet: true, BitTiming: BitTiming250}) {
		t.Fatalf("%+v", stored)
	}
	if err := m.ActivateBitTiming(ctx, 70000*time.Millisecond); err == nil {
		t.Fatal("delay out of range accepted")
	}
	if err := m.ActivateBitTiming(ctx, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("not activated")
	}
	if delay != 300*time.Millisecond {
		t.Fatal(delay)
	}
	if err := m.SwitchStateGlobal(ctx, State(3)); err == nil {
		t.Fatal("invalid state accepted")
	}
	if err := m.SwitchStateGlobal(ctx, Waiting); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return s.State() == Waiting })
	if s.NodeID() != 5 || s.Pending().NodeID != 6 {
		t.Fatal("configured node changed its active node-ID")
	}
	if m.Stats().Refusals != 2 || m.Stats().Activations != 1 {
		t.Fatalf("%+v", m.Stats())
	}
	// Configure: fn errors are returned and the slave is released.
	boom := errors.New("boom")
	if err := m.Configure(ctx, testAddr, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return s.State() == Waiting })
}

func TestMasterIdentify(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0], WithRetries(0))
	startSlave(t, ports[1], testAddr, 3)
	lo, hi := testAddr, testAddr
	lo.Serial, hi.Serial = 0, 0xFFFFFFFF
	if ok, err := m.Identify(ctx, lo, hi); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	lo.Serial = testAddr.Serial + 1
	if ok, err := m.Identify(ctx, lo, hi); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := m.Identify(ctx, hi, lo); err == nil {
		t.Fatal("invalid range accepted")
	}
	bad := hi
	bad.Vendor++
	if _, err := m.Identify(ctx, lo, bad); err == nil {
		t.Fatal("vendor range accepted")
	}
	if ok, err := m.IdentifyNonConfigured(ctx); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	other := testAddr
	other.Serial = 1
	startSlave(t, ports[2], other, Unconfigured)
	if ok, err := m.IdentifyNonConfigured(ctx); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if m.Stats().Identifies != 2 {
		t.Fatalf("%+v", m.Stats())
	}
}

func TestMasterLifecycle(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	a, _ := testbus.Pair(t)
	m := NewMaster(a, nil, WithTimeout(0), WithFastscanTimeout(0), WithRetries(-1), WithFastscanRetries(-1))
	if m.cfg.timeout != DefaultTimeout || m.cfg.fsTimeout != DefaultFastscanTimeout || m.cfg.retries != 0 {
		t.Fatalf("%+v", m.cfg)
	}
	if err := m.SwitchStateGlobal(ctx, Waiting); !errors.Is(err, canopen.ErrClosed) {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Fatal(err)
	}
	_ = m.Close()
	_ = m.Close()
	if err := m.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatal(err)
	}
	calls := []func() error{
		func() error { return m.SwitchStateGlobal(ctx, Waiting) },
		func() error { return m.SwitchStateSelective(ctx, testAddr) },
		func() error { return m.SetNodeID(ctx, 1) },
		func() error { _, err := m.Inquire(ctx, PartVendor); return err },
		func() error { _, err := m.InquireAddress(ctx); return err },
		func() error { _, err := m.InquireNodeID(ctx); return err },
		func() error { _, err := m.Identify(ctx, testAddr, testAddr); return err },
		func() error { return m.ActivateBitTiming(ctx, 0) },
		func() error { _, err := m.Fastscan(ctx, FastscanOptions{}); return err },
		func() error { _, _, err := m.FastscanSelect(ctx, FastscanOptions{}); return err },
		func() error { _, err := m.AssignAll(ctx, 1); return err },
		func() error { return m.ConfigureNodeID(ctx, testAddr, 1) },
	}
	for i, c := range calls {
		if err := c(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("call %d: %v", i, err)
		}
	}
	b := &failBus{Bus: a, subErr: true}
	if err := NewMaster(b).Start(); err == nil {
		t.Fatal("subscribe error ignored")
	}
}

func TestMasterCloseDuringService(t *testing.T) {
	checkLeaks(t)
	a, _ := testbus.Pair(t)
	m := NewMaster(a, WithTimeout(5*time.Second))
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(20 * time.Millisecond); _ = m.Close() }()
	if _, err := m.InquireNodeID(context.Background()); !errors.Is(err, canopen.ErrClosed) {
		t.Fatal(err)
	}
}

func TestMasterContext(t *testing.T) {
	checkLeaks(t)
	a, _ := testbus.Pair(t)
	m := startMaster(t, a, WithTimeout(5*time.Second), WithInhibit(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.InquireNodeID(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// The inhibit time is honoured against the context too.
	if err := m.SwitchStateGlobal(ctx, Waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestMasterSendError(t *testing.T) {
	a, _ := testbus.Pair(t)
	m := startMaster(t, &failBus{Bus: a})
	ctx := ctxT(t)
	if err := m.SwitchStateGlobal(ctx, Waiting); err == nil {
		t.Fatal("send error ignored")
	}
	if _, err := m.InquireNodeID(ctx); err == nil {
		t.Fatal("send error ignored")
	}
	if _, err := m.Fastscan(ctx, FastscanOptions{}); err == nil {
		t.Fatal("send error ignored")
	}
	if _, _, err := m.FastscanSelect(ctx, FastscanOptions{}); err == nil {
		t.Fatal("send error ignored")
	}
	if _, err := m.AssignAll(ctx, 1); err == nil {
		t.Fatal("send error ignored")
	}
	if err := m.ActivateBitTiming(ctx, 0); err == nil {
		t.Fatal("send error ignored")
	}
	if m.Stats().SendErrors == 0 {
		t.Fatal("not counted")
	}
}

func TestMasterUnexpectedFrames(t *testing.T) {
	checkLeaks(t)
	ctx := ctxT(t)
	a, b := testbus.Pair(t)
	m := startMaster(t, a, WithRetries(0), WithTimeout(50*time.Millisecond))
	// A responder that answers every request with a wrong frame first.
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, err := sub.Receive(ctx)
			if err != nil {
				return
			}
			wrong := frame(SlaveID, CmdStore)
			wrong.Len = 1
			short := frame(SlaveID, Command(f.Data[0]))
			short.Len = 1
			right := frame(SlaveID, Command(f.Data[0]), 0x7F)
			for _, r := range []canbus.Frame{wrong, short, right} {
				_ = b.Send(ctx, &r)
			}
		}
	}()
	defer func() { _ = sub.Close(); <-done }()
	n, err := m.InquireNodeID(ctx)
	if err != nil || n != 0x7F {
		t.Fatalf("%d %v", n, err)
	}
	if m.Stats().Unexpected < 2 {
		t.Fatalf("%+v", m.Stats())
	}
}

func TestMatchResponse(t *testing.T) {
	tests := []struct {
		f    canbus.Frame
		want Command
		ok   bool
	}{
		{frame(SlaveID, CmdSelectResponse), CmdSelectResponse, true},
		{frame(MasterID, CmdSelectResponse), CmdSelectResponse, false},
		{frame(SlaveID, CmdInquireVendor), CmdInquireNodeID, false},
		{canbus.Frame{ID: SlaveID, Len: 4, Data: [64]byte{0x5A}}, CmdInquireVendor, false},
		{canbus.Frame{ID: SlaveID, Len: 5, Data: [64]byte{0x5A}}, CmdInquireVendor, true},
		{canbus.Frame{ID: SlaveID, Len: 1, Data: [64]byte{0x11}}, CmdConfigureNodeID, false},
		{canbus.Frame{ID: SlaveID, Len: 2, Data: [64]byte{0x11}}, CmdConfigureNodeID, true},
		{canbus.Frame{ID: SlaveID, Len: 0}, CmdSelectResponse, false},
		{canbus.Frame{ID: SlaveID, Len: 8, Flags: canbus.FlagRemote, Data: [64]byte{0x44}}, CmdSelectResponse, false},
	}
	for i, tt := range tests {
		if got := matchResponse(&tt.f, tt.want); got != tt.ok {
			t.Errorf("%d: %v", i, got)
		}
	}
}
