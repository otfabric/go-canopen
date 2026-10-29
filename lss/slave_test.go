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

var testAddr = Address{Vendor: 0x29C, Product: 0x1234, Revision: 0x00010002, Serial: 0xCAFEF00D}

// le32 returns v as 4 little-endian bytes.
func le32(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }

func req(cs Command, payload ...byte) canbus.Frame {
	return masterFrame(append([]byte{byte(cs)}, append(payload, make([]byte, 7-len(payload))...)...)...)
}

func req32(cs Command, v uint32, tail ...byte) canbus.Frame {
	return req(cs, append(le32(v), tail...)...)
}

func fsReq(id uint32, bit uint8, sub, next Part) canbus.Frame {
	return req32(CmdFastscan, id, bit, byte(sub), byte(next))
}

func selectFrames(a Address) []canbus.Frame {
	return []canbus.Frame{
		req32(CmdSelectVendor, a.Vendor), req32(CmdSelectProduct, a.Product),
		req32(CmdSelectRevision, a.Revision), req32(CmdSelectSerial, a.Serial),
	}
}

// feed runs frames through the slave and returns the responses.
func feed(s *Slave, fs ...canbus.Frame) []canbus.Frame {
	var out []canbus.Frame
	for i := range fs {
		if r, ok := s.handle(&fs[i]); ok {
			out = append(out, r)
		}
	}
	return out
}

func newTestSlave(t *testing.T, node canopen.NodeID, opts ...SlaveOption) *Slave {
	t.Helper()
	s, err := NewSlave(nil, testAddr, node, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSlaveNodeID(t *testing.T) {
	for _, n := range []canopen.NodeID{0, 128, 0xFE} {
		if _, err := NewSlave(nil, testAddr, n); !errors.Is(err, canopen.ErrInvalidNodeID) {
			t.Errorf("%d: %v", n, err)
		}
	}
	s := newTestSlave(t, 5, nil)
	if s.NodeID() != 5 || s.Pending().NodeID != 5 || s.Address() != testAddr || s.State() != Waiting {
		t.Fatal("initial state")
	}
	if err := s.SetNodeID(200); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	if err := s.SetNodeID(Unconfigured); err != nil || s.NodeID() != Unconfigured {
		t.Fatal(err)
	}
}

func TestSlaveSwitchGlobal(t *testing.T) {
	var states []State
	s := newTestSlave(t, 5, WithStateHandler(func(st State) { states = append(states, st) }))
	feed(s, req(CmdSwitchGlobal, 1))
	if s.State() != Configuration {
		t.Fatal("not configuring")
	}
	feed(s, req(CmdSwitchGlobal, 1), req(CmdSwitchGlobal, 2), masterFrame(byte(CmdSwitchGlobal)))
	if s.State() != Configuration {
		t.Fatal("bad mode changed the state")
	}
	feed(s, req(CmdSwitchGlobal, 0))
	if s.State() != Waiting || len(states) != 2 {
		t.Fatalf("state %v, handler %v", s.State(), states)
	}
	st := s.Stats()
	if st.Received != 5 || st.Ignored != 2 || st.Selected != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestSlaveSelective(t *testing.T) {
	sel := selectFrames(testAddr)
	other := testAddr
	other.Revision++
	tests := []struct {
		name   string
		frames []canbus.Frame
		ok     bool
	}{
		{"in order", sel, true},
		{"duplicates", []canbus.Frame{sel[0], sel[0], sel[1], sel[1], sel[2], sel[3]}, true},
		{"restart", []canbus.Frame{sel[0], sel[1], sel[0], sel[1], sel[2], sel[3]}, true},
		{"wrong revision", selectFrames(other), false},
		{"missing product", []canbus.Frame{sel[0], sel[2], sel[3]}, false},
		{"out of order", []canbus.Frame{sel[0], sel[2], sel[1], sel[3]}, false},
		{"no vendor", sel[1:], false},
		{"short", []canbus.Frame{sel[0], sel[1], sel[2], masterFrame(byte(CmdSelectSerial), 1)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSlave(t, Unconfigured)
			resp := feed(s, tt.frames...)
			if tt.ok != (len(resp) == 1) || (s.State() == Configuration) != tt.ok {
				t.Fatalf("responses %v, state %v", resp, s.State())
			}
			if tt.ok && (resp[0].ID != SlaveID || resp[0].Len != 8 || Command(resp[0].Data[0]) != CmdSelectResponse) {
				t.Fatalf("response %v", resp[0])
			}
			// In the configuration state, selection frames are ignored.
			if tt.ok && len(feed(s, sel...)) != 0 {
				t.Fatal("answered in the wrong state")
			}
		})
	}
}

func TestSlaveIdentify(t *testing.T) {
	id := func(lo, hi Address) []canbus.Frame {
		return []canbus.Frame{
			req32(CmdIdentifyVendor, lo.Vendor), req32(CmdIdentifyProduct, lo.Product),
			req32(CmdIdentifyRevisionLow, lo.Revision), req32(CmdIdentifyRevisionHigh, hi.Revision),
			req32(CmdIdentifySerialLow, lo.Serial), req32(CmdIdentifySerialHigh, hi.Serial),
		}
	}
	lo, hi := testAddr, testAddr
	lo.Revision, hi.Revision, lo.Serial, hi.Serial = 0, 0xFFFFFFFF, 0, 0xFFFFFFFF
	mod := func(f func(*Address, *Address)) []canbus.Frame {
		l, h := lo, hi
		f(&l, &h)
		return id(l, h)
	}
	tests := []struct {
		name   string
		frames []canbus.Frame
		ok     bool
	}{
		{"exact", id(testAddr, testAddr), true},
		{"wide", id(lo, hi), true},
		{"vendor", mod(func(l, h *Address) { l.Vendor++ }), false},
		{"product", mod(func(l, h *Address) { l.Product++ }), false},
		{"revision low", mod(func(l, h *Address) { l.Revision = testAddr.Revision + 1 }), false},
		{"revision high", mod(func(l, h *Address) { h.Revision = testAddr.Revision - 1 }), false},
		{"serial low", mod(func(l, h *Address) { l.Serial = testAddr.Serial + 1 }), false},
		{"serial high", mod(func(l, h *Address) { h.Serial = testAddr.Serial - 1 }), false},
		{"short", append(id(lo, hi)[:5], masterFrame(byte(CmdIdentifySerialHigh))), false},
	}
	for _, tt := range tests {
		for _, st := range []State{Waiting, Configuration} {
			s := newTestSlave(t, 9)
			feed(s, req(CmdSwitchGlobal, byte(st)))
			resp := feed(s, tt.frames...)
			if tt.ok != (len(resp) == 1) {
				t.Errorf("%s in %v: %v", tt.name, st, resp)
			}
			if tt.ok && Command(resp[0].Data[0]) != CmdIdentifySlave {
				t.Errorf("%s: response %v", tt.name, resp[0])
			}
		}
	}
}

func TestSlaveNonConfigured(t *testing.T) {
	s := newTestSlave(t, Unconfigured)
	if r := feed(s, req(CmdIdentifyNonConfig)); len(r) != 1 || Command(r[0].Data[0]) != CmdNonConfigSlave {
		t.Fatalf("%v", r)
	}
	feed(s, req(CmdSwitchGlobal, 1), req(CmdConfigureNodeID, 7))
	if r := feed(s, req(CmdIdentifyNonConfig)); len(r) != 0 {
		t.Fatal("answered with a pending node-ID")
	}
	if r := feed(newTestSlave(t, 3), req(CmdIdentifyNonConfig)); len(r) != 0 {
		t.Fatal("configured slave answered")
	}
}

func TestSlaveFastscan(t *testing.T) {
	a := testAddr
	tests := []struct {
		name   string
		node   canopen.NodeID
		frames []canbus.Frame
		acks   int
		state  State
	}{
		{"confirm", Unconfigured, []canbus.Frame{fsReq(0, 0x80, 0, 0)}, 1, Waiting},
		{"configured node", 4, []canbus.Frame{fsReq(0, 0x80, 0, 0)}, 0, Waiting},
		{"bit 31 match", Unconfigured, []canbus.Frame{fsReq(0, 31, 0, 0)}, 1, Waiting},
		{"bit 0 mismatch", Unconfigured, []canbus.Frame{fsReq(a.Vendor^1, 0, 0, 0)}, 0, Waiting},
		{"high bits mismatch", Unconfigured, []canbus.Frame{fsReq(0x80000000, 31, 0, 0)}, 0, Waiting},
		{"wrong sub", Unconfigured, []canbus.Frame{fsReq(a.Product, 0, 1, 1)}, 0, Waiting},
		{"invalid bitcheck", Unconfigured, []canbus.Frame{fsReq(0, 32, 0, 0)}, 0, Waiting},
		{"invalid sub", Unconfigured, []canbus.Frame{fsReq(0, 0x80, 4, 0)}, 0, Waiting},
		{"invalid next", Unconfigured, []canbus.Frame{fsReq(0, 0x80, 0, 4)}, 0, Waiting},
		{"short", Unconfigured, []canbus.Frame{masterFrame(byte(CmdFastscan), 0, 0, 0, 0, 0x80)}, 0, Waiting},
		{"full selection", Unconfigured, []canbus.Frame{
			fsReq(0, 0x80, 0, 0),
			fsReq(a.Vendor, 0, 0, 1), fsReq(a.Product, 0, 1, 2),
			fsReq(a.Revision, 0, 2, 3), fsReq(a.Serial&0xFFFF0000, 16, 3, 3), fsReq(a.Serial, 0, 3, 0),
		}, 6, Configuration},
		{"next equal sub stays", Unconfigured, []canbus.Frame{
			fsReq(a.Vendor, 0, 0, 1), fsReq(a.Product, 0, 1, 2),
			fsReq(a.Revision, 0, 2, 3), fsReq(a.Serial, 0, 3, 3), fsReq(a.Serial, 0, 3, 3),
		}, 5, Waiting},
		{"reset", Unconfigured, []canbus.Frame{fsReq(a.Vendor, 0, 0, 1), fsReq(0, 0x80, 0, 0), fsReq(a.Product, 0, 1, 2)}, 2, Waiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSlave(t, tt.node)
			r := feed(s, tt.frames...)
			if len(r) != tt.acks || s.State() != tt.state {
				t.Fatalf("acks %d, state %v", len(r), s.State())
			}
			for _, f := range r {
				if Command(f.Data[0]) != CmdIdentifySlave {
					t.Fatalf("response %v", f)
				}
			}
		})
	}
	// Fastscan is ignored in the configuration state.
	s := newTestSlave(t, Unconfigured)
	if len(feed(s, req(CmdSwitchGlobal, 1), fsReq(0, 0x80, 0, 0))) != 0 {
		t.Fatal("fastscan answered in configuration")
	}
}

func TestSlaveConfigure(t *testing.T) {
	errStore := errors.New("flash")
	tests := []struct {
		name  string
		opts  []SlaveOption
		frame canbus.Frame
		want  []byte // response bytes 0..2; nil: none
		check func(*testing.T, *Slave)
	}{
		{"node-ID", nil, req(CmdConfigureNodeID, 42), []byte{0x11, 0, 0}, func(t *testing.T, s *Slave) {
			if s.Pending().NodeID != 42 || s.NodeID() != Unconfigured || s.Stats().NodeIDs != 1 {
				t.Fatal("pending node-ID")
			}
		}},
		{"node-ID unconfigure", nil, req(CmdConfigureNodeID, 0xFF), []byte{0x11, 0, 0}, nil},
		{"node-ID 0", nil, req(CmdConfigureNodeID, 0), []byte{0x11, 1, 0}, nil},
		{"node-ID 128", nil, req(CmdConfigureNodeID, 128), []byte{0x11, 1, 0}, func(t *testing.T, s *Slave) {
			if s.Pending().NodeID != Unconfigured || s.Stats().Refused != 1 {
				t.Fatal("refused node-ID applied")
			}
		}},
		{"node-ID handler refuses", []SlaveOption{WithNodeIDHandler(func(canopen.NodeID) error { return errors.New("no") })},
			req(CmdConfigureNodeID, 4), []byte{0x11, 1, 0}, nil},
		{"node-ID handler code", []SlaveOption{WithNodeIDHandler(func(canopen.NodeID) error {
			return &ConfigError{Code: CodeImplementSpec, Spec: 3}
		})}, req(CmdConfigureNodeID, 4), []byte{0x11, 0xFF, 3}, nil},
		{"node-ID short", nil, masterFrame(byte(CmdConfigureNodeID)), nil, nil},
		{"bit timing unsupported", nil, req(CmdConfigureBitTiming, 0, 2), []byte{0x13, 1, 0}, nil},
		{"bit timing", []SlaveOption{WithBitTimingHandler(func(sel uint8, b BitTiming) error { return nil })},
			req(CmdConfigureBitTiming, 0, 3), []byte{0x13, 0, 0}, func(t *testing.T, s *Slave) {
				if p := s.Pending(); !p.BitTimingSet || p.BitTiming != BitTiming250 || p.Selector != 0 {
					t.Fatalf("%+v", p)
				}
			}},
		{"bit timing index", []SlaveOption{WithBitTimingHandler(func(uint8, BitTiming) error { panic("called") })},
			req(CmdConfigureBitTiming, 0, 10), []byte{0x13, 1, 0}, nil},
		{"bit timing selector", []SlaveOption{WithBitTimingHandler(func(sel uint8, _ BitTiming) error {
			if sel != 0x80 {
				return errors.New("selector")
			}
			return nil
		})}, req(CmdConfigureBitTiming, 0x80, 20), []byte{0x13, 0, 0}, nil},
		{"bit timing refused", []SlaveOption{WithBitTimingHandler(func(uint8, BitTiming) error {
			return &ConfigError{Code: CodeImplementSpec, Spec: 1}
		})}, req(CmdConfigureBitTiming, 0, 0), []byte{0x13, 0xFF, 1}, nil},
		{"bit timing short", nil, masterFrame(byte(CmdConfigureBitTiming), 0), nil, nil},
		{"store unsupported", nil, req(CmdStore), []byte{0x17, 1, 0}, nil},
		{"store", []SlaveOption{WithStoreHandler(func(Settings) error { return nil })}, req(CmdStore), []byte{0x17, 0, 0},
			func(t *testing.T, s *Slave) {
				if s.Stats().Stores != 1 {
					t.Fatal("not counted")
				}
			}},
		{"store fails", []SlaveOption{WithStoreHandler(func(Settings) error { return errStore })}, req(CmdStore), []byte{0x17, 2, 0}, nil},
		{"inquire vendor", nil, req(CmdInquireVendor), append([]byte{0x5A}, le32(testAddr.Vendor)[:2]...), nil},
		{"inquire serial", nil, req(CmdInquireSerial), []byte{0x5D, 0x0D, 0xF0}, nil},
		{"inquire node-ID", nil, req(CmdInquireNodeID), []byte{0x5E, 0xFF, 0}, nil},
		{"activate without handler", nil, req(CmdActivateBitTiming, 10, 0), nil, nil},
		{"activate short", nil, masterFrame(byte(CmdActivateBitTiming), 1), nil, nil},
		{"unknown", nil, req(0x30), nil, nil},
		{"selective in configuration", nil, req32(CmdSelectVendor, testAddr.Vendor), nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSlave(t, Unconfigured, tt.opts...)
			// In the waiting state, configuration services are ignored.
			if r := feed(s, tt.frame); len(r) != 0 {
				t.Fatal("answered in waiting")
			}
			feed(s, req(CmdSwitchGlobal, 1))
			r := feed(s, tt.frame)
			if tt.want == nil {
				if len(r) != 0 {
					t.Fatalf("unexpected response %v", r)
				}
				return
			}
			if len(r) != 1 || r[0].ID != SlaveID || r[0].Len != 8 || string(r[0].Data[:len(tt.want)]) != string(tt.want) {
				t.Fatalf("response %v, want % X", r, tt.want)
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
}

func TestSlaveActivateAndStoreSettings(t *testing.T) {
	var gotDelay time.Duration
	var activated, stored Settings
	s := newTestSlave(t, 7,
		WithBitTimingHandler(func(uint8, BitTiming) error { return nil }),
		WithActivateHandler(func(st Settings, d time.Duration) { activated, gotDelay = st, d }),
		WithStoreHandler(func(st Settings) error { stored = st; return nil }))
	feed(s, req(CmdSwitchGlobal, 1), req(CmdConfigureNodeID, 9), req(CmdConfigureBitTiming, 0, 2),
		req(CmdStore), req(CmdActivateBitTiming, 0x10, 0x01))
	want := Settings{NodeID: 9, BitTimingSet: true, BitTiming: BitTiming500}
	if stored != want || activated != want || gotDelay != 0x110*time.Millisecond {
		t.Fatalf("stored %+v activated %+v delay %v", stored, activated, gotDelay)
	}
	// A configured node keeps its active node-ID until it is reset.
	feed(s, req(CmdSwitchGlobal, 0))
	if s.NodeID() != 7 || s.Pending().NodeID != 9 {
		t.Fatal("active node-ID changed")
	}
}

func TestSlaveResetHandler(t *testing.T) {
	var reset []canopen.NodeID
	s := newTestSlave(t, Unconfigured, WithResetHandler(func(n canopen.NodeID) { reset = append(reset, n) }))
	// Leaving the configuration state without a node-ID does nothing.
	feed(s, req(CmdSwitchGlobal, 1), req(CmdSwitchGlobal, 0))
	if len(reset) != 0 {
		t.Fatal("reset without node-ID")
	}
	feed(s, selectFrames(testAddr)...)
	feed(s, req(CmdConfigureNodeID, 12), req(CmdSwitchGlobal, 0))
	if len(reset) != 1 || reset[0] != 12 || s.NodeID() != 12 {
		t.Fatalf("reset %v, node %d", reset, s.NodeID())
	}
	// Now configured: no second reset, no fastscan.
	feed(s, req(CmdSwitchGlobal, 1), req(CmdConfigureNodeID, 13), req(CmdSwitchGlobal, 0))
	if len(reset) != 1 || len(feed(s, fsReq(0, 0x80, 0, 0))) != 0 {
		t.Fatal("configured slave reset again or answers fastscan")
	}
}

func TestSlaveIgnoresForeignFrames(t *testing.T) {
	s := newTestSlave(t, Unconfigured)
	foreign := []canbus.Frame{
		{ID: SlaveID, Len: 8, Data: [64]byte{byte(CmdIdentifyNonConfig)}},
		{ID: MasterID, Len: 8, Flags: canbus.FlagRemote},
		{ID: MasterID, Len: 8, Flags: canbus.FlagExtended, Data: [64]byte{byte(CmdIdentifyNonConfig)}},
		{ID: MasterID, Len: 12, Flags: canbus.FlagFD, Data: [64]byte{byte(CmdIdentifyNonConfig)}},
		{ID: MasterID, Len: 0},
	}
	if r := feed(s, foreign...); len(r) != 0 {
		t.Fatalf("%v", r)
	}
	if st := s.Stats(); st.Ignored != uint64(len(foreign)) {
		t.Fatalf("%+v", st)
	}
}

func TestSlaveLifecycle(t *testing.T) {
	checkLeaks(t)
	a, b := testbus.Pair(t)
	s, err := NewSlave(a, testAddr, Unconfigured, nil, WithSlaveLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Fatal(err)
	}
	sub, err := b.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	f := req(CmdIdentifyNonConfig)
	if err := b.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := sub.Receive(ctx)
	if err != nil || r.ID != SlaveID || Command(r.Data[0]) != CmdNonConfigSlave {
		t.Fatalf("%v %v", r, err)
	}
	eventually(t, time.Second, func() bool { return s.Stats().Responses == 1 })
	_ = s.Close()
	_ = s.Close()
	if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatal(err)
	}
}

// failBus fails every Send and Subscribe after the first subscription.
type failBus struct {
	*canbus.Bus
	subErr bool
}

func (f *failBus) Send(context.Context, *canbus.Frame) error { return errors.New("bus off") }

func (f *failBus) Subscribe(opts ...canbus.SubscribeOption) (*canbus.Subscription, error) {
	if f.subErr {
		return nil, errors.New("no filters")
	}
	return f.Bus.Subscribe(opts...)
}

func TestSlaveSendError(t *testing.T) {
	checkLeaks(t)
	a, b := testbus.Pair(t)
	s := startSlave(t, &failBus{Bus: a}, testAddr, Unconfigured)
	f := req(CmdIdentifyNonConfig)
	if err := b.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return s.Stats().SendErrors == 1 })
	s2, _ := NewSlave(&failBus{Bus: a, subErr: true}, testAddr, 1)
	if err := s2.Start(); err == nil {
		t.Fatal("subscribe error ignored")
	}
}
