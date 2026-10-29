// SPDX-License-Identifier: MIT

package node_test

import (
	"bytes"
	"errors"
	"hash/crc32"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/usdo"
)

// fdDict is a CANopen FD device dictionary whose TPDO 1 maps 0x2100
// sixteen times (64 bytes), event-driven.
func fdDict(t *testing.T, id canopen.NodeID) *od.Dictionary {
	t.Helper()
	d := nettest.Dict(t, id, func(c *od.Config) { c.MappingEntries = 16; c.HeartbeatMs = 50 })
	mi, ci := pdo.MappingIndex(true, 1), pdo.CommIndex(true, 1)
	cob := canopen.COBID(0x180 + uint32(id))
	set := func(index uint16, sub uint8, x uint64) {
		if err := d.SetUint(index, sub, x); err != nil {
			t.Fatal(err)
		}
	}
	set(ci, 1, uint64(cob|canopen.COBInvalid))
	set(mi, 0, 0)
	for i := 1; i <= 16; i++ {
		set(mi, uint8(i), uint64(nettest.IdxIn32)<<16|32)
	}
	set(mi, 0, 16)
	set(ci, 2, 255)
	set(ci, 1, uint64(cob))
	return d
}

// TestFDNetwork runs a CANopen FD master and node: FD boot-up and
// heartbeat, USDO in place of SDO for the master's procedures, a 64-byte
// PDO, a 20-byte EMCY and program download over USDO.
func TestFDNetwork(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2, virtual.WithFD())
	rec := &recorder{}
	m, err := master.New(buses[0], master.WithFD(true), master.WithEventHandler(rec.add),
		master.WithUSDOClient(0x7F, usdo.WithTimeout(300*time.Millisecond)),
		master.WithSDOOptions(sdo.WithTimeout(100*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := nettest.Ctx(t)
	d := fdDict(t, 5)
	f := &flasher{}
	var en enabled
	n := startNode(t, buses[1], d, en.option(), node.WithFD(true), node.WithProgram(program.Config{Handler: f}),
		node.WithUSDOServerOptions(usdo.WithMaxSessions(8)), node.WithEMCYOptions(emcy.WithFDInfo(1, 401)))
	if n.USDOServer() == nil {
		t.Fatal("no USDO server")
	}
	rec.wait(t, "FD boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 5 })

	// The master's procedures use USDO.
	found, err := m.Scan(ctx, []canopen.NodeID{5, 6})
	if err != nil || len(found) != 1 || found[0].Identity.SerialNumber != 5 {
		t.Fatal(found, err)
	}
	id, err := m.Identity(ctx, 5)
	if err != nil || id.VendorID != nettest.VendorID {
		t.Fatal(id, err)
	}
	if err := m.Configure(ctx, 5, []master.Entry{{Index: od.IndexProducerHeartbeat, Data: []byte{40, 0}}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(od.IndexProducerHeartbeat, 0); v != 40 {
		t.Fatal(v)
	}
	// Refusals arrive as SDO abort codes.
	if err := m.Configure(ctx, 5, []master.Entry{{Index: 0x5FFF, Data: []byte{1}}}); !errors.Is(err, canopen.AbortNoObject) {
		t.Fatalf("missing object: %v", err)
	}
	var ae *canopen.AbortError
	var ue *usdo.AbortError
	if _, err := m.Identity(ctx, 5); err != nil {
		t.Fatal(err)
	}
	err = m.ConfigureConcise(ctx, 5, []master.Entry{{Index: nettest.IdxIn32 + 0x1000, Data: []byte{1}}})
	if !errors.As(err, &ae) || !errors.As(err, &ue) || err.Error() == "" {
		t.Fatalf("abort: %v", err)
	}
	// No classical SDO server in FD mode.
	c, _ := m.SDO(5)
	if _, err := c.Upload(ctx, od.IndexDeviceType, 0); !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("SDO in FD mode: %v", err)
	}
	if _, err := m.USDO(); err != nil {
		t.Fatal(err)
	}

	// Program download over USDO (bulk).
	img := bytes.Repeat([]byte("fd-image"), 100)
	opts := fastProgram
	opts.Identification = crc32.ChecksumIEEE(img)
	if err := m.DownloadProgram(ctx, 5, img, opts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.running, img) {
		t.Fatal("image not running")
	}

	// 64-byte PDO.
	got := make(chan []pdo.Value, 4)
	mapping := make(pdo.Mapping, 16)
	for i := range mapping {
		mapping[i] = pdo.Entry{Index: nettest.IdxIn32, Bits: 32}
	}
	if _, err := m.Listen(0x185, mapping, d, func(fr *canbus.Frame, v []pdo.Value) {
		if fr.IsFD() && fr.Len == 64 {
			got <- v
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Monitor(5, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := m.StartNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := m.WaitState(ctx, 5, canopen.StateOperational); err != nil {
		t.Fatal(err)
	}
	if err := d.SetValue(nettest.IdxIn32, 0, int32(-7)); err != nil {
		t.Fatal(err)
	}
	// The TPDO is enabled by the state handler, just after the state
	// change the heartbeat reports.
	nettest.Eventually(t, 2*time.Second, "TPDO transmitted", func() bool { return n.TPDO(1).Transmit(ctx) == nil })
	// An earlier PDO (entering Operational) may come first.
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case v := <-got:
			done = len(v) == 16 && v[15].Value == int64(-7)
		case <-timeout:
			t.Fatal("no 64-byte PDO")
		}
	}

	// 20-byte EMCY.
	if err := n.EMCY().Raise(ctx, 0x5000, emcy.RegGeneric, [5]byte{1}); err != nil {
		t.Fatal(err)
	}
	ev := rec.wait(t, "FD EMCY", emcyFrom(5, 0x5000))
	if ev.EMCY.FD == nil || ev.EMCY.FD.CiASpec != 401 || ev.EMCY.FD.LogicalDevice != 1 {
		t.Fatalf("%+v", ev.EMCY)
	}
	if st := n.Stats(); st.USDO.Uploads == 0 || st.USDO.Downloads == 0 || st.Guard != (n.Stats().Guard) {
		t.Fatalf("%+v", st.USDO)
	}
	if st := m.Stats(); st.USDO.Uploads == 0 {
		t.Fatalf("%+v", st.USDO)
	}
	// The USDO server follows the NMT state: stopped, it does not answer.
	if err := m.StopNode(ctx, 5); err != nil {
		t.Fatal(err)
	}
	en.waitFor(t, canopen.StateStopped)
	if _, err := m.Identity(ctx, 5); !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("stopped node answered: %v", err)
	}
	// A reset keeps it serving, on a new node-ID.
	if err := n.SetNodeID(8); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetCommunication(ctx, 5); err != nil {
		t.Fatal(err)
	}
	rec.wait(t, "boot-up of node 8", func(e master.Event) bool { return e.Kind == master.EventBootUp && e.Node == 8 })
	if id, err := m.Identity(ctx, 8); err != nil || id.SerialNumber != 5 {
		t.Fatal(id, err)
	}
}

func TestUSDOWithoutFD(t *testing.T) {
	r := newRig(t, 0)
	if _, err := r.m.USDO(); !errors.Is(err, master.ErrNoUSDO) {
		t.Fatal(err)
	}
	_ = r.m.Close()
	if _, err := r.m.USDO(); !errors.Is(err, canopen.ErrClosed) {
		t.Fatal(err)
	}
}
