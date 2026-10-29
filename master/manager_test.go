// SPDX-License-Identifier: MIT

package master_test

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
)

// fastBoot are boot times for tests.
var fastBoot = master.BootTiming{
	RetryDelay: 30 * time.Millisecond, ResetTimeout: 500 * time.Millisecond,
	GuardTimeout: 60 * time.Millisecond, PollInterval: 5 * time.Millisecond, ProgramTimeout: 5 * time.Second,
}

const masterID canopen.NodeID = 1

// mgrDict is a manager dictionary for node-ID 1.
func mgrDict(t testing.TB) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: masterID, HeartbeatConsumers: 8, NodeGuarding: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := master.AddManagerObjects(d); err != nil {
		t.Fatal(err)
	}
	if err := master.AddManagerObjects(d); err != nil {
		t.Fatal("second AddManagerObjects:", err)
	}
	return d
}

func set32(t testing.TB, d *od.Dictionary, index uint16, sub uint8, v uint32) {
	t.Helper()
	if err := d.SetUint(index, sub, uint64(v)); err != nil {
		t.Fatal(err)
	}
}

func assign(t testing.TB, d *od.Dictionary, id canopen.NodeID, a master.Assignment) {
	t.Helper()
	set32(t, d, master.IndexSlaveAssignment, uint8(id), uint32(a))
}

// consume adds a heartbeat consumer entry for a node to the manager.
func consume(t testing.TB, d *od.Dictionary, sub uint8, id canopen.NodeID, ms time.Duration) {
	t.Helper()
	set32(t, d, od.IndexConsumerHeartbeat, sub, nmt.ConsumerEntry(id, ms))
}

func concise(t testing.TB, d *od.Dictionary, id canopen.NodeID, entries ...cdcf.Entry) {
	t.Helper()
	b, err := cdcf.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Set(cdcf.IndexConciseDCF, uint8(id), b); err != nil {
		t.Fatal(err)
	}
}

// mgrRig starts the slaves first (on buses[1..]), then the manager.
type mgrRig struct {
	*rig
	d *od.Dictionary
}

func startManager(t testing.TB, buses []*canbus.Bus, d *od.Dictionary, opts ...master.Option) *mgrRig {
	t.Helper()
	opts = append([]master.Option{master.WithDictionary(d), master.WithBootTiming(fastBoot)}, opts...)
	return &mgrRig{rig: newRigOn(t, buses, opts...), d: d}
}

// bootUps subscribes to bus and returns a function that waits for the
// boot-up messages of the nodes, so that they have passed bus before a
// later subscriber (the manager) starts.
func bootUps(t testing.TB, bus *canbus.Bus, ids ...canopen.NodeID) func() {
	t.Helper()
	sub, err := bus.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		defer sub.Close()
		left := map[canbus.ID]bool{}
		for _, id := range ids {
			left[canopen.FuncHeartbeat+canbus.ID(id)] = true
		}
		ctx := nettest.Ctx(t)
		for len(left) > 0 {
			f, err := sub.Receive(ctx)
			if err != nil {
				t.Fatalf("boot-up messages %v: %v", left, err)
			}
			if f.Len == 1 && f.Data[0] == 0 {
				delete(left, f.ID)
			}
		}
	}
}

func bootOK(n canopen.NodeID) func(master.Event) bool {
	return func(e master.Event) bool { return e.Kind == master.EventBootDone && e.Node == n }
}

func TestManagerNetworkBoot(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 4)
	d5 := nettest.Dict(t, 5, nil)
	d6 := nettest.Dict(t, 6, nil)
	d7 := nettest.Dict(t, 7, nil)
	n5 := startNode(t, buses[1], d5)
	n6 := startNode(t, buses[2], d6)
	n7 := startNode(t, buses[3], d7)

	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	boot := master.AssignSlave | master.AssignBoot
	assign(t, d, 5, master.NewAssignment(boot|master.AssignMandatory, 0, 0))
	assign(t, d, 6, master.NewAssignment(boot, 0, 0))
	assign(t, d, 7, master.NewAssignment(boot|master.AssignMandatory, 0, 0))
	set32(t, d, master.IndexExpectedDeviceType, 5, nettest.DeviceType)
	set32(t, d, master.IndexExpectedVendorID, 5, nettest.VendorID)
	set32(t, d, master.IndexExpectedProductCode, 5, nettest.ProductCode)
	set32(t, d, master.IndexExpectedRevision, 5, nettest.Revision)
	set32(t, d, master.IndexExpectedSerialNumber, 5, 5)
	consume(t, d, 1, 7, 500*time.Millisecond)
	concise(t, d, 7,
		cdcf.Entry{Index: od.IndexProducerHeartbeat, Data: u16(30)},
		cdcf.Entry{Index: nettest.IdxParam, Data: u32(0xBEEF)})
	r := startManager(t, buses, d)

	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	for _, id := range []canopen.NodeID{5, 6, 7} {
		ev := r.rec.wait(t, "boot done", bootOK(id))
		if ev.Status != master.BootOK {
			t.Fatalf("node %d status %s", id, ev.Status)
		}
	}
	for _, n := range []*node.Node{n5, n6, n7} {
		waitState(t, n, canopen.StateOperational)
	}
	if st := r.m.Node().State(); st != canopen.StateOperational {
		t.Fatalf("master state %s", st)
	}
	if v, _ := d7.GetUint(nettest.IdxParam, 0); v != 0xBEEF {
		t.Fatalf("node 7 param %#x", v)
	}
	if v, _ := d7.GetUint(od.IndexProducerHeartbeat, 0); v != 30 {
		t.Fatalf("node 7 heartbeat %d", v)
	}
	if st, done := r.m.SlaveStatus(7); !done || st != master.BootOK {
		t.Fatalf("slave status %s %v", st, done)
	}
	// 0x1F82 reads the node states.
	nettest.Eventually(t, 2*time.Second, "1F82", func() bool {
		b, err := d.Read(master.IndexRequestNMT, 7)
		return err == nil && b[0] == byte(canopen.StateOperational)
	})
	if b, _ := d.Read(master.IndexRequestNMT, 9); b[0] != 0 {
		t.Fatalf("unknown node state %d", b[0])
	}
	// The master's own state changes are events.
	if _, ok := r.rec.find(func(e master.Event) bool {
		return e.Kind == master.EventStateChange && e.Node == masterID && e.State == canopen.StateOperational
	}); !ok {
		t.Fatal("no own state event")
	}
	if s := (master.Event{Kind: master.EventNetworkBootDone, Node: 1}).String(); !strings.Contains(s, "network-boot-done") {
		t.Fatal(s)
	}
}

func TestManagerIdentityMismatch(t *testing.T) {
	tests := []struct {
		index  uint16
		status master.BootStatus
	}{
		{master.IndexExpectedDeviceType, master.BootDeviceType},
		{master.IndexExpectedVendorID, master.BootVendorID},
		{master.IndexExpectedProductCode, master.BootProductCode},
		{master.IndexExpectedRevision, master.BootRevision},
		{master.IndexExpectedSerialNumber, master.BootSerialNumber},
	}
	for _, tc := range tests {
		t.Run(tc.status.String(), func(t *testing.T) {
			nettest.CheckLeaks(t)
			buses := testbus.Ports(t, 2)
			n5 := startNode(t, buses[1], nettest.Dict(t, 5, nil))
			d := mgrDict(t)
			set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
			assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
			set32(t, d, tc.index, 5, 0xDEAD)
			r := startManager(t, buses, d)
			ev := r.rec.wait(t, "network boot failed", kind(master.EventNetworkBootFailed, 5))
			var be *master.BootError
			if ev.Status != tc.status || !errors.As(ev.Err, &be) || be.Status != tc.status || !errors.Is(ev.Err, master.ErrIdentity) {
				t.Fatalf("event %v status %s", ev, ev.Status)
			}
			if !strings.Contains(be.Error(), string(rune(tc.status))) {
				t.Fatalf("error text %q", be.Error())
			}
			if _, ok := r.rec.find(kind(master.EventBootFailed, 5)); !ok {
				t.Fatal("no boot-failed event")
			}
			time.Sleep(50 * time.Millisecond)
			if st := r.m.Node().State(); st != canopen.StatePreOperational {
				t.Fatalf("master state %s", st)
			}
			if n5.State() != canopen.StatePreOperational {
				t.Fatalf("slave started: %s", n5.State())
			}
		})
	}
}

func TestManagerMissingMandatory(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	startNode(t, buses[1], nettest.Dict(t, 5, nil))
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	set32(t, d, master.IndexBootTime, 0, 200)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	assign(t, d, 9, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	assign(t, d, 10, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0)) // optional, absent
	start := time.Now()
	r := startManager(t, buses, d)
	ev := r.rec.wait(t, "network boot failed", kind(master.EventNetworkBootFailed, 9))
	if ev.Status != master.BootNoResponse || !errors.Is(ev.Err, canopen.ErrTimeout) {
		t.Fatalf("event %v", ev)
	}
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Fatalf("gave up after %v", el)
	}
	r.rec.wait(t, "optional booted", bootOK(5))
	if _, ok := r.rec.find(kind(master.EventBootFailed, 10)); ok {
		t.Fatal("absent optional slave reported")
	}
	if st := r.m.Node().State(); st != canopen.StatePreOperational {
		t.Fatalf("master state %s", st)
	}
}

func TestManagerStartup(t *testing.T) {
	boot := master.AssignSlave | master.AssignBoot
	tests := []struct {
		name        string
		startup     uint32
		a6          uint32
		masterOp    bool // the master enters Operational on its own
		slavesOp    bool // the slaves are started
		startMaster bool // the test sets the master Operational
	}{
		{"individual", master.StartupMaster, boot, true, true, false},
		{"start all", master.StartupMaster | master.StartupStartAll, boot, true, true, false},
		{"start all, one not bootable", master.StartupMaster | master.StartupStartAll, master.AssignSlave, true, true, false},
		{"application starts master", master.StartupMaster | master.StartupStartAll | master.StartupNoAutoOperational, boot, false, true, true},
		{"application starts slaves", master.StartupMaster | master.StartupNoStartSlaves, boot, true, false, false},
		{"not a master", 0, boot, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nettest.CheckLeaks(t)
			buses := testbus.Ports(t, 3)
			n5 := startNode(t, buses[1], nettest.Dict(t, 5, nil))
			n6 := startNode(t, buses[2], nettest.Dict(t, 6, nil))
			d := mgrDict(t)
			set32(t, d, master.IndexNMTStartup, 0, tc.startup)
			assign(t, d, 5, master.NewAssignment(boot|master.AssignMandatory, 0, 0))
			assign(t, d, 6, master.Assignment(tc.a6))
			r := startManager(t, buses, d)
			if tc.startup == 0 {
				time.Sleep(100 * time.Millisecond)
				if _, ok := r.rec.find(kind(master.EventNetworkBootDone, masterID)); ok {
					t.Fatal("a slave booted the network")
				}
				if err := r.m.BootNetwork(nettest.Ctx(t)); err != nil {
					t.Fatal(err)
				}
				return
			}
			r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
			r.rec.wait(t, "boot 6", bootOK(6))
			if tc.startMaster {
				time.Sleep(50 * time.Millisecond)
				if n5.State() == canopen.StateOperational {
					t.Fatal("slave started before the master")
				}
				_ = r.m.Node().SetState(canopen.StateOperational)
			}
			if tc.masterOp {
				waitState(t, r.m.Node(), canopen.StateOperational)
			}
			if tc.slavesOp {
				waitState(t, n5, canopen.StateOperational)
				if tc.a6 != uint32(master.AssignSlave) {
					waitState(t, n6, canopen.StateOperational)
				}
			} else {
				time.Sleep(50 * time.Millisecond)
				if n5.State() == canopen.StateOperational || n6.State() == canopen.StateOperational {
					t.Fatal("slaves started")
				}
			}
		})
	}
}

// TestManagerConfigurationSkip configures a slave from its concise DCF,
// has it store the configuration, restarts it and checks that the next
// boot finds 0x1020 up to date and does not configure it again.
func TestManagerConfigurationSkip(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	s := store.NewMemory()
	const date, tod = 15000, 3600000
	slaveDict := func() (*od.Dictionary, *atomic.Int32) {
		d := nettest.Dict(t, 5, nil)
		if err := store.AddObjects(d); err != nil {
			t.Fatal(err)
		}
		var writes atomic.Int32
		d.OnWrite(nettest.IdxParam, 0, func(*od.Variable, []byte) error { writes.Add(1); return nil })
		return d, &writes
	}
	d5, w5 := slaveDict()
	n5, err := node.New(buses[1], d5, node.WithStore(s))
	if err != nil {
		t.Fatal(err)
	}
	if err := n5.Start(); err != nil {
		t.Fatal(err)
	}
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	set32(t, d, cdcf.IndexExpectedConfigDate, 5, date)
	set32(t, d, cdcf.IndexExpectedConfigTime, 5, tod)
	concise(t, d, 5,
		cdcf.Entry{Index: nettest.IdxParam, Data: u32(0x55)},
		cdcf.Entry{Index: cdcf.IndexVerifyConfiguration, SubIndex: 1, Data: u32(date)},
		cdcf.Entry{Index: cdcf.IndexVerifyConfiguration, SubIndex: 2, Data: u32(tod)})
	// A verification read that times out (a CPU-starved run) correctly
	// leads to configuring again, which this test asserts does not
	// happen: a wide SDO timeout keeps starvation out of it.
	r := startManager(t, buses, d, master.WithStoreAfterConfigure(store.All),
		master.WithSDOOptions(sdo.WithTimeout(2*time.Second)))
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	if w5.Load() != 1 || s.Saves() != 1 {
		t.Fatalf("first boot: %d writes, %d saves", w5.Load(), s.Saves())
	}
	_ = n5.Close()

	// The slave restarts (power cycle): a fresh dictionary, the same store.
	d5b, w5b := slaveDict()
	n5b := startNode(t, buses[2], d5b, node.WithStore(s))
	nettest.Eventually(t, waitTimeout, "second boot", func() bool {
		return r.rec.count(bootOK(5)) == 2
	})
	waitState(t, n5b, canopen.StateOperational)
	if w5b.Load() != 0 || s.Saves() != 1 {
		t.Fatalf("second boot configured again: %d writes, %d saves; events %v", w5b.Load(), s.Saves(), r.rec.all())
	}
	if v, _ := d5b.GetUint(nettest.IdxParam, 0); v != 0x55 {
		t.Fatalf("param %#x after restart", v)
	}
	// A changed expected date configures it again.
	set32(t, d, cdcf.IndexExpectedConfigDate, 5, date+1)
	if err := r.m.BootSlave(nettest.Ctx(t), 5); err != nil {
		t.Fatal(err)
	}
	if w5b.Load() != 1 {
		t.Fatalf("not configured after a date change: %d", w5b.Load())
	}
}

// TestManagerRestoreAndConfigFailure restores a slave's defaults before
// configuring it, and reports a failing concise DCF as status J.
func TestManagerRestoreAndConfigFailure(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	s := store.NewMemory()
	d5 := nettest.Dict(t, 5, nil)
	_ = store.AddObjects(d5)
	_ = d5.SetUint(nettest.IdxParam, 0, 0x99)
	n5 := startNode(t, buses[1], d5, node.WithStore(s))
	if err := n5.SaveParameters(store.All); err != nil {
		t.Fatal(err)
	}
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory|master.AssignRestore, 0, 0))
	_ = d.SetUint(master.IndexRestoreConfiguration, 5, 1)
	concise(t, d, 5, cdcf.Entry{Index: od.IndexProducerHeartbeat, Data: u16(0)})
	r := startManager(t, buses, d)
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	if v, _ := d5.GetUint(nettest.IdxParam, 0); v != 0x12345678 {
		t.Fatalf("param %#x: not restored", v)
	}
	// A concise DCF the slave refuses.
	concise(t, d, 5, cdcf.Entry{Index: od.IndexDeviceType, Data: u32(1)})
	err := r.m.BootSlave(nettest.Ctx(t), 5)
	var be *master.BootError
	if !errors.As(err, &be) || be.Status != master.BootConfiguration || !errors.Is(err, canopen.AbortReadOnly) {
		t.Fatalf("bad concise DCF: %v", err)
	}
	// A malformed one.
	_ = d.Set(cdcf.IndexConciseDCF, 5, []byte{1, 2})
	if err := r.m.BootSlave(nettest.Ctx(t), 5); !errors.Is(err, cdcf.ErrFormat) {
		t.Fatalf("malformed concise DCF: %v", err)
	}
	// An empty one is no configuration.
	concise(t, d, 5)
	if err := r.m.BootSlave(nettest.Ctx(t), 5); err != nil {
		t.Fatal(err)
	}
	// A restore the slave refuses.
	_ = d.SetUint(master.IndexRestoreConfiguration, 5, 9)
	if err := r.m.BootSlave(nettest.Ctx(t), 5); !errors.As(err, &be) || be.Status != master.BootConfiguration {
		t.Fatalf("bad restore: %v", err)
	}
	// Restoring the communication parameters resets communication.
	_ = d.SetUint(master.IndexRestoreConfiguration, 5, 2)
	before := n5.Stats().Resets
	if err := r.m.BootSlave(nettest.Ctx(t), 5); err != nil {
		t.Fatal(err)
	}
	if n5.Stats().Resets == before {
		t.Fatal("no reset")
	}
}

func TestManagerKeepAlive(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 4)
	// The start's boot-up messages pass before the manager starts: one
	// reaching it would boot node 5 before the network boot-up, and the
	// first boot event would not be the network boot-up's.
	ups := bootUps(t, buses[0], 5, 6, 7)
	hb := func(c *od.Config) { c.HeartbeatMs = 20 }
	n5 := startNode(t, buses[1], nettest.Dict(t, 5, hb))
	_ = n5.SetState(canopen.StateOperational)
	n6 := startNode(t, buses[2], nettest.Dict(t, 6, hb))
	n7 := startNode(t, buses[3], nettest.Dict(t, 7, nil))
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	keep := master.AssignSlave | master.AssignBoot | master.AssignMandatory | master.AssignKeepAlive
	assign(t, d, 5, master.NewAssignment(keep, 0, 0))
	assign(t, d, 6, master.NewAssignment(keep, 0, 0))
	assign(t, d, 7, master.NewAssignment(keep, 0, 0)) // no heartbeat: node guarding check
	consume(t, d, 1, 5, 300*time.Millisecond)
	consume(t, d, 2, 6, 300*time.Millisecond)
	ups()
	r := startManager(t, buses, d)
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	if ev := r.rec.wait(t, "boot 5", bootOK(5)); ev.Status != master.BootOperational {
		t.Fatalf("node 5 status %s", ev.Status)
	}
	if n5.Stats().Resets != 0 {
		t.Fatal("operational keep-alive node reset")
	}
	if ev := r.rec.wait(t, "boot 6", bootOK(6)); ev.Status != master.BootOK || n6.Stats().Resets == 0 {
		t.Fatalf("node 6 status %s resets %d", ev.Status, n6.Stats().Resets)
	}
	if ev := r.rec.wait(t, "boot 7", bootOK(7)); ev.Status != master.BootOK || n7.Stats().Resets == 0 {
		t.Fatalf("node 7 status %s resets %d", ev.Status, n7.Stats().Resets)
	}
	// A keep-alive node that does not answer: E without a heartbeat
	// consumer entry... F without one.
	assign(t, d, 9, master.NewAssignment(keep, 0, 0))
	var be *master.BootError
	if err := r.m.BootSlave(nettest.Ctx(t), 9); !errors.As(err, &be) || be.Status != master.BootGuarding {
		t.Fatalf("absent keep-alive: %v", err)
	}
	consume(t, d, 3, 9, 50*time.Millisecond)
	if err := r.m.BootSlave(nettest.Ctx(t), 9); !errors.As(err, &be) || be.Status != master.BootHeartbeat {
		t.Fatalf("absent keep-alive with heartbeat: %v", err)
	}
}

// TestManagerErrorControl loses slaves and checks the reactions of 0x1F80.
func TestManagerErrorControl(t *testing.T) {
	tests := []struct {
		name    string
		startup uint32
	}{
		{"reset node", master.StartupMaster},
		{"stop all", master.StartupMaster | master.StartupStopAllOnError | master.StartupResetAllOnError},
		{"reset all", master.StartupMaster | master.StartupResetAllOnError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nettest.CheckLeaks(t)
			buses := testbus.Ports(t, 4)
			hb := func(c *od.Config) { c.HeartbeatMs = 20 }
			n5 := startNode(t, buses[1], nettest.Dict(t, 5, hb))
			n6 := startNode(t, buses[2], nettest.Dict(t, 6, hb))
			d := mgrDict(t)
			set32(t, d, master.IndexNMTStartup, 0, tc.startup)
			set32(t, d, master.IndexBootTime, 0, 300)
			boot := master.AssignSlave | master.AssignBoot
			assign(t, d, 5, master.NewAssignment(boot|master.AssignMandatory, 0, 0))
			assign(t, d, 6, master.NewAssignment(boot, 0, 0))
			consume(t, d, 1, 5, 150*time.Millisecond)
			consume(t, d, 2, 6, 150*time.Millisecond)
			// The manager's configuration survives its own resets in a
			// store (an NMT reset restores the defaults of 0x1F80..).
			if err := store.AddObjects(d); err != nil {
				t.Fatal(err)
			}
			r := startManager(t, buses, d, master.WithDictionary(d, node.WithStore(store.NewMemory())))
			if err := r.m.Node().SaveParameters(store.Communication); err != nil {
				t.Fatal(err)
			}
			r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
			waitState(t, n5, canopen.StateOperational)
			waitState(t, n6, canopen.StateOperational)
			// Supervision starts with the first heartbeat after the boot.
			nettest.Eventually(t, 2*time.Second, "supervised", func() bool {
				return r.m.Status(5).Active && r.m.Status(6).Active
			})
			switch tc.name {
			case "reset node":
				// The optional slave is lost and comes back: it is reset
				// and booted again.
				_ = n6.Close()
				r.rec.wait(t, "node error", kind(master.EventNodeError, 6))
				n6b := startNode(t, buses[3], nettest.Dict(t, 6, hb))
				nettest.Eventually(t, waitTimeout, "reboot", func() bool { return r.rec.count(bootOK(6)) == 2 })
				waitState(t, n6b, canopen.StateOperational)
			case "stop all":
				_ = n5.Close()
				r.rec.wait(t, "node error", kind(master.EventNodeError, 5))
				waitState(t, n6, canopen.StateStopped)
				waitState(t, r.m.Node(), canopen.StateStopped)
			case "reset all":
				resets := r.m.Node().Stats().Resets
				_ = n5.Close()
				r.rec.wait(t, "node error", kind(master.EventNodeError, 5))
				// The master resets and boots the network again, which
				// fails without the mandatory slave.
				r.rec.wait(t, "network boot failed", kind(master.EventNetworkBootFailed, 5))
				if r.m.Node().Stats().Resets == resets {
					t.Fatal("master not reset")
				}
			}
		})
	}
}

func TestManagerRequestObjects(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	n5 := startNode(t, buses[1], nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 }))
	d6 := nettest.Dict(t, 6, nil)
	n6 := startNode(t, buses[2], d6)
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	assign(t, d, 6, master.NewAssignment(master.AssignSlave|master.AssignBoot, 40*time.Millisecond, 3))
	r := startManager(t, buses, d)
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	// Node 6 has no heartbeat: node guarding started from its assignment.
	nettest.Eventually(t, 2*time.Second, "guarded", func() bool {
		b, _ := d.Read(master.IndexRequestGuarding, 6)
		return b[0] == 1
	})
	waitState(t, n5, canopen.StateOperational)
	tests := []struct {
		name  string
		index uint16
		sub   uint8
		val   uint8
		want  error
	}{
		{"stop 5", master.IndexRequestNMT, 5, 4, nil},
		{"invalid command", master.IndexRequestNMT, 5, 9, canopen.AbortInvalidValue},
		{"unlisted node", master.IndexRequestNMT, 9, 5, canopen.AbortInvalidValue},
		{"pre-operational all", master.IndexRequestNMT, 0x80, 127, nil},
		{"start all", master.IndexRequestNMT, 0x80, 5, nil},
		{"reset communication 5", master.IndexRequestNMT, 5, 7, nil},
		{"reset node 5", master.IndexRequestNMT, 5, 6, nil},
		{"guard 5 without guard time", master.IndexRequestGuarding, 5, 1, canopen.AbortDeviceState},
		{"invalid guarding value", master.IndexRequestGuarding, 6, 2, canopen.AbortInvalidValue},
		{"stop guarding all", master.IndexRequestGuarding, 0x80, 0, nil},
		{"start guarding all", master.IndexRequestGuarding, 0x80, 1, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := d.Write(tc.index, tc.sub, []byte{tc.val})
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.name == "stop 5" {
				waitState(t, n5, canopen.StateStopped)
				nettest.Eventually(t, time.Second, "1F82 stopped", func() bool {
					b, _ := d.Read(master.IndexRequestNMT, 5)
					return b[0] == byte(canopen.StateStopped)
				})
			}
		})
	}
	if b, _ := d.Read(master.IndexRequestGuarding, 5); b[0] != 0 {
		t.Fatal("node 5 guarded")
	}
	if b, _ := d.Read(master.IndexRequestGuarding, 0); b[0] != 128 {
		t.Fatalf("1F83:00 = %d", b[0])
	}
	// Losing the guarded node: a guarding event and a reset.
	_ = n6.Close()
	r.rec.wait(t, "guarding lost", kind(master.EventGuardingLost, 6))
	r.rec.wait(t, "node error", kind(master.EventNodeError, 6))
	n6b := startNode(t, buses[2], nettest.Dict(t, 6, nil))
	nettest.Eventually(t, waitTimeout, "reboot", func() bool { return r.rec.count(bootOK(6)) == 2 })
	waitState(t, n6b, canopen.StateOperational)
	if err := d.Write(master.IndexRequestGuarding, 6, []byte{0}); err != nil {
		t.Fatal(err)
	}
	// Not a master: requests refused.
	set32(t, d, master.IndexNMTStartup, 0, 0)
	if err := d.Write(master.IndexRequestNMT, 5, []byte{5}); !errors.Is(err, canopen.AbortLocalControl) {
		t.Fatal(err)
	}
	if err := d.Write(master.IndexRequestGuarding, 6, []byte{1}); !errors.Is(err, canopen.AbortLocalControl) {
		t.Fatal(err)
	}
}

func TestManagerSoftwareUpdate(t *testing.T) {
	img := bytes.Repeat([]byte("application v2 "), 100)
	want := crc32.ChecksumIEEE(img)
	full := master.AssignSlave | master.AssignBoot | master.AssignMandatory | master.AssignVersionCheck
	tests := []struct {
		name     string
		a        uint32
		expected uint32
		data     []byte
		status   master.BootStatus
		seq      string
	}{
		{"update", full | master.AssignSoftwareUpdate, want, img, master.BootOK, "stop,clear,download,start"},
		{"up to date", full | master.AssignSoftwareUpdate, crc32.ChecksumIEEE([]byte("v1")), img, master.BootOK, ""},
		{"update not allowed", full, want, img, master.BootUpdateNotAllowed, ""},
		{"update with keep-alive", full | master.AssignSoftwareUpdate | master.AssignKeepAlive, want, img, master.BootUpdateNotAllowed, ""},
		{"no expected identification", full | master.AssignSoftwareUpdate, 0, img, master.BootProgramObjects, ""},
		{"no program data", full | master.AssignSoftwareUpdate, want, nil, master.BootUpdateFailed, ""},
		{"wrong program", full | master.AssignSoftwareUpdate, want + 1, img, master.BootUpdateFailed, "stop,clear,download"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nettest.CheckLeaks(t)
			buses := testbus.Ports(t, 2)
			f := &flasher{image: []byte("v1")}
			n5 := startNode(t, buses[1], nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 }), node.WithProgram(program.Config{Handler: f}))
			_ = n5
			d := mgrDict(t)
			set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
			assign(t, d, 5, master.NewAssignment(tc.a, 0, 0))
			consume(t, d, 1, 5, 300*time.Millisecond)
			set32(t, d, program.IndexExpectedIdentification, 5, tc.expected)
			if tc.data != nil {
				_ = d.Set(program.IndexManagerData, 5, tc.data)
			}
			r := startManager(t, buses, d)
			ev := r.rec.wait(t, "boot end", func(e master.Event) bool {
				return (e.Kind == master.EventBootDone || e.Kind == master.EventBootFailed) && e.Node == 5
			})
			if ev.Status != tc.status {
				t.Fatalf("status %s (%v)", ev.Status, ev.Err)
			}
			if got := f.seq(); got != tc.seq {
				t.Fatalf("sequence %q", got)
			}
		})
	}
}

// flasher is a program handler keeping the image in memory; the
// identification is the CRC-32 of the image.
type flasher struct {
	mu    sync.Mutex
	calls []string
	image []byte
}

func (f *flasher) Control(_ uint8, cmd program.Command) error {
	f.mu.Lock()
	f.calls = append(f.calls, cmd.String())
	f.mu.Unlock()
	return nil
}

func (f *flasher) Download(_ uint8, data []byte) error {
	f.mu.Lock()
	f.calls = append(f.calls, "download")
	f.image = bytes.Clone(data)
	f.mu.Unlock()
	return nil
}

func (f *flasher) Identification(uint8) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return crc32.ChecksumIEEE(f.image)
}

func (f *flasher) seq() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func TestManagerAPI(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	if r.m.Node() != nil {
		t.Fatal("node without dictionary")
	}
	if err := r.m.BootNetwork(ctx); !errors.Is(err, master.ErrNoDictionary) {
		t.Fatal(err)
	}
	if err := r.m.BootSlave(ctx, 5); !errors.Is(err, master.ErrNoDictionary) {
		t.Fatal(err)
	}
	if _, done := r.m.SlaveStatus(5); done {
		t.Fatal("status without dictionary")
	}
	if _, err := master.New(r.buses[1], master.WithDictionary(mgrDict(t)), master.WithHeartbeat(1, time.Second)); err == nil {
		t.Fatal("WithHeartbeat and WithDictionary")
	}
	if _, err := master.New(r.buses[1], master.WithDictionary(od.New(0))); err == nil {
		t.Fatal("dictionary without node-ID")
	}

	buses := testbus.Ports(t, 2)
	d := mgrDict(t)
	m := startManager(t, buses, d, master.WithSDOOptions(sdo.WithTimeout(50*time.Millisecond)))
	if err := m.m.BootSlave(ctx, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	var be *master.BootError
	if err := m.m.BootSlave(ctx, 5); !errors.As(err, &be) || be.Status != master.BootNotListed {
		t.Fatalf("unlisted: %v", err)
	}
	if st, done := m.m.SlaveStatus(5); !done || st != master.BootNotListed {
		t.Fatalf("status %s %v", st, done)
	}
	// Without the boot bit only the error control starts.
	assign(t, d, 5, master.NewAssignment(master.AssignSlave, 0, 0))
	if err := m.m.BootSlave(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// An absent node with the boot bit: B, not retried by BootSlave.
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	if err := m.m.BootSlave(ctx, 5); !errors.As(err, &be) || be.Status != master.BootNoResponse {
		t.Fatalf("absent: %v", err)
	}
	// A node that answers but never sends a heartbeat: K.
	startNode(t, buses[1], nettest.Dict(t, 5, nil))
	consume(t, d, 1, 5, 50*time.Millisecond)
	if err := m.m.BootSlave(ctx, 5); !errors.As(err, &be) || be.Status != master.BootErrorControl {
		t.Fatalf("no heartbeat: %v", err)
	}
	if be.Unwrap() == nil || !strings.Contains(be.Error(), "K:") {
		t.Fatal(be)
	}
}

func TestBootStatusAndAssignment(t *testing.T) {
	for s := master.BootStatus('A'); s <= 'O'; s++ {
		if strings.HasPrefix(s.String(), "BootStatus") {
			t.Errorf("%c has no text", byte(s))
		}
	}
	if master.BootStatus(1).String() != "BootStatus(1)" || master.BootOK.String() != "0: booted" {
		t.Fatal("status text")
	}
	if !master.BootOperational.OK() || master.BootNoResponse.OK() {
		t.Fatal("OK")
	}
	if (&master.BootError{Node: 3, Status: master.BootNoResponse}).Error() == "" {
		t.Fatal("error text")
	}
	a := master.NewAssignment(master.AssignSlave|master.AssignMandatory, 250*time.Millisecond, 3)
	if !a.Has(master.AssignSlave) || a.Has(master.AssignBoot) || a.RetryFactor() != 3 || a.GuardTime() != 250*time.Millisecond {
		t.Fatalf("%v", a)
	}
	if a.String() != "slave|mandatory guard 250ms x3" {
		t.Fatal(a.String())
	}
	if master.Assignment(0).String() != "none" {
		t.Fatal(master.Assignment(0).String())
	}
	if master.NewAssignment(0, time.Hour, 0).GuardTime() != 65535*time.Millisecond || master.NewAssignment(0, -time.Second, 0) != 0 {
		t.Fatal("guard time clamp")
	}
	for _, k := range []master.EventKind{master.EventNetworkBootDone, master.EventNetworkBootFailed, master.EventGuardingLost, master.EventGuardingResumed, master.EventNodeError} {
		if strings.HasPrefix(k.String(), "EventKind") {
			t.Errorf("%d unnamed", k)
		}
	}
}

// TestManagerLateMandatory starts a mandatory slave after the manager: its
// boot slave process is retried until it answers.
func TestManagerLateMandatory(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	r := startManager(t, buses, d, master.WithSDOOptions(sdo.WithTimeout(40*time.Millisecond)))
	time.Sleep(fastBoot.ResetTimeout + 200*time.Millisecond)
	if _, ok := r.rec.find(kind(master.EventNetworkBootDone, masterID)); ok {
		t.Fatal("booted without the mandatory slave")
	}
	n5 := startNode(t, buses[1], nettest.Dict(t, 5, nil))
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	waitState(t, n5, canopen.StateOperational)
	if n := r.rec.count(bootOK(5)); n != 1 {
		t.Fatalf("boot events: %d; all: %v", n, r.rec.all())
	}
}

// TestManagerMinimal runs the manager on a dictionary with only 0x1F80 and
// 0x1F81, and boots slaves whose objects make the checks fail.
func TestManagerMinimal(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	// Node 5 has a device type and a vendor-ID only.
	d5 := od.New(5)
	_ = d5.Add(od.NewVar(od.IndexDeviceType, "Device type", canopen.Unsigned32, od.AccessRO, uint32(1)))
	_ = d5.Add(od.NewRecord(od.IndexIdentity, "Identity", od.Sub(1, "Vendor-ID", canopen.Unsigned32, od.AccessRO, uint32(2))))
	startNode(t, buses[1], d5)
	d6 := nettest.Dict(t, 6, nil)
	startNode(t, buses[2], d6)

	d, _ := od.Standard(od.Config{NodeID: masterID})
	full := mgrDict(t)
	for _, idx := range []uint16{master.IndexNMTStartup, master.IndexSlaveAssignment} {
		_ = d.Add(full.Object(idx))
	}
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	assign(t, d, 6, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	r := startManager(t, buses, d)
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	r.rec.wait(t, "boot 5", bootOK(5))

	// The checks need the objects of the full manager dictionary.
	var be *master.BootError
	ctx := nettest.Ctx(t)
	// Add the identity and program objects to the running manager.
	for _, idx := range []uint16{master.IndexExpectedProductCode, program.IndexExpectedIdentification} {
		_ = d.Add(full.Object(idx))
	}
	set32(t, d, master.IndexExpectedProductCode, 5, 3)
	if err := r.m.BootSlave(ctx, 5); !errors.As(err, &be) || be.Status != master.BootProductCode || !errors.Is(err, canopen.AbortNoSubIndex) {
		t.Fatalf("missing product code: %v", err)
	}
	set32(t, d, master.IndexExpectedProductCode, 5, 0)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignVersionCheck, 0, 0))
	set32(t, d, program.IndexExpectedIdentification, 5, 1)
	if err := r.m.BootSlave(ctx, 5); !errors.As(err, &be) || be.Status != master.BootProgramObjects {
		t.Fatalf("no program objects: %v", err)
	}
}

func TestManagerFailures(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	n5 := startNode(t, buses[1], nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 }))
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	consume(t, d, 1, 5, 150*time.Millisecond)
	concise(t, d, 5, cdcf.Entry{Index: nettest.IdxParam, Data: u32(1)})
	r := startManager(t, buses, d, master.WithStoreAfterConfigure(store.All))
	ctx := nettest.Ctx(t)
	// The slave has no 0x1010: the store request fails the configuration.
	ev := r.rec.wait(t, "boot failed", kind(master.EventBootFailed, 5))
	if ev.Status != master.BootConfiguration || !errors.Is(ev.Err, canopen.AbortNoObject) {
		t.Fatalf("store after configure: %v", ev)
	}
	// Busy while a process runs.
	assign(t, d, 9, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
	done := make(chan error, 1)
	go func() { done <- r.m.BootSlave(ctx, 9) }()
	nettest.Eventually(t, time.Second, "booting", func() bool {
		_, finished := r.m.SlaveStatus(9)
		return !finished
	})
	time.Sleep(10 * time.Millisecond)
	if err := r.m.BootSlave(ctx, 9); !errors.Is(err, canopen.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	<-done
	// Requests for unlisted nodes; sub-index 0 reads.
	if err := d.Write(master.IndexRequestGuarding, 20, []byte{1}); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Fatal(err)
	}
	if b, err := d.Read(master.IndexRequestNMT, 0); err != nil || b[0] != 128 {
		t.Fatalf("1F82:00 %v %v", b, err)
	}
	// Without the master bit, an error control event has no reaction.
	_ = r.m.Monitor(5, 100*time.Millisecond)
	nettest.Eventually(t, 2*time.Second, "supervised", func() bool { return r.m.Status(5).Active })
	set32(t, d, master.IndexNMTStartup, 0, 0)
	resets := n5.Stats().Resets
	_ = n5.Close()
	r.rec.wait(t, "lost", kind(master.EventHeartbeatLost, 5))
	time.Sleep(50 * time.Millisecond)
	if _, ok := r.rec.find(kind(master.EventNodeError, 5)); ok || n5.Stats().Resets != resets {
		t.Fatal("reaction without the master bit")
	}
	// The remote helpers on an invalid node-ID.
	if err := r.m.ConfigureConcise(ctx, 0, nil); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	if err := r.m.StoreParameters(ctx, 0, 1); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	if err := r.m.RestoreDefaults(ctx, 0, 1); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
	if err := r.m.DownloadProgram(ctx, 0, nil, master.ProgramOptions{}); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatal(err)
	}
}

// TestManagerGuardingResumed loses a guarded slave and gets it back: the
// guarding reports its return.
func TestManagerGuardingResumed(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	n6 := startNode(t, buses[1], nettest.Dict(t, 6, nil))
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 6, master.NewAssignment(master.AssignSlave|master.AssignBoot, 30*time.Millisecond, 2))
	r := startManager(t, buses, d)
	r.rec.wait(t, "boot", bootOK(6))
	waitState(t, n6, canopen.StateOperational)
	_ = n6.Close()
	r.rec.wait(t, "guarding lost", kind(master.EventGuardingLost, 6))
	startNode(t, buses[2], nettest.Dict(t, 6, nil))
	r.rec.wait(t, "guarding resumed", kind(master.EventGuardingResumed, 6))
}

// failingFlasher refuses or stalls as configured.
type failingFlasher struct {
	flasher
	failStart bool
	pending   bool
	// stopAfter stops the program again right after its start.
	stopAfter func()
}

func (f *failingFlasher) Control(n uint8, cmd program.Command) error {
	if f.failStart && cmd == program.Start {
		return canopen.AbortLocalControl
	}
	if cmd == program.Start && f.stopAfter != nil {
		go func() {
			time.Sleep(2 * time.Millisecond)
			f.stopAfter()
		}()
	}
	return f.flasher.Control(n, cmd)
}

func (f *failingFlasher) Download(n uint8, data []byte) error {
	_ = f.flasher.Download(n, data)
	if f.pending {
		return program.ErrPending
	}
	return nil
}

func TestDownloadProgramFailures(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2, master.WithBootTiming(fastBoot))
	ctx := nettest.Ctx(t)
	f := &failingFlasher{failStart: true}
	n5 := startNode(t, r.buses[1], nettest.Dict(t, 5, nil), node.WithProgram(program.Config{Handler: f}))
	dev := n5.Program()
	startNode(t, r.buses[2], nettest.Dict(t, 6, nil))
	opts := master.ProgramOptions{Timing: master.BootTiming{ProgramTimeout: 300 * time.Millisecond}}
	if err := r.m.DownloadProgram(ctx, 5, []byte{1}, opts); !errors.Is(err, canopen.AbortLocalControl) {
		t.Fatalf("start refused: %v", err)
	}
	f.failStart, f.pending = false, true
	if err := r.m.DownloadProgram(ctx, 5, []byte{1}, opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flash never done: %v", err)
	}
	// The flash reports an error after a pending download.
	dev.SetFlashStatus(1, 0)
	go func() {
		for !dev.FlashStatus(1).InProgress() {
			time.Sleep(time.Millisecond)
		}
		dev.SetFlashStatus(1, program.NewFlashStatus(false, program.FlashWriteError))
	}()
	if err := r.m.DownloadProgram(ctx, 5, []byte{1}, opts); !errors.Is(err, program.FlashWriteError) {
		t.Fatalf("flash error: %v", err)
	}
	// A program that does not start.
	f.pending = false
	f.stopAfter = func() { dev.SetState(1, program.Stopped) }
	if err := r.m.DownloadProgram(ctx, 5, []byte{1}, opts); !errors.Is(err, master.ErrProgram) {
		t.Fatalf("not started: %v", err)
	}
	// A node without the program objects.
	if err := r.m.DownloadProgram(ctx, 6, []byte{1}, opts); !errors.Is(err, canopen.AbortNoObject) {
		t.Fatalf("no objects: %v", err)
	}
}

// TestManagerCloseDuringBoot closes the manager while it waits for an
// absent mandatory slave.
func TestManagerCloseDuringBoot(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 1)
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	m, err := master.New(buses[0], master.WithDictionary(d), master.WithBootTiming(master.BootTiming{ResetTimeout: 10 * time.Millisecond, RetryDelay: 10 * time.Millisecond}),
		master.WithSDOOptions(sdo.WithTimeout(20*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if st, done := m.SlaveStatus(5); done {
		t.Fatalf("status %s", st)
	}
	_ = m.Close()
}
