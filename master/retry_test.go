// SPDX-License-Identifier: MIT

package master_test

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
)

// TestManagerIdentityTimeoutRetried is the regression test of a boot
// slave process that reported an identity mismatch (status D) when the
// read of 0x1018 timed out once on a loaded system: the identity reads
// are retried on timeout like the read of 0x1000.
func TestManagerIdentityTimeoutRetried(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	d5 := nettest.Dict(t, 5, nil)
	var slow atomic.Int32
	slow.Store(1)
	d5.OnRead(od.IndexIdentity, 1, func(_ *od.Variable, stored []byte) ([]byte, error) {
		if slow.Add(-1) == 0 {
			time.Sleep(300 * time.Millisecond) // beyond the SDO timeout (150 ms)
		}
		return stored, nil
	})
	startNode(t, buses[1], d5)
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	set32(t, d, master.IndexExpectedVendorID, 5, nettest.VendorID)
	r := startManager(t, buses, d)
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	if ev := r.rec.wait(t, "boot done", bootOK(5)); ev.Status != master.BootOK {
		t.Fatalf("status %s", ev.Status)
	}
	if slow.Load() >= 0 {
		t.Fatal("the slow read did not happen")
	}
}

// TestManagerConfigTimeoutRetried is the regression test of a boot slave
// process that failed with status J when a write of the configuration
// step timed out once on a loaded system: the restore request, every
// concise DCF entry and the store request are repeated on timeout.
func TestManagerConfigTimeoutRetried(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	d5 := nettest.Dict(t, 5, nil)
	if err := store.AddObjects(d5); err != nil {
		t.Fatal(err)
	}
	slowOnce := func(index uint16) *atomic.Int32 {
		var n atomic.Int32
		n.Store(1)
		d5.OnWrite(index, -1, func(*od.Variable, []byte) error {
			if n.Add(-1) == 0 {
				time.Sleep(300 * time.Millisecond) // beyond the SDO timeout (150 ms)
			}
			return nil
		})
		return &n
	}
	slow := []*atomic.Int32{slowOnce(store.IndexRestore), slowOnce(nettest.IdxParam), slowOnce(store.IndexStore)}
	startNode(t, buses[1], d5, node.WithStore(store.NewMemory()))
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory|master.AssignRestore, 0, 0))
	set32(t, d, master.IndexRestoreConfiguration, 5, 1)
	concise(t, d, 5, cdcf.Entry{Index: nettest.IdxParam, Data: u32(0x77)})
	r := startManager(t, buses, d, master.WithStoreAfterConfigure(store.All))
	r.rec.wait(t, "network boot", kind(master.EventNetworkBootDone, masterID))
	if ev := r.rec.wait(t, "boot done", bootOK(5)); ev.Status != master.BootOK {
		t.Fatalf("status %s", ev.Status)
	}
	for i, n := range slow {
		if n.Load() >= 0 {
			t.Fatalf("slow write %d did not happen", i)
		}
	}
	if v, _ := d5.GetUint(nettest.IdxParam, 0); v != 0x77 {
		t.Fatalf("param %#x", v)
	}
}

// TestBootNetworkSuperseded is the regression test of BootNetwork calls
// that race each other (or the automatic network boot-up at the master's
// boot-up): the call that is superseded does not fail with "context
// canceled" but reports the result of the network boot-up that replaced
// it.
func TestBootNetworkSuperseded(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	startNode(t, buses[1], nettest.Dict(t, 5, nil))
	d := mgrDict(t)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	r := startManager(t, buses, d) // 0x1F80 bit 0 clear: no automatic boot-up
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	ctx := nettest.Ctx(t)
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errs <- r.m.BootNetwork(ctx) }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	r.rec.wait(t, "boot done", bootOK(5))
	// A boot-up that fails is reported to the superseded calls as well.
	set32(t, d, master.IndexExpectedVendorID, 5, 0xBAD)
	for i := 0; i < 2; i++ {
		go func() { errs <- r.m.BootNetwork(ctx) }()
	}
	for i := 0; i < 2; i++ {
		var be *master.BootError
		if err := <-errs; !errors.As(err, &be) || be.Status != master.BootVendorID {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

// TestBootNetworkCancelsRunningBoot is the regression test of a boot slave
// process that runs when a network boot-up starts (it began at a boot-up
// message that arrived just before): the network boot-up's reset undid
// the configuration it wrote, and its error control check then failed
// with status K (heartbeat producer back at 0). The network boot-up now
// cancels it before the reset, and the slave boots again after it.
func TestBootNetworkCancelsRunningBoot(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	d7 := nettest.Dict(t, 7, nil)
	configuring := make(chan struct{})
	var once sync.Once
	remove := d7.OnWrite(nettest.IdxParam, 0, func(*od.Variable, []byte) error {
		once.Do(func() {
			close(configuring)
			time.Sleep(100 * time.Millisecond) // the network boot-up starts meanwhile
		})
		return nil
	})
	defer remove()
	startNode(t, buses[1], d7)
	d := mgrDict(t)
	assign(t, d, 7, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	consume(t, d, 1, 7, 500*time.Millisecond)
	concise(t, d, 7,
		cdcf.Entry{Index: od.IndexProducerHeartbeat, Data: u16(30)},
		cdcf.Entry{Index: nettest.IdxParam, Data: u32(0xBEEF)})
	r := startManager(t, buses, d) // 0x1F80 bit 0 clear: no automatic boot-up
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	ctx := nettest.Ctx(t)
	// A boot slave process at the slave's boot-up message.
	if err := r.m.ResetCommunication(ctx, 7); err != nil {
		t.Fatal(err)
	}
	select {
	case <-configuring:
	case <-ctx.Done():
		t.Fatal("no boot slave process configured node 7")
	}
	if err := r.m.BootNetwork(ctx); err != nil {
		t.Fatalf("network boot-up: %v; events %v", err, r.rec.all())
	}
	ev := r.rec.wait(t, "boot done", bootOK(7))
	if ev.Status != master.BootOK {
		t.Fatalf("status %s", ev.Status)
	}
	if v, _ := d7.GetUint(od.IndexProducerHeartbeat, 0); v != 30 {
		t.Errorf("0x1017 = %d after the network boot-up", v)
	}
}

// logSignal is a log handler that closes seen at a message.
type logSignal struct {
	msg  string
	seen chan struct{}
	once sync.Once
}

func (l *logSignal) Enabled(context.Context, slog.Level) bool { return true }
func (l *logSignal) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logSignal) WithGroup(string) slog.Handler            { return l }
func (l *logSignal) Handle(_ context.Context, r slog.Record) error {
	if r.Message == l.msg {
		l.once.Do(func() { close(l.seen) })
	}
	return nil
}

// TestBootNetworkCompletesSoftwareUpdate: a network boot-up that cancels
// a boot slave process in the middle of its software update lets the
// update finish (stop, clear, download, start); cancelled after the
// download, the slave kept the new program stopped. The flash ends only
// after the cancellation.
func TestBootNetworkCompletesSoftwareUpdate(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	downloading := make(chan struct{})
	var once sync.Once
	f := &failingFlasher{pending: true}
	f.image = []byte("v1")
	f.onDownload = func() { once.Do(func() { close(downloading) }) }
	ups := bootUps(t, buses[0], 5)
	n5 := startNode(t, buses[1], nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 20 }), node.WithProgram(program.Config{Handler: f}))
	ups() // the boot-up must not start a process before the reset below
	img := bytes.Repeat([]byte("application v2 "), 100)
	d := mgrDict(t)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory|master.AssignVersionCheck|master.AssignSoftwareUpdate, 0, 0))
	consume(t, d, 1, 5, 300*time.Millisecond)
	set32(t, d, program.IndexExpectedIdentification, 5, crc32.ChecksumIEEE(img))
	if err := d.Set(program.IndexManagerData, 5, img); err != nil {
		t.Fatal(err)
	}
	cancelled := &logSignal{msg: "master: cancelling a boot slave process", seen: make(chan struct{})}
	timing := fastBoot
	timing.ProgramTimeout = waitTimeout
	// 0x1F80 bit 0 clear: no automatic boot-up
	r := startManager(t, buses, d, master.WithLogger(slog.New(cancelled)), master.WithBootTiming(timing))
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	ctx := nettest.Ctx(t)
	if err := r.m.ResetCommunication(ctx, 5); err != nil {
		t.Fatal(err)
	}
	select {
	case <-downloading:
	case <-ctx.Done():
		t.Fatalf("no software update started: sequence %q, events %v", f.seq(), r.rec.all())
	}
	flashed := make(chan struct{})
	go func() {
		defer close(flashed)
		select {
		case <-cancelled.seen:
			n5.Program().SetFlashStatus(1, program.NewFlashStatus(false, program.FlashOK))
		case <-ctx.Done():
		}
	}()
	defer func() { <-flashed }()
	if err := r.m.BootNetwork(ctx); err != nil {
		t.Fatalf("network boot-up: %v; events %v", err, r.rec.all())
	}
	if got := f.seq(); got != "stop,clear,download,start" {
		t.Fatalf("sequence %q", got)
	}
}

// lateFlasher holds one step of a program download until the SDO client
// has given up on it (aborted it on timeout): the device carries the
// step out, the master does not learn it from the answer.
type lateFlasher struct {
	flasher
	step    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *lateFlasher) hold(step string) {
	if step == f.step {
		f.once.Do(func() {
			close(f.entered)
			<-f.release
		})
	}
}

func (f *lateFlasher) Control(n uint8, cmd program.Command) error {
	f.hold(cmd.String())
	return f.flasher.Control(n, cmd)
}

func (f *lateFlasher) Download(n uint8, data []byte) error {
	f.hold("download")
	return f.flasher.Download(n, data)
}

// TestDownloadProgramLateAnswers: a step of a program download whose
// answer comes after the SDO timeout was carried out by the device. It is
// not repeated when the device's state shows that (Clear and the data
// would be refused), and the download goes on. Found by CI: a slow answer
// failed the update, with status I or with the new program left stopped.
func TestDownloadProgramLateAnswers(t *testing.T) {
	for _, step := range []string{"stop", "clear", "download", "start"} {
		t.Run(step, func(t *testing.T) {
			nettest.CheckLeaks(t)
			r := newRig(t, 2, master.WithBootTiming(fastBoot), master.WithSDOOptions(sdo.WithTimeout(30*time.Millisecond)))
			f := &lateFlasher{step: step, entered: make(chan struct{}), release: make(chan struct{})}
			f.image = []byte("v1")
			var released sync.Once
			release := func() { released.Do(func() { close(f.release) }) }
			defer release()
			// The client's abort of the request ends the wait.
			sub, err := r.buses[2].Subscribe() // a port does not receive its own frames
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sub.Close() }()
			ctx := nettest.Ctx(t)
			watched := make(chan struct{})
			go func() {
				defer close(watched)
				defer release()
				<-f.entered
				for {
					fr, err := sub.Receive(ctx)
					if err != nil {
						return
					}
					if fr.ID == canopen.FuncSDORx+5 && fr.Len > 0 && fr.Data[0] == 0x80 {
						return
					}
				}
			}()
			n5 := startNode(t, r.buses[1], nettest.Dict(t, 5, nil), node.WithProgram(program.Config{Handler: f}))
			defer func() { release(); <-watched }()
			img := []byte("application v2")
			if err := r.m.DownloadProgram(ctx, 5, img, master.ProgramOptions{Identification: crc32.ChecksumIEEE(img)}); err != nil {
				t.Fatalf("download: %v (sequence %q)", err, f.seq())
			}
			if got := f.seq(); got != "stop,clear,download,start" {
				t.Fatalf("sequence %q", got)
			}
			if st := n5.Program().State(1); st != program.Started {
				t.Fatalf("state %v", st)
			}
		})
	}
}
