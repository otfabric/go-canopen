// SPDX-License-Identifier: MIT

package syncobj

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// TestFD: a CANopen FD producer sends SYNC in CAN FD frames; only a
// consumer made WithFD takes them.
func TestFD(t *testing.T) {
	p := testbus.Ports(t, 3, virtual.WithFD())
	evFD, evCC := newEvents[Event](), newEvents[Event]()
	cfd, err := NewConsumer(p[1], WithFD(false), WithHandler(evFD.add), WithWindow(500*time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	ccc, _ := NewConsumer(p[2], WithHandler(evCC.add))
	for _, c := range []*Consumer{cfd, ccc} {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	if cfd.Window() != 500*time.Microsecond || ccc.Window() != 0 {
		t.Errorf("windows %v %v", cfd.Window(), ccc.Window())
	}
	sub := subscribe(t, p[2])
	prod, _ := NewProducer(p[0], WithFD(true), WithOverflow(4))
	if err := prod.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := recv(t, sub, nil)
	if !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.Len != 1 || f.Data[0] != 1 {
		t.Errorf("frame %v", f)
	}
	e := evFD.wait(t, time.Second, func(Event) bool { return true })
	if e.Counter != 1 || e.WindowEnd.Sub(e.Time) != 500*time.Microsecond {
		t.Errorf("%+v", e)
	}
	// a classical SYNC reaches both
	send(t, p[0], frame(canopen.COBID(canopen.FuncSYNC), 0, false, false))
	evFD.wait(t, time.Second, func(Event) bool { return true })
	evCC.wait(t, time.Second, func(Event) bool { return true })
	if ccc.Stats().Received != 1 {
		t.Errorf("classical consumer took the FD SYNC: %+v", ccc.Stats())
	}
	// extended FD frame
	if f := frame(canopen.NewCOBID(0x12345), 3, true, false); !f.IsFD() || !f.IsExtended() {
		t.Errorf("%v", f)
	}
}

// TestWindowFromDictionary: the consumer follows 0x1007.
func TestWindowFromDictionary(t *testing.T) {
	d, err := od.Standard(od.Config{NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.SetUint(od.IndexSyncWindow, 0, 250)
	a, _ := testbus.Pair(t)
	c, err := NewConsumer(a, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Window() != 250*time.Microsecond {
		t.Errorf("%v", c.Window())
	}
	_ = d.SetUint(od.IndexSyncWindow, 0, 0)
	eventually(t, time.Second, func() bool { return c.Window() == 0 })
}

func FuzzReceiveFD(f *testing.F) {
	f.Add(uint32(0x80), uint8(canbus.FlagFD), []byte{1})
	f.Add(uint32(0x80), uint8(canbus.FlagFD|canbus.FlagBRS), []byte{})
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte) {
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		c, _ := NewConsumer(nil, WithFD(false), WithOverflow(10))
		e, ok := c.receive(&fr, canopen.COBID(canopen.FuncSYNC))
		if ok && (fr.Flags&(canbus.FlagRemote|canbus.FlagError) != 0 || fr.Len > 0 && e.Counter != fr.Data[0]) {
			t.Fatalf("accepted %v as %+v", fr, e)
		}
	})
}
