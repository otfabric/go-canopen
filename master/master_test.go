// SPDX-License-Identifier: MIT

package master_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

func TestScan(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 4, master.WithScanParallelism(32), master.WithScanOptions(sdo.WithTimeout(60*time.Millisecond)))
	ctx := nettest.Ctx(t)
	for i, id := range []canopen.NodeID{5, 6, 7} {
		startNode(t, r.buses[i+1], nettest.Dict(t, id, nil))
	}
	// Node 9 answers 0x1000 but has no identity object.
	d9 := od.New(9)
	_ = d9.Add(od.NewVar(od.IndexDeviceType, "Device type", canopen.Unsigned32, od.AccessRO, uint32(1)))
	startNode(t, r.buses[4], d9)
	for _, id := range []canopen.NodeID{5, 6, 7, 9} {
		r.rec.wait(t, "boot-up", kind(master.EventBootUp, id))
	}
	start := time.Now()
	found, err := r.m.Scan(ctx, nil)
	t.Logf("scan of 127 nodes: %v", time.Since(start))
	if err == nil || !strings.Contains(err.Error(), "node 9") {
		t.Fatalf("scan error %v", err)
	}
	if len(found) != 4 {
		t.Fatalf("found %+v", found)
	}
	for i, id := range []canopen.NodeID{5, 6, 7} {
		want := master.NodeInfo{Node: id, DeviceType: nettest.DeviceType, Identity: master.Identity{
			VendorID: nettest.VendorID, ProductCode: nettest.ProductCode, Revision: nettest.Revision, SerialNumber: uint32(id)}}
		if found[i] != want {
			t.Fatalf("node %d: %+v, want %+v", id, found[i], want)
		}
	}
	if found[3].Node != 9 || found[3].DeviceType != 1 {
		t.Fatalf("node 9: %+v", found[3])
	}
	// A subset, with an invalid node-ID.
	found, err = r.m.Scan(ctx, []canopen.NodeID{6, 8, 200})
	if len(found) != 1 || found[0].Node != 6 || err == nil || !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatalf("subset scan: %+v, %v", found, err)
	}
	id, err := r.m.Identity(ctx, 7)
	if err != nil || id.SerialNumber != 7 || !strings.Contains(id.String(), "0x00000F7A") {
		t.Fatalf("identity %v, %v", id, err)
	}
	if _, err := r.m.Identity(ctx, 0); err == nil {
		t.Fatal("identity of node 0")
	}
}

func TestScanRefusedAndCancelled(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	// Node 4 lacks 0x1000: it answers with an abort.
	d := od.New(4)
	_ = d.Add(od.NewVar(nettest.IdxParam, "x", canopen.Unsigned8, od.AccessRW, nil))
	startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 4))
	found, err := r.m.Scan(ctx, []canopen.NodeID{4})
	if len(found) != 0 || err == nil || !strings.Contains(err.Error(), "device type") {
		t.Fatalf("refusing node: %+v, %v", found, err)
	}
	cctx, cancel := contextWithCancel(ctx)
	cancel()
	if _, err := r.m.Scan(cctx, nil); err == nil {
		t.Fatal("cancelled scan without error")
	}
}

func TestConfigureRemapAndTraffic(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 5))
	in32 := pdo.Entry{Index: nettest.IdxIn32, Bits: 32}
	in8 := pdo.Entry{Index: nettest.IdxIn8, Bits: 8}
	out32 := pdo.Entry{Index: nettest.IdxOut32, Bits: 32}
	// Deliberately in an order that breaks the CiA sequence.
	entries := []master.Entry{
		{Index: 0x1A00, SubIndex: 1, Data: u32(in32.Uint32())},
		{Index: 0x1800, SubIndex: 1, Data: u32(0x185)},
		{Index: od.IndexProducerHeartbeat, Data: u16(25)},
		{Index: 0x1A00, SubIndex: 2, Data: u32(in8.Uint32())},
		{Index: 0x1A00, SubIndex: 0, Data: []byte{2}},
		{Index: 0x1800, SubIndex: 2, Data: []byte{255}},
		{Index: 0x1800, SubIndex: 3, Data: u16(0)},
		{Index: 0x1800, SubIndex: 5, Data: u16(0)},
		{Index: 0x1800, SubIndex: 6, Data: []byte{0}},
		{Index: 0x1600, SubIndex: 0, Data: []byte{1}},
		{Index: 0x1600, SubIndex: 1, Data: u32(out32.Uint32())},
		{Index: 0x1400, SubIndex: 1, Data: u32(0x205)},
		{Index: nettest.IdxParam, Data: u32(7)},
	}
	if err := r.m.Configure(ctx, 5, entries); err != nil {
		t.Fatal(err)
	}
	cfg, err := r.m.ReadPDO(ctx, 5, true, 1)
	if err != nil || cfg.COBID != 0x185 || len(cfg.Mapping) != 2 || cfg.Mapping[1] != in8 {
		t.Fatalf("TPDO 1 %+v, %v", cfg, err)
	}
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 25 {
		t.Fatalf("heartbeat %d", v)
	}
	var vals values
	if _, err := r.m.ListenTPDO(ctx, 5, 1, d, vals.handler); err != nil {
		t.Fatal(err)
	}
	snd, err := r.m.RPDOSender(ctx, 5, 1, d)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.SetValue(nettest.IdxIn32, 0, int32(-1000))
	_ = d.SetValue(nettest.IdxIn8, 0, uint8(9))
	if err := r.m.StartNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "TPDO", func() bool {
		v := vals.last()
		return v != nil && v[0] == int64(-1000) && v[1] == uint64(9)
	})
	if err := snd.Send(ctx, uint32(0xCAFEF00D)); err != nil {
		t.Fatal(err)
	}
	nettest.Eventually(t, 2*time.Second, "RPDO", func() bool {
		v, _ := d.GetUint(nettest.IdxOut32, 0)
		return v == 0xCAFEF00D
	})

	// Remapping an active PDO through Configure works in any state the
	// SDO server answers (the sequence invalidates it first).
	if err := r.m.PreOperational(ctx, 5); err != nil {
		t.Fatal(err)
	}
	waitState(t, n, canopen.StatePreOperational)
	if err := r.m.Configure(ctx, 5, []master.Entry{{Index: 0x1A00, SubIndex: 0, Data: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := r.m.ReadPDO(ctx, 5, true, 1); len(cfg.Mapping) != 1 || !cfg.Active() {
		t.Fatalf("remapped %+v", cfg)
	}

	// Failures name what failed.
	err = r.m.Configure(ctx, 5, []master.Entry{{Index: od.IndexDeviceType, Data: u32(1)}})
	if err == nil || !strings.Contains(err.Error(), "1000:00") {
		t.Fatalf("read-only write: %v", err)
	}
	err = r.m.Configure(ctx, 5, []master.Entry{{Index: 0x1A00, SubIndex: 1, Data: u32(0x30000008)}})
	if err == nil || !strings.Contains(err.Error(), "TPDO1") {
		t.Fatalf("bad mapping: %v", err)
	}
	err = r.m.Configure(ctx, 5, []master.Entry{{Index: 0x1A07, SubIndex: 0, Data: []byte{0}}})
	if err == nil || !strings.Contains(err.Error(), "TPDO 8") {
		t.Fatalf("absent PDO: %v", err)
	}
	err = r.m.Configure(ctx, 5, []master.Entry{{Index: 0x1A00, SubIndex: 0, Data: []byte{3}}})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty entry: %v", err)
	}
	if err := r.m.Configure(ctx, 0, nil); err == nil {
		t.Fatal("node 0")
	}
	if err := r.m.ConfigurePDO(ctx, 0, true, 1, pdo.Config{}); err == nil {
		t.Fatal("ConfigurePDO node 0")
	}
	if _, err := r.m.ReadPDO(ctx, 0, true, 1); err == nil {
		t.Fatal("ReadPDO node 0")
	}
}

func TestOverlay(t *testing.T) {
	base := pdo.Config{COBID: 0x185, TransmissionType: 1, Mapping: pdo.Mapping{{Index: 0x2000, Bits: 8}}}
	got, err := master.Overlay(base, []master.Entry{
		{Index: 0x1800, SubIndex: 0, Data: []byte{6}}, // ignored
		{Index: 0x1800, SubIndex: 3, Data: u16(10)},
		{Index: 0x1800, SubIndex: 4, Data: []byte{1}}, // ignored
		{Index: 0x1800, SubIndex: 5, Data: u16(20)},
		{Index: 0x1800, SubIndex: 6, Data: []byte{3}},
		{Index: 0x2000, SubIndex: 0, Data: []byte{3}}, // not a PDO object
		{Index: 0x1A00, SubIndex: 2, Data: u32(0x20010010)},
		{Index: 0x1A00, SubIndex: 0, Data: []byte{2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.InhibitTime != 10 || got.EventTimer != 20 || got.SyncStart != 3 || got.TransmissionType != 1 ||
		len(got.Mapping) != 2 || got.Mapping[1] != (pdo.Entry{Index: 0x2001, Bits: 16}) {
		t.Fatalf("overlay %+v", got)
	}
	if _, err := master.Overlay(base, []master.Entry{{Index: 0x1600, SubIndex: 65, Data: u32(1)}}); err == nil {
		t.Fatal("sub-index 65")
	}
	if _, err := master.Overlay(base, []master.Entry{{Index: 0x1600, SubIndex: 0, Data: []byte{65}}}); err == nil {
		t.Fatal("65 entries")
	}
	if s := (master.Entry{Index: 0x1017, Data: u16(1)}).String(); s != "1017:00=01 00" {
		t.Fatalf("entry string %q", s)
	}
}

func TestBootProcedure(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	boot := &master.BootConfig{
		DeviceType: nettest.DeviceType,
		Identity:   &master.Identity{VendorID: nettest.VendorID, ProductCode: nettest.ProductCode},
		Entries:    []master.Entry{{Index: od.IndexProducerHeartbeat, Data: u16(20)}},
		// Generous: the race detector slows the goroutines down.
		HeartbeatTimeout: 200 * time.Millisecond,
	}
	if err := r.m.SetBoot(5, boot); err != nil {
		t.Fatal(err)
	}
	bad := *boot
	bad.Identity = &master.Identity{VendorID: 1}
	_ = r.m.SetBoot(6, &bad)
	d5 := nettest.Dict(t, 5, nil)
	n5 := startNode(t, r.buses[1], d5)
	n6 := startNode(t, r.buses[2], nettest.Dict(t, 6, nil))
	r.rec.wait(t, "boot done", kind(master.EventBootDone, 5))
	waitState(t, n5, canopen.StateOperational)
	ev := r.rec.wait(t, "boot failed", kind(master.EventBootFailed, 6))
	if !errors.Is(ev.Err, master.ErrIdentity) || !strings.Contains(ev.String(), "boot-failed") {
		t.Fatalf("boot of node 6: %v", ev)
	}
	if n6.State() != canopen.StatePreOperational {
		t.Fatalf("node 6 state %s", n6.State())
	}
	if st := r.m.Status(5); st.Timeout != 200*time.Millisecond {
		t.Fatalf("supervision %+v", st)
	}
	r.rec.wait(t, "state change", func(e master.Event) bool {
		return e.Kind == master.EventStateChange && e.Node == 5 && e.State == canopen.StateOperational
	})

	// Heartbeat loss, and a new boot-up: the procedure runs again.
	_ = n5.Close()
	r.rec.wait(t, "heartbeat lost", kind(master.EventHeartbeatLost, 5))
	if r.m.State(5) != canopen.StateUnknown {
		t.Fatalf("state of a lost node %s", r.m.State(5))
	}
	n5 = startNode(t, r.buses[1], d5)
	r.rec.wait(t, "heartbeat resumed", kind(master.EventHeartbeatResumed, 5))
	nettest.Eventually(t, waitTimeout, "second boot", func() bool { return r.rec.count(kind(master.EventBootDone, 5)) == 2 })
	waitState(t, n5, canopen.StateOperational)

	// Device type mismatch, no start, removal.
	_ = r.m.SetBoot(6, &master.BootConfig{DeviceType: 1})
	if err := r.m.Boot(ctx, 6, master.BootConfig{DeviceType: 1}); !errors.Is(err, master.ErrIdentity) {
		t.Fatalf("device type check: %v", err)
	}
	if err := r.m.Boot(ctx, 6, master.BootConfig{NoStart: true, Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.SetBoot(6, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.m.SetBoot(0, nil); err == nil {
		t.Fatal("SetBoot node 0")
	}
	if err := r.m.Boot(ctx, 0, master.BootConfig{}); err == nil {
		t.Fatal("Boot node 0")
	}
	if err := r.m.Boot(ctx, 9, master.BootConfig{DeviceType: 1}); err == nil {
		t.Fatal("Boot of an absent node")
	}
	if err := r.m.Boot(ctx, 9, master.BootConfig{Identity: &master.Identity{}}); err == nil {
		t.Fatal("Boot of an absent node")
	}
	if err := r.m.Boot(ctx, 6, master.BootConfig{Entries: []master.Entry{{Index: 0x1000, Data: u32(0)}}}); err == nil {
		t.Fatal("Boot with a refused entry")
	}
}

func TestBootRestartedByNewBootUp(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	entries := make([]master.Entry, 50)
	for i := range entries {
		entries[i] = master.Entry{Index: nettest.IdxParam, Data: u32(uint32(i))}
	}
	_ = r.m.SetBoot(5, &master.BootConfig{Entries: entries})
	d := nettest.Dict(t, 5, nil)
	// The first procedure is held in its 10th write until the node has
	// entered Initialising for the reset (its SDO server is then
	// disabled): the procedure cannot complete before the second boot-up
	// message, whatever the scheduling.
	var writes atomic.Int32
	mid, release := make(chan struct{}), make(chan struct{})
	d.OnWrite(nettest.IdxParam, 0, func(*od.Variable, []byte) error {
		if writes.Add(1) == 10 {
			close(mid)
			<-release
		}
		return nil
	})
	n := startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 5))
	<-mid
	reset := make(chan error, 1)
	go func() { reset <- n.Reset(nettest.Ctx(t), nmt.ResetCommunication) }()
	nettest.Eventually(t, waitTimeout, "node initialising", func() bool { return n.State() == canopen.StateInitialising })
	close(release)
	if err := <-reset; err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "boot done", kind(master.EventBootDone, 5))
	waitState(t, n, canopen.StateOperational)
	time.Sleep(50 * time.Millisecond)
	if c := r.rec.count(kind(master.EventBootDone, 5)); c != 1 {
		t.Fatalf("%d boot done events", c)
	}
	// The interrupted procedure ends silently.
	if c := r.rec.count(kind(master.EventBootFailed, 5)); c != 0 {
		t.Fatalf("%d boot failed events", c)
	}
	if c := r.rec.count(kind(master.EventBootUp, 5)); c != 2 {
		t.Fatalf("%d boot-up events", c)
	}
}

// TestConfigureCOBIDChange applies a DCF with an absolute EMCY COB-ID to
// a node whose EMCY COB-ID is valid with another CAN-ID: the device
// refuses a direct change (CiA 301), so Configure invalidates the COB-ID
// first. SYNC and TIME COB-IDs in use are released the same way.
func TestConfigureCOBIDChange(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 2)
	ctx := nettest.Ctx(t)
	desc, err := eds.ParseFile("../eds/testdata/io401.eds")
	if err != nil {
		t.Fatal(err)
	}
	dcf, err := eds.ParseFile("../eds/testdata/node37.dcf")
	if err != nil {
		t.Fatal(err)
	}
	d, err := desc.Dictionary(21)
	if err != nil {
		t.Fatal(err)
	}
	startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 21))
	if v, _ := d.GetUint(od.IndexCOBIDEmcy, 0); v != 0x80+21 {
		t.Fatalf("EMCY COB-ID before %x", v)
	}
	entries, err := master.DCFEntries(dcf, 21)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.Configure(ctx, 21, entries); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(od.IndexCOBIDEmcy, 0); v != 0xA5 {
		t.Fatalf("EMCY COB-ID after %x", v)
	}

	// SYNC (generate) and TIME (consume and produce) in use.
	d2 := nettest.Dict(t, 22, nil)
	_ = d2.SetUint(od.IndexCOBIDSync, 0, uint64(canopen.COBGenerate|0x80))
	_ = d2.SetUint(od.IndexCOBIDTime, 0, uint64(canopen.COBGenerate|canopen.COBConsume|0x100))
	startNode(t, r.buses[2], d2)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 22))
	c, _ := r.m.SDO(22)
	if err := c.WriteUint32(ctx, od.IndexCOBIDSync, 0, uint32(canopen.COBGenerate|0x81)); !errors.Is(err, canopen.AbortDeviceState) {
		t.Fatalf("direct SYNC COB-ID change: %v", err)
	}
	syncCOB := uint32(canopen.COBGenerate | 0x81)
	tm := uint32(canopen.COBGenerate | canopen.COBConsume | 0x101)
	same := uint32(0x80 + 22) // EMCY unchanged: no release needed
	if err := r.m.Configure(ctx, 22, []master.Entry{
		{Index: od.IndexCOBIDSync, Data: u32(syncCOB)},
		{Index: od.IndexCOBIDTime, Data: u32(tm)},
		{Index: od.IndexCOBIDEmcy, Data: u32(same)},
	}); err != nil {
		t.Fatal(err)
	}
	for idx, want := range map[uint16]uint32{od.IndexCOBIDSync: syncCOB, od.IndexCOBIDTime: tm, od.IndexCOBIDEmcy: same} {
		if v, _ := d2.GetUint(idx, 0); uint32(v) != want {
			t.Fatalf("%04X = %x, want %x", idx, v, want)
		}
	}
}

func TestConfigureFromDCF(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	desc, err := eds.ParseFile("../eds/testdata/io401.eds")
	if err != nil {
		t.Fatal(err)
	}
	dcf, err := eds.ParseFile("../eds/testdata/node37.dcf")
	if err != nil {
		t.Fatal(err)
	}
	d, err := desc.Dictionary(0x25)
	if err != nil {
		t.Fatal(err)
	}
	startNode(t, r.buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 0x25))
	entries, err := master.DCFEntries(dcf, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Logf("%s", e)
	}
	if err := r.m.ConfigureFromDCF(ctx, 0, dcf); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 500 {
		t.Fatalf("heartbeat %d", v)
	}
	if v, _ := d.GetValue(0x2100, 0); v != int64(-3) {
		t.Fatalf("0x2100 = %v", v)
	}
	if v, _ := d.GetValue(0x2000, 0); v != float32(12.5) {
		t.Fatalf("0x2000 = %v", v)
	}
	cfg, err := pdo.Load(d, true, 1)
	if err != nil || cfg.EventTimer != 100 || len(cfg.Mapping) != 1 || cfg.COBID.ID() != 0x1A5 {
		t.Fatalf("TPDO 1 %+v, %v", cfg, err)
	}
	if _, err := master.DCFEntries(dcf, 200); err == nil {
		t.Fatal("node 200")
	}
	if err := r.m.ConfigureFromDCF(ctx, 200, dcf); err == nil {
		t.Fatal("node 200")
	}
	bad := *dcf
	bad.Objects = append([]*eds.Object{{Index: 0x1000, Entry: eds.Entry{ObjectType: od.ObjectVar}}}, dcf.Objects[1:]...)
	if _, err := master.DCFEntries(&bad, 0x25); err == nil {
		t.Fatal("broken DCF")
	}
}

func TestEventsAndLifecycle(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	r := newRigOn(t, buses, master.WithHeartbeat(1, 20*time.Millisecond), master.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 })
	// The node supervises the master's heartbeat.
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, uint64(nmt.ConsumerEntry(1, 300*time.Millisecond)))
	n := startNode(t, buses[1], d)
	r.rec.wait(t, "boot-up", kind(master.EventBootUp, 5))
	nettest.Eventually(t, 2*time.Second, "master heard", func() bool { return n.HeartbeatConsumer().Status(1).Active })
	_ = n.EMCY().Raise(ctx, emcy.CodeVoltage, emcy.RegVoltage, [5]byte{1, 2, 3, 4, 5})
	ev := r.rec.wait(t, "EMCY", kind(master.EventEMCY, 5))
	if ev.EMCY.Code != emcy.CodeVoltage || !strings.Contains(ev.String(), "emcy") {
		t.Fatalf("event %v", ev)
	}
	if last, ok := r.m.LastEMCY(5); !ok || last.Code != emcy.CodeVoltage {
		t.Fatalf("last EMCY %+v", last)
	}
	if err := r.m.Monitor(5, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.m.WaitState(ctx, 5, canopen.StatePreOperational); err != nil {
		t.Fatal(err)
	}
	if len(r.m.Nodes()) == 0 {
		t.Fatal("no nodes")
	}
	st := r.m.Stats()
	if st.EMCY.Received == 0 || st.Heartbeat.BootUps == 0 {
		t.Fatalf("stats %+v", st)
	}
	if _, err := json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Fatalf("second Start: %v", err)
	}
	if _, err := r.m.SDO(0); err == nil {
		t.Fatal("SDO(0)")
	}
	if _, err := r.m.ListenTPDO(ctx, 5, 1, d, nil); !errors.Is(err, pdo.ErrInactive) {
		t.Fatalf("listen to an empty TPDO: %v", err)
	}
	if _, err := r.m.RPDOSender(ctx, 5, 1, d); !errors.Is(err, pdo.ErrInactive) {
		t.Fatalf("sender of an empty RPDO: %v", err)
	}
	if _, err := r.m.ListenTPDO(ctx, 0, 1, d, nil); err == nil {
		t.Fatal("ListenTPDO node 0")
	}
	if _, err := r.m.RPDOSender(ctx, 0, 1, d); err == nil {
		t.Fatal("RPDOSender node 0")
	}
	badMap := pdo.Mapping{{Index: 0x5555, Bits: 8}}
	if _, err := r.m.Listen(0x185, badMap, d, nil); err == nil {
		t.Fatal("Listen with an unknown object")
	}
	if _, err := r.m.Sender(0x205, badMap, d); err == nil {
		t.Fatal("Sender with an unknown object")
	}
	if r.m.SYNC() == nil || r.m.TIME() == nil {
		t.Fatal("producers")
	}

	// Close: the node loses the master's heartbeat.
	_ = r.m.Close()
	_ = r.m.Close()
	nettest.Eventually(t, 3*time.Second, "master lost", func() bool {
		reg, _ := d.GetUint(od.IndexErrorRegister, 0)
		return reg&uint64(emcy.RegCommunication) != 0
	})
	if err := r.m.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatalf("Start after Close: %v", err)
	}
	if _, err := r.m.SDO(5); !errors.Is(err, canopen.ErrClosed) {
		t.Fatalf("SDO after Close: %v", err)
	}
	if _, err := r.m.Listen(0x185, pdo.Mapping{{Index: nettest.IdxIn8, Bits: 8}}, d, nil); !errors.Is(err, canopen.ErrClosed) {
		t.Fatalf("Listen after Close: %v", err)
	}
	if _, err := master.New(nil); err == nil {
		t.Fatal("New(nil)")
	}
	// A master that was never started closes cleanly.
	m2, err := master.New(buses[0], master.WithScanParallelism(0))
	if err != nil {
		t.Fatal(err)
	}
	_ = m2.Close()
	if _, err := master.New(buses[0], master.WithHeartbeat(0, time.Second)); err == nil {
		t.Fatal("heartbeat with node-ID 0")
	}
}

func TestEventStrings(t *testing.T) {
	for k := master.EventBootUp; k <= master.EventBootFailed+1; k++ {
		if k.String() == "" {
			t.Fatal("empty kind name")
		}
	}
	evs := []master.Event{
		{Kind: master.EventBootUp, Node: 1},
		{Kind: master.EventStateChange, Node: 1, Previous: canopen.StatePreOperational, State: canopen.StateOperational},
		{Kind: master.EventBootFailed, Node: 1, Err: fmt.Errorf("x")},
		{Kind: master.EventEMCY, Node: 1, EMCY: &emcy.Event{}},
	}
	for _, e := range evs {
		if !strings.Contains(e.String(), "node 1") {
			t.Fatalf("%q", e.String())
		}
	}
}
