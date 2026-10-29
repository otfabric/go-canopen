// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"hash/crc32"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/mpdo"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
	"github.com/otfabric/go-canopen/usdo"
)

// opener returns a new participant's bus on the test network: classical
// CAN, or CAN FD when fd is set.
type opener func(t *testing.T, fd bool) canopen.Bus

// scenarios are the end-to-end scenarios of the features of v0.3.0, run
// on the virtual bus and over SocketCAN.
var scenarios = []struct {
	name string
	run  func(t *testing.T, open opener)
}{
	{"LSSNetworkBoot", runLSSNetworkBoot},
	{"MPDO", runMPDO},
	{"FDNetwork", runFDNetwork},
	{"SyncWindow", runSyncWindow},
	{"StoreRestart", runStoreRestart},
	{"ProgramDownload", runProgramDownload},
}

// newMaster starts a master on a new bus.
func newMaster(t *testing.T, bus canopen.Bus, opts ...master.Option) (*master.Master, *events) {
	t.Helper()
	ev := &events{}
	opts = append([]master.Option{master.WithEventHandler(ev.add),
		master.WithSDOOptions(sdo.WithTimeout(300*time.Millisecond), sdo.WithRetries(2))}, opts...)
	m, err := master.New(bus, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, ev
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitNode(t *testing.T, n *node.Node, st canopen.NMTState) {
	t.Helper()
	nettest.Eventually(t, 10*time.Second, "node state "+st.String(), func() bool { return n.State() == st })
}

func waitValue(t *testing.T, d *od.Dictionary, index uint16, want uint64) {
	t.Helper()
	nettest.Eventually(t, 10*time.Second, "value", func() bool {
		v, _ := d.GetUint(index, 0)
		return v == want
	})
}

// enabled reports when a node's services have been enabled for
// Operational: the node's state handler runs after that, while the state
// (and so the heartbeat) changes just before.
type enabled struct{ op atomic.Bool }

func (e *enabled) option() node.Option {
	return node.WithStateHandler(func(sc nmt.StateChange) { e.op.Store(sc.To == canopen.StateOperational) })
}

func (e *enabled) wait(t *testing.T) {
	t.Helper()
	nettest.Eventually(t, 10*time.Second, "services enabled", e.op.Load)
}

// runLSSNetworkBoot gives three unconfigured devices node-IDs with LSS
// Fastscan (AssignAll), then boots them with the CiA 302 network boot-up,
// configuring each from its concise DCF.
func runLSSNetworkBoot(t *testing.T, open opener) {
	ctx := nettest.Ctx(t)
	serials := []uint32{0x1001, 0x1002, 0x1003}
	var nodes []*node.Node
	for _, s := range serials {
		d := nettest.Dict(t, lss.Unconfigured, func(c *od.Config) { c.SerialNumber = s })
		must(t, store.AddObjects(d))
		nodes = append(nodes, startNode(t, open(t, false), d, node.WithLSS(), node.WithStore(store.NewMemory())))
	}
	md, err := od.Standard(od.Config{NodeID: 1, HeartbeatConsumers: 4})
	must(t, err)
	must(t, master.AddManagerObjects(md))
	ids := []canopen.NodeID{5, 6, 7}
	for _, id := range ids {
		must(t, md.SetUint(master.IndexSlaveAssignment, uint8(id), uint64(master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))))
		b, err := cdcf.Encode([]cdcf.Entry{
			{Index: od.IndexProducerHeartbeat, Data: u16(50)},
			{Index: nettest.IdxParam, Data: u32(0xA000 + uint32(id))},
		})
		must(t, err)
		must(t, md.Set(cdcf.IndexConciseDCF, uint8(id), b))
	}
	m, ev := newMaster(t, open(t, false), master.WithDictionary(md),
		master.WithBootTiming(master.BootTiming{RetryDelay: 50 * time.Millisecond, ResetTimeout: time.Second}),
		master.WithLSSOptions(lss.WithFastscanTimeout(20*time.Millisecond)))
	fs := lss.FastscanOptions{Known: map[lss.Part]uint32{
		lss.PartVendor: nettest.VendorID, lss.PartProduct: nettest.ProductCode, lss.PartRevision: nettest.Revision,
	}}
	got, err := m.LSS().AssignAll(ctx, 5, lss.AssignFastscan(fs), lss.AssignStore())
	must(t, err)
	if len(got) != 3 {
		t.Fatalf("assignments %v", got)
	}
	// Every device has one of the node-IDs, each a different one (the
	// order follows the Fastscan, lowest address first unless a late
	// answer made it pick another).
	byID := map[canopen.NodeID]*node.Node{}
	for _, a := range got {
		ev.wait(t, "boot-up", 1, is(master.EventBootUp, a.NodeID))
		for i, s := range serials {
			if s == a.Address.Serial {
				nettest.Eventually(t, 10*time.Second, "node-ID", func() bool { return nodes[i].NodeID() == a.NodeID })
				byID[a.NodeID] = nodes[i]
			}
		}
	}
	if len(byID) != 3 || byID[5] == nil || byID[6] == nil || byID[7] == nil {
		t.Fatalf("assignments %v", got)
	}
	must(t, md.SetUint(master.IndexNMTStartup, 0, uint64(master.StartupMaster)))
	must(t, m.BootNetwork(ctx))
	for _, id := range ids {
		ev.wait(t, "boot done", 1, is(master.EventBootDone, id))
		waitNode(t, byID[id], canopen.StateOperational)
		waitValue(t, byID[id].Dictionary(), nettest.IdxParam, 0xA000+uint64(id))
	}
}

// makeMPDO configures PDO n of d locally as an MPDO.
func makeMPDO(t *testing.T, d *od.Dictionary, transmit bool, n int, mode uint8, cob canopen.COBID) {
	t.Helper()
	ci, mi := pdo.CommIndex(transmit, n), pdo.MappingIndex(transmit, n)
	must(t, d.SetUint(ci, 1, uint64(cob|canopen.COBInvalid)))
	must(t, d.SetUint(mi, 0, 0))
	must(t, d.SetUint(ci, 2, uint64(pdo.TransEventManufacturer)))
	must(t, d.SetUint(mi, 0, uint64(mode)))
	must(t, d.SetUint(ci, 1, uint64(cob)))
}

// runMPDO sends DAM- and SAM-MPDOs between two nodes; the dispatcher list
// is written by the master.
func runMPDO(t *testing.T, open opener) {
	ctx := nettest.Ctx(t)
	m, _ := newMaster(t, open(t, false))
	da, db := nettest.Dict(t, 5, nil), nettest.Dict(t, 6, nil)
	makeMPDO(t, da, true, 1, mpdo.MappingDAM, 0x185)
	makeMPDO(t, db, false, 1, mpdo.MappingDAM, 0x185)
	makeMPDO(t, da, true, 2, mpdo.MappingSAM, 0x285)
	makeMPDO(t, db, false, 2, mpdo.MappingSAM, 0x285)
	sl, err := mpdo.NewScannerList(1, 2, mpdo.ScannerEntry{Index: nettest.IdxOut32})
	must(t, err)
	must(t, da.Add(sl))
	dl, err := mpdo.NewDispatcherList(1, 2)
	must(t, err)
	must(t, db.Add(dl))
	var eb enabled
	a := startNode(t, open(t, false), da)
	b := startNode(t, open(t, false), db, eb.option())
	waitNode(t, b, canopen.StatePreOperational)
	must(t, m.WriteDispatcherList(ctx, 6, 1, []mpdo.DispatcherEntry{{Producer: 5, ProducerIndex: nettest.IdxOut32, Index: nettest.IdxOut32}}))
	must(t, m.StartNode(ctx, 0))
	waitNode(t, a, canopen.StateOperational)
	eb.wait(t)
	// The producer's services are enabled just after the state change.
	nettest.Eventually(t, 5*time.Second, "DAM sent", func() bool {
		return a.MPDOProducer(1).SendDAM(ctx, 6, nettest.IdxOut16, 0, u16(0xBEEF)) == nil
	})
	waitValue(t, db, nettest.IdxOut16, 0xBEEF)
	must(t, da.SetUint(nettest.IdxOut32, 0, 0x01020304))
	must(t, a.MPDOProducer(2).Send(ctx, nettest.IdxOut32, 0))
	waitValue(t, db, nettest.IdxOut32, 0x01020304)
}

// runFDNetwork runs a CANopen FD network: a master and two nodes with
// USDO servers, 64-byte PDOs between the nodes and FD emergencies.
func runFDNetwork(t *testing.T, open opener) {
	ctx := nettest.Ctx(t)
	m, ev := newMaster(t, open(t, true), master.WithFD(true), master.WithUSDOClient(0x7F, usdo.WithTimeout(500*time.Millisecond)))
	src := nettest.Dict(t, 5, func(c *od.Config) { c.MappingEntries = 16 })
	dst := nettest.Dict(t, 6, func(c *od.Config) { c.MappingEntries = 16 })
	ns := startNode(t, open(t, true), src, node.WithFD(true))
	var ed enabled
	nd := startNode(t, open(t, true), dst, node.WithFD(true), ed.option())
	ev.wait(t, "boot-up 5", 1, is(master.EventBootUp, 5))
	ev.wait(t, "boot-up 6", 1, is(master.EventBootUp, 6))
	// The master maps 16 x 32 bits (64 bytes) over USDO: TPDO 1 of node 5
	// to RPDO 1 of node 6.
	tx, rx := make(pdo.Mapping, 16), make(pdo.Mapping, 16)
	for i := range tx {
		tx[i] = pdo.Entry{Index: nettest.IdxIn32, Bits: 32}
		rx[i] = pdo.Entry{Index: nettest.IdxOut32, Bits: 32}
	}
	must(t, m.ConfigurePDO(ctx, 5, true, 1, pdo.Config{COBID: 0x185, TransmissionType: 255, Mapping: tx}))
	must(t, m.ConfigurePDO(ctx, 6, false, 1, pdo.Config{COBID: 0x185, TransmissionType: 255, Mapping: rx}))
	got, err := m.ReadPDO(ctx, 6, false, 1)
	if err != nil || len(got.Mapping) != 16 {
		t.Fatal(got, err)
	}
	must(t, m.StartNode(ctx, 0))
	waitNode(t, ns, canopen.StateOperational)
	ed.wait(t)
	must(t, src.SetValue(nettest.IdxIn32, 0, int32(0x11223344)))
	// The TPDO is enabled by the state handler, just after the state change.
	nettest.Eventually(t, 5*time.Second, "TPDO transmitted", func() bool { return ns.TPDO(1).Transmit(ctx) == nil })
	waitValue(t, dst, nettest.IdxOut32, 0x11223344)
	must(t, ns.EMCY().Raise(ctx, 0x6100, emcy.RegGeneric, [5]byte{9}))
	nettest.Eventually(t, 10*time.Second, "FD EMCY", func() bool {
		e, ok := m.LastEMCY(5)
		return ok && e.FD != nil && e.Code == 0x6100
	})
	if st := nd.Stats(); st.USDO.Downloads == 0 || st.RPDO[1].Applied == 0 {
		t.Fatalf("usdo %+v rpdo %+v", st.USDO, st.RPDO[1])
	}
}

// runSyncWindow checks the synchronous window 0x1007: a synchronous RPDO
// arriving after the window is discarded.
func runSyncWindow(t *testing.T, open opener) {
	ctx := nettest.Ctx(t)
	m, _ := newMaster(t, open(t, false))
	bus := open(t, false)
	d := nettest.Dict(t, 5, nil)
	must(t, d.SetUint(od.IndexSyncWindow, 0, 100_000))
	n := startNode(t, open(t, false), d)
	waitNode(t, n, canopen.StatePreOperational)
	must(t, m.ConfigurePDO(ctx, 5, false, 1, pdo.Config{COBID: 0x205, TransmissionType: 1,
		Mapping: pdo.Mapping{{Index: nettest.IdxOut8, Bits: 8}}}))
	must(t, m.StartNode(ctx, 5))
	waitNode(t, n, canopen.StateOperational)
	send := func(v byte) {
		f, _ := canbus.NewStandard(0x205, []byte{v})
		must(t, bus.Send(ctx, &f))
	}
	syncs := func(k uint64) {
		must(t, m.SYNC().Send(ctx))
		nettest.Eventually(t, 5*time.Second, "SYNC", func() bool { return n.Stats().SYNC.Received >= k })
	}
	syncs(1)
	time.Sleep(250 * time.Millisecond)
	send(7)
	nettest.Eventually(t, 5*time.Second, "outside window", func() bool { return n.Stats().RPDO[1].OutsideWindow == 1 })
	syncs(2)
	send(9)
	nettest.Eventually(t, 5*time.Second, "received", func() bool { return n.Stats().RPDO[1].Received == 2 })
	syncs(3)
	waitValue(t, d, nettest.IdxOut8, 9)
}

// runStoreRestart saves parameters over the network, restarts the node on
// a fresh dictionary and finds them again; restoring the defaults takes
// effect at the next restart.
func runStoreRestart(t *testing.T, open opener) {
	ctx := nettest.Ctx(t)
	m, ev := newMaster(t, open(t, false))
	s := store.NewFile(t.TempDir() + "/node5.cdcf")
	dict := func() *od.Dictionary {
		d := nettest.Dict(t, 5, nil)
		must(t, store.AddObjects(d))
		return d
	}
	bus := open(t, false)
	n, err := node.New(bus, dict(), node.WithStore(s))
	must(t, err)
	must(t, n.Start())
	ev.wait(t, "boot-up", 1, is(master.EventBootUp, 5))
	must(t, m.Configure(ctx, 5, []master.Entry{{Index: nettest.IdxParam, Data: u32(0xC0FFEE)}}))
	must(t, m.StoreParameters(ctx, 5, store.All))
	must(t, n.Close())
	d2 := dict()
	n2 := startNode(t, bus, d2, node.WithStore(s))
	ev.wait(t, "second boot-up", 2, is(master.EventBootUp, 5))
	if v, _ := d2.GetUint(nettest.IdxParam, 0); v != 0xC0FFEE {
		t.Fatalf("stored value lost: %#x", v)
	}
	must(t, m.RestoreDefaults(ctx, 5, store.All))
	must(t, m.ResetNode(ctx, 5))
	ev.wait(t, "third boot-up", 3, is(master.EventBootUp, 5))
	waitValue(t, d2, nettest.IdxParam, 0x12345678)
	_ = n2
}

// memFlasher keeps the program image in memory.
type memFlasher struct {
	mu      sync.Mutex
	image   []byte
	running []byte
}

func (f *memFlasher) Control(_ uint8, cmd program.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cmd == program.Start {
		f.running = f.image
	}
	return nil
}

func (f *memFlasher) Download(_ uint8, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.image = bytes.Clone(data)
	return nil
}

func (f *memFlasher) Identification(uint8) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return crc32.ChecksumIEEE(f.image)
}

// runProgramDownload replaces a node's program (CiA 302-3) with SDO block
// transfer.
func runProgramDownload(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(nettest.Ctx(t), 30*time.Second)
	defer cancel()
	m, _ := newMaster(t, open(t, false))
	f := &memFlasher{}
	n := startNode(t, open(t, false), nettest.Dict(t, 5, nil), node.WithProgram(program.Config{Handler: f}))
	waitNode(t, n, canopen.StatePreOperational)
	img := bytes.Repeat([]byte("e2e-firmware-"), 300)
	must(t, m.DownloadProgram(ctx, 5, img, master.ProgramOptions{
		Identification: crc32.ChecksumIEEE(img),
		Timing:         master.BootTiming{PollInterval: 10 * time.Millisecond},
	}))
	f.mu.Lock()
	ok := bytes.Equal(f.running, img)
	f.mu.Unlock()
	if !ok || n.Program().State(1) != program.Started {
		t.Fatal("program not running")
	}
}
