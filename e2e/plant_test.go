// SPDX-License-Identifier: MIT

package e2e

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// Node-IDs of the plant.
const (
	io1   canopen.NodeID = 10 // I/O module: TPDO (SYNC) and RPDO
	io2   canopen.NodeID = 11 // I/O module supervising io1's heartbeat
	drive canopen.NodeID = 20 // CiA 402 drive
	drv2  canopen.NodeID = 21 // the drive's node-ID after the change
)

type events struct {
	mu  sync.Mutex
	evs []master.Event
}

func (e *events) add(ev master.Event) {
	e.mu.Lock()
	e.evs = append(e.evs, ev)
	e.mu.Unlock()
}

func (e *events) count(f func(master.Event) bool) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.evs {
		if f(ev) {
			n++
		}
	}
	return n
}

func (e *events) wait(t *testing.T, what string, n int, f func(master.Event) bool) {
	t.Helper()
	nettest.Eventually(t, 10*time.Second, what, func() bool { return e.count(f) >= n })
}

func is(k master.EventKind, id canopen.NodeID) func(master.Event) bool {
	return func(e master.Event) bool { return e.Kind == k && e.Node == id }
}

func emcyIs(id canopen.NodeID, code uint16) func(master.Event) bool {
	return func(e master.Event) bool { return e.Kind == master.EventEMCY && e.Node == id && e.EMCY.Code == code }
}

func u16(x uint16) []byte { return []byte{byte(x), byte(x >> 8)} }
func u32(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }

func startNode(t *testing.T, bus canopen.Bus, d *od.Dictionary, opts ...node.Option) *node.Node {
	t.Helper()
	n, err := node.New(bus, d, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// runPlant runs the plant scenario: a master and three nodes, each on its
// own bus from open (on one network).
func runPlant(t *testing.T, open func() canopen.Bus) {
	ctx := nettest.Ctx(t)
	var ev events
	m, err := master.New(open(), master.WithEventHandler(ev.add),
		master.WithSDOOptions(sdo.WithTimeout(300*time.Millisecond), sdo.WithRetries(2)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	in32 := pdo.Entry{Index: nettest.IdxIn32, Bits: 32}
	out16 := pdo.Entry{Index: nettest.IdxOut16, Bits: 16}
	common := []master.Entry{{Index: od.IndexProducerHeartbeat, Data: u16(25)}}
	boot := func(extra ...master.Entry) *master.BootConfig {
		return &master.BootConfig{
			DeviceType: nettest.DeviceType,
			Identity:   &master.Identity{VendorID: nettest.VendorID, ProductCode: nettest.ProductCode},
			Entries:    append(append([]master.Entry{}, common...), extra...),
			// Generous for loaded CI machines and the race detector.
			HeartbeatTimeout: 400 * time.Millisecond,
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.SetBoot(io1, boot(
		master.Entry{Index: 0x1800, SubIndex: 1, Data: u32(0x180 + uint32(io1))},
		master.Entry{Index: 0x1800, SubIndex: 2, Data: []byte{1}}, // every SYNC
		master.Entry{Index: 0x1A00, SubIndex: 0, Data: []byte{1}},
		master.Entry{Index: 0x1A00, SubIndex: 1, Data: u32(in32.Uint32())},
		master.Entry{Index: 0x1600, SubIndex: 1, Data: u32(out16.Uint32())},
		master.Entry{Index: 0x1600, SubIndex: 0, Data: []byte{1}},
	)))
	must(m.SetBoot(io2, boot(
		master.Entry{Index: od.IndexConsumerHeartbeat, SubIndex: 1, Data: u32(nmt.ConsumerEntry(io1, 400*time.Millisecond))},
	)))
	must(m.SetBoot(drive, boot()))

	d1 := nettest.Dict(t, io1, nil)
	d2 := nettest.Dict(t, io2, nil)
	dd := nettest.Dict(t, drive, nil)
	bus1 := open()
	n1 := startNode(t, bus1, d1)
	n2 := startNode(t, open(), d2)
	nd := startNode(t, open(), dd, node.WithDrive(cia402.DriveConfig{}, 2*time.Millisecond))
	for _, id := range []canopen.NodeID{io1, io2, drive} {
		ev.wait(t, "boot procedure", 1, is(master.EventBootDone, id))
	}
	for _, n := range []*node.Node{n1, n2, nd} {
		nettest.Eventually(t, 5*time.Second, "operational", func() bool { return n.State() == canopen.StateOperational })
	}
	if c := ev.count(func(e master.Event) bool { return e.Kind == master.EventBootFailed }); c != 0 {
		t.Fatalf("%d boot failures", c)
	}

	// Scan.
	found, err := m.Scan(ctx, []canopen.NodeID{io1, io2, drive, 30})
	if err != nil || len(found) != 3 {
		t.Fatalf("scan: %+v, %v", found, err)
	}
	for _, f := range found {
		if f.DeviceType != nettest.DeviceType || f.Identity.SerialNumber != uint32(f.Node) {
			t.Fatalf("scan: %+v", f)
		}
	}

	// SYNC-driven TPDO and an RPDO.
	var pmu sync.Mutex
	var last int64
	var tpdos int
	if _, err := m.ListenTPDO(ctx, io1, 1, d1, func(_ *canbus.Frame, v []pdo.Value) {
		pmu.Lock()
		last, tpdos = v[0].Value.(int64), tpdos+1
		pmu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	snd, err := m.RPDOSender(ctx, io1, 1, d1)
	if err != nil {
		t.Fatal(err)
	}
	m.SYNC().SetPeriod(10 * time.Millisecond)
	_ = d1.SetValue(nettest.IdxIn32, 0, int32(-123456))
	nettest.Eventually(t, 5*time.Second, "TPDOs on SYNC", func() bool {
		pmu.Lock()
		defer pmu.Unlock()
		return tpdos >= 5 && last == -123456
	})
	must(snd.Send(ctx, uint16(0xA55A)))
	nettest.Eventually(t, 5*time.Second, "RPDO", func() bool {
		v, _ := d1.GetUint(nettest.IdxOut16, 0)
		return v == 0xA55A
	})
	m.SYNC().SetPeriod(0)

	// The drive, over SDO.
	ctl := m.Drive(drive, &cia402.ControllerOptions{Timeout: 5 * time.Second, PollInterval: 2 * time.Millisecond, ResetFault: true})
	must(ctl.Enable(ctx))
	must(ctl.MoveTo(ctx, 5000, &cia402.MoveOptions{Wait: true, Velocity: 500000, Acceleration: 5000000, Deceleration: 5000000}))
	if p, err := ctl.Position(ctx); err != nil || p != 5000 {
		t.Fatalf("position %d, %v", p, err)
	}
	nd.Drive().InjectFault(0x7300) // sensor
	ev.wait(t, "drive EMCY", 1, emcyIs(drive, 0x7300))
	must(ctl.Enable(ctx)) // fault reset, then enable
	ev.wait(t, "drive error reset", 1, emcyIs(drive, emcy.CodeNoError))
	if p, err := ctl.Position(ctx); err != nil || p > 5000 {
		t.Fatalf("position after fault %d, %v", p, err)
	}

	// Heartbeat loss of io1: the master and io2 notice; io1 comes back
	// and is booted again.
	_ = n1.Close()
	ev.wait(t, "master: io1 lost", 1, is(master.EventHeartbeatLost, io1))
	ev.wait(t, "io2: EMCY 0x8130", 1, emcyIs(io2, emcy.CodeHeartbeat))
	if last, ok := m.LastEMCY(io2); !ok || last.MSEF[0] != byte(io1) {
		t.Fatalf("io2 EMCY %+v", last)
	}
	n1 = startNode(t, bus1, d1)
	ev.wait(t, "io1 booted again", 2, is(master.EventBootDone, io1))
	ev.wait(t, "io2 error reset", 1, emcyIs(io2, emcy.CodeNoError))
	ev.wait(t, "master: io1 resumed", 1, is(master.EventHeartbeatResumed, io1))
	nettest.Eventually(t, 5*time.Second, "io1 operational", func() bool { return n1.State() == canopen.StateOperational })

	// The drive changes its node-ID at a reset communication.
	must(nd.SetNodeID(drv2))
	since := time.Now()
	must(m.ResetCommunication(ctx, drive))
	must(m.WaitBootUp(ctx, drv2, since))
	c, err := m.SDO(drv2)
	must(err)
	sw, err := c.ReadUint(ctx, cia402.IndexStatusword, 0)
	must(err)
	if st := cia402.DecodeStatusword(uint16(sw)); st != cia402.OperationEnabled {
		t.Fatalf("drive state %s after reset communication", st)
	}
	old, _ := m.SDO(drive)
	_, err = old.ReadUint(ctx, od.IndexDeviceType, 0)
	var ae *canopen.AbortError
	switch {
	case err == nil:
		t.Fatal("the old node-ID still answers")
	case !errors.As(err, &ae) && !errors.Is(err, canopen.ErrTimeout):
		t.Fatalf("old node-ID: %v", err)
	}
	t.Logf("master stats: %+v", m.Stats())
}
