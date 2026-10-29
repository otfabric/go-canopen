// SPDX-License-Identifier: MIT

package emcy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/timestamp"
)

var fdSample = FDMessage{
	LogicalDevice: 2, CiASpec: 402, Code: 0x8130, Register: RegGeneric | RegCommunication,
	Specific: [5]byte{1, 2, 3, 4, 5}, Status: 3, Time: timestamp.TimeOfDay{Ms: 1000, Days: 15000},
}

func TestFDMessageLayouts(t *testing.T) {
	b := fdSample.Bytes()
	if got := fmt.Sprintf("% X", b); got != "02 00 92 01 30 81 11 01 02 03 04 05 03 00 E8 03 00 00 98 3A" {
		t.Errorf("CiA layout %s", got)
	}
	// bytes 4..11 are the classical message
	cc := fdSample.Message().Bytes()
	if fmt.Sprintf("% X", b[4:12]) != fmt.Sprintf("% X", cc[:]) {
		t.Error("classical part")
	}
	r := fdSample.BytesLayout(FDLayoutRegisterFirst)
	if r[4] != 0x11 || r[5] != 0x30 || r[6] != 0x81 {
		t.Errorf("register-first % X", r)
	}
	for _, l := range []FDLayout{FDLayoutCiA, FDLayoutRegisterFirst} {
		x := fdSample.BytesLayout(l)
		m, err := ParseFDMessageLayout(append(x[:], 0, 0, 0, 0), l)
		if err != nil || m != fdSample {
			t.Errorf("%v: %+v %v", l, m, err)
		}
	}
	if _, err := ParseFDMessage(b[:19]); !errors.Is(err, canopen.ErrProtocol) {
		t.Error(err)
	}
	if FDLayoutCiA.String() != "CiA" || FDLayoutRegisterFirst.String() != "register-first" || FDLayout(9).String() != "FDLayout(9)" {
		t.Error("layout names")
	}
	if fdSample.String() == "" || fdSample.IsReset() || !(FDMessage{}).IsReset() {
		t.Error("String/IsReset")
	}
	f := fdSample.Frame(canopen.NewCOBID(0x85), true, FDLayoutCiA)
	if !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.Len != FDSize || f.ID != 0x85 {
		t.Errorf("frame %v", f)
	}
}

func TestParseFDFrame(t *testing.T) {
	f := fdSample.Frame(canopen.NewCOBID(0x85), false, FDLayoutCiA)
	m, fd, err := ParseFDFrame(&f, FDLayoutCiA)
	if err != nil || fd == nil || *fd != fdSample || m != fdSample.Message() {
		t.Fatalf("%v %v %v", m, fd, err)
	}
	cc := fdSample.Message().Frame(canopen.NewCOBID(0x85))
	if m, fd, err := ParseFDFrame(&cc, FDLayoutCiA); err != nil || fd != nil || m != fdSample.Message() {
		t.Errorf("classical: %v %v %v", m, fd, err)
	}
	short, _ := canbus.NewFD(0x85, false, false, []byte{0x30, 0x81, 1})
	if _, fd, err := ParseFDFrame(&short, FDLayoutCiA); err != nil || fd != nil {
		t.Errorf("short FD frame: %v %v", fd, err)
	}
	odd, _ := canbus.NewFD(0x85, false, false, make([]byte, 12))
	if _, _, err := ParseFDFrame(&odd, FDLayoutCiA); !errors.Is(err, canopen.ErrProtocol) {
		t.Errorf("12 bytes: %v", err)
	}
	rtr, _ := canbus.NewRemote(0x85, false, 0)
	if _, _, err := ParseFDFrame(&rtr, FDLayoutCiA); !errors.Is(err, canopen.ErrProtocol) {
		t.Errorf("remote: %v", err)
	}
}

func TestFDProducerConsumer(t *testing.T) {
	p := testbus.Ports(t, 3, virtual.WithFD())
	clock := func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	prod, err := NewProducer(p[0], 5, WithFD(true), WithFDInfo(1, 1301), WithFDClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	evFD, evCC := newEvents[Event](), newEvents[Event]()
	cfd := NewConsumer(p[1], WithFD(false), WithHandler(evFD.add))
	ccc := NewConsumer(p[2], WithHandler(evCC.add))
	for _, c := range []*Consumer{cfd, ccc} {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	ctx := context.Background()
	if err := prod.Raise(ctx, 0x8130, RegCommunication, [5]byte{9}); err != nil {
		t.Fatal(err)
	}
	e := evFD.wait(t, time.Second, func(Event) bool { return true })
	want, _ := timestamp.FromTime(clock())
	if e.FD == nil || e.FD.LogicalDevice != 1 || e.FD.CiASpec != 1301 || e.FD.Time != want || e.Code != 0x8130 || e.Node != 5 || e.MSEF[0] != 9 {
		t.Fatalf("%+v %+v", e, e.FD)
	}
	eventually(t, time.Second, func() bool { return ccc.Stats().Invalid == 1 })
	// an explicit FD message and a classical one through Send
	fm := fdSample
	if err := prod.SendFD(ctx, fm); err != nil {
		t.Fatal(err)
	}
	e = evFD.wait(t, time.Second, func(Event) bool { return true })
	if e.FD == nil || *e.FD != fdSample {
		t.Errorf("%+v", e.FD)
	}
	if err := prod.Clear(ctx, 0x8130); err != nil {
		t.Fatal(err)
	}
	e = evFD.wait(t, time.Second, func(Event) bool { return true })
	if !e.IsReset() || e.FD == nil {
		t.Errorf("reset %+v", e)
	}
	// a classical producer's SendFD still sends an FD frame; its Send a
	// classical one, which both consumers take
	cprod, _ := NewProducer(p[0], 6)
	if err := cprod.SendFD(ctx, fdSample); err != nil {
		t.Fatal(err)
	}
	evFD.wait(t, time.Second, func(e Event) bool { return e.Node == 6 && e.FD != nil })
	if err := cprod.Send(ctx, Message{Code: 0x1000}); err != nil {
		t.Fatal(err)
	}
	evFD.wait(t, time.Second, func(e Event) bool { return e.Node == 6 && e.FD == nil })
	evCC.wait(t, time.Second, func(e Event) bool { return e.Node == 6 })
	// queued FD messages behind the inhibit time
	qp, _ := NewProducer(p[0], 7, WithFD(false), WithInhibit(20*time.Millisecond), WithFDLayout(FDLayoutRegisterFirst))
	if err := qp.Start(); err != nil {
		t.Fatal(err)
	}
	defer qp.Close()
	_ = qp.SendFD(ctx, fdSample)
	_ = qp.SendFD(ctx, fdSample)
	got := 0
	for got < 2 {
		e := evFD.wait(t, time.Second, func(e Event) bool { return e.Node == 7 })
		// the consumer uses the CiA layout: register and code are swapped
		if e.FD == nil || e.FD.LogicalDevice != 2 {
			t.Fatalf("%+v", e)
		}
		got++
	}
	if qp.Stats().Queued != 1 {
		t.Errorf("%+v", qp.Stats())
	}
}

func FuzzParseFDFrame(f *testing.F) {
	b := fdSample.Bytes()
	f.Add(uint8(canbus.FlagFD), b[:], uint8(0))
	f.Add(uint8(0), []byte{0x30, 0x81, 0x11, 1, 2, 3, 4, 5}, uint8(1))
	f.Add(uint8(canbus.FlagFD), make([]byte, 12), uint8(0))
	f.Fuzz(func(t *testing.T, flags uint8, data []byte, layout uint8) {
		var fr canbus.Frame
		fr.ID, fr.Flags = 0x85, canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		l := FDLayout(layout % 2)
		m, fd, err := ParseFDFrame(&fr, l)
		if err != nil {
			return
		}
		if fd != nil {
			x := fd.BytesLayout(l)
			back, err := ParseFDMessageLayout(x[:], l)
			if err != nil || back != *fd || fd.Message() != m {
				t.Fatalf("round trip %+v -> %+v (%v)", fd, back, err)
			}
			_ = fd.String()
		}
		NewConsumer(nil, WithFD(false), WithFDLayout(l)).receive(&fr)
	})
}
