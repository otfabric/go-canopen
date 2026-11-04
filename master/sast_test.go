// SPDX-License-Identifier: MIT

package master_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// TestOverlayRefusesOutOfRangeValues: a concise DCF or configuration
// entry wider than its sub-index's type is refused instead of being
// truncated; an 8-byte number of mapping entries made Overlay panic
// (int(x) negative, make with a negative length).
func TestOverlayRefusesOutOfRangeValues(t *testing.T) {
	ff8 := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	comm, mapping := od.IndexTPDOComm, od.IndexTPDOMapping
	for _, tc := range []struct {
		name string
		e    master.Entry
		want error
	}{
		{"count 8 bytes", master.Entry{Index: mapping, SubIndex: 0, Data: ff8}, canopen.AbortPDOLength},
		{"count 256", master.Entry{Index: mapping, SubIndex: 0, Data: []byte{0x00, 0x01}}, canopen.AbortPDOLength},
		{"mapping entry", master.Entry{Index: mapping, SubIndex: 1, Data: ff8}, canopen.AbortValueHigh},
		{"COB-ID", master.Entry{Index: comm, SubIndex: 1, Data: ff8}, canopen.AbortValueHigh},
		{"transmission type", master.Entry{Index: comm, SubIndex: 2, Data: []byte{0x00, 0x01}}, canopen.AbortValueHigh},
		{"inhibit time", master.Entry{Index: comm, SubIndex: 3, Data: []byte{0, 0, 1, 0}}, canopen.AbortValueHigh},
		{"event timer", master.Entry{Index: comm, SubIndex: 5, Data: []byte{0, 0, 1, 0}}, canopen.AbortValueHigh},
		{"SYNC start", master.Entry{Index: comm, SubIndex: 6, Data: []byte{0x00, 0x01}}, canopen.AbortValueHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := master.Overlay(pdo.Config{}, []master.Entry{tc.e})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Overlay: %v, want %v", err, tc.want)
			}
		})
	}
	// In range, with more bytes than the type: unchanged.
	cfg, err := master.Overlay(pdo.Config{}, []master.Entry{
		{Index: comm, SubIndex: 1, Data: []byte{0x81, 0x01, 0, 0, 0, 0, 0, 0}},
		{Index: comm, SubIndex: 2, Data: []byte{0xFE, 0}},
		{Index: comm, SubIndex: 3, Data: []byte{0xFF, 0xFF, 0, 0}},
		{Index: comm, SubIndex: 5, Data: []byte{0x10, 0x00}},
		{Index: comm, SubIndex: 6, Data: []byte{0xF0}},
		{Index: mapping, SubIndex: 1, Data: []byte{0x08, 0x00, 0x00, 0x20}},
		{Index: mapping, SubIndex: 0, Data: []byte{1}},
	})
	if err != nil || cfg.COBID != 0x181 || cfg.TransmissionType != 0xFE || cfg.InhibitTime != 0xFFFF ||
		cfg.EventTimer != 0x10 || cfg.SyncStart != 0xF0 || len(cfg.Mapping) != 1 {
		t.Fatalf("Overlay in range: %+v, %v", cfg, err)
	}
}

// TestWideUnsigned32Answers: a device answering 0x1000 or 0x1018 with a
// value wider than UNSIGNED32 is no match by its low 32 bits: the boot's
// device type check fails, Scan and Identity report ErrDataType instead
// of a truncated value.
func TestWideUnsigned32Answers(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := od.New(7)
	_ = d.Add(od.NewVar(od.IndexDeviceType, "Device type", canopen.Unsigned64, od.AccessRO, uint64(1)<<32|uint64(nettest.DeviceType)))
	_ = d.Add(od.NewRecord(od.IndexIdentity, "Identity",
		od.Sub(1, "Vendor-ID", canopen.Unsigned64, od.AccessRO, uint64(1)<<32|uint64(nettest.VendorID))))
	startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 7))

	if err := r.m.Boot(ctx, 7, master.BootConfig{DeviceType: nettest.DeviceType, NoStart: true}); !errors.Is(err, master.ErrIdentity) {
		t.Fatalf("boot device type check: %v", err)
	}
	found, err := r.m.Scan(ctx, []canopen.NodeID{7})
	if len(found) != 0 || !errors.Is(err, canopen.ErrDataType) {
		t.Fatalf("scan: %+v, %v", found, err)
	}
	if id, err := r.m.Identity(ctx, 7); !errors.Is(err, canopen.ErrDataType) {
		t.Fatalf("identity: %v, %v", id, err)
	}
}

// TestEventQueueBounded: with a handler that does not return, a flood of
// EMCY frames keeps at most WithEventQueue of them (the newest), counts
// the others in Stats.EventsDropped, and a heartbeat loss in between is
// not dropped and keeps its place in the order. The queue was unbounded.
func TestEventQueueBounded(t *testing.T) {
	nettest.CheckLeaks(t)
	const bound, frames = 8, 200
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var mu sync.Mutex
	var got []master.Event
	r := newRig(t, 1, master.WithEventQueue(bound), master.WithEventHandler(func(e master.Event) {
		<-release
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}))
	t.Cleanup(unblock)
	ctx := context.Background()
	send := func(f canbus.Frame) {
		t.Helper()
		if err := r.buses[1].Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	emcyFrame := func(i int) canbus.Frame {
		f := canbus.Frame{ID: canopen.FuncEMCY + 5, Len: 8}
		f.Data[0], f.Data[1], f.Data[3] = 0x00, 0x10, byte(i)
		return f
	}
	if err := r.m.Monitor(9, 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	hb := canbus.Frame{ID: canopen.FuncHeartbeat + 9, Len: 1}
	hb.Data[0] = byte(canopen.StateOperational)
	send(hb)
	for i := range frames / 2 {
		send(emcyFrame(i))
	}
	nettest.Eventually(t, 5*time.Second, "heartbeat loss", func() bool { return r.m.State(9) == canopen.StateUnknown })
	for i := frames / 2; i < frames; i++ {
		send(emcyFrame(i))
	}
	nettest.Eventually(t, 5*time.Second, "EMCY received", func() bool {
		return r.m.Stats().EMCY.Received >= frames
	})
	dropped := r.m.Stats().EventsDropped
	unblock()
	// The event in the handler when the flood came, the bound, the
	// heartbeat loss; the state change before it may be dropped.
	nettest.Eventually(t, 5*time.Second, "delivery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, e := range got {
			if e.Kind == master.EventEMCY {
				n++
			}
		}
		return uint64(n)+dropped >= frames
	})
	mu.Lock()
	defer mu.Unlock()
	var lost, emcys int
	lastEMCY, lostAt, lastEMCYAt := -1, -1, -1
	for i, e := range got {
		switch e.Kind {
		case master.EventHeartbeatLost:
			lost++
			lostAt = i
		case master.EventEMCY:
			emcys++
			lastEMCY, lastEMCYAt = int(e.EMCY.MSEF[0]), i
		default: // state changes
		}
	}
	if lost != 1 || lostAt > lastEMCYAt {
		t.Fatalf("heartbeat losses delivered: %d, at %d, last EMCY at %d", lost, lostAt, lastEMCYAt)
	}
	if emcys > bound+1 || dropped < frames-bound-1 {
		t.Fatalf("%d EMCY events delivered, %d dropped (bound %d)", emcys, dropped, bound)
	}
	if lastEMCY != (frames-1)&0xFF {
		t.Fatalf("last EMCY delivered #%d, want the newest", lastEMCY)
	}
}
