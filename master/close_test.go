// SPDX-License-Identifier: MIT

package master_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// markerID is the identifier of the frame a test sends after the calls
// it checks: on one port frames arrive in the order they were sent, so
// what the master sent arrives before it.
const markerID canbus.ID = 0x7FF

// sentBefore returns the frames that reached sub before a marker frame
// sent now from bus.
func sentBefore(t *testing.T, bus *canbus.Bus, sub *canbus.Subscription) []canbus.Frame {
	t.Helper()
	ctx := nettest.Ctx(t)
	mark, _ := canbus.NewStandard(markerID, nil)
	if err := bus.Send(ctx, &mark); err != nil {
		t.Fatal(err)
	}
	var out []canbus.Frame
	for {
		f, err := sub.Receive(ctx)
		if err != nil {
			t.Fatalf("no marker frame: %v", err)
		}
		if f.ID == markerID {
			return out
		}
		out = append(out, f)
	}
}

// closedManager returns a master with a network list (a mandatory slave 5
// that is absent), started and closed, and a subscription on another port
// that has taken every frame the master sent until it was closed.
func closedManager(t *testing.T) (*master.Master, []*canbus.Bus, *canbus.Subscription) {
	t.Helper()
	buses := testbus.Ports(t, 2)
	sub, err := buses[1].Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
	m, err := master.New(buses[0], master.WithDictionary(d), master.WithBootTiming(fastBoot),
		master.WithSDOOptions(sdo.WithTimeout(20*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	sentBefore(t, buses[0], sub) // what was sent until Close returned
	return m, buses, sub
}

// TestBootNetworkAfterClose: a network boot-up asked for after Close
// fails with ErrClosed at once and sends nothing (no NMT reset).
func TestBootNetworkAfterClose(t *testing.T) {
	nettest.CheckLeaks(t)
	m, buses, sub := closedManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.BootNetwork(ctx); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("BootNetwork after Close = %v, want ErrClosed", err)
	}
	if err := m.BootSlave(ctx, 5); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("BootSlave after Close = %v, want ErrClosed", err)
	}
	if sent := sentBefore(t, buses[0], sub); len(sent) != 0 {
		t.Errorf("frames sent after Close: %v", sent)
	}
}

// TestOperationsAfterClose: after Close the master's operations fail
// with ErrClosed and put no frame on the bus, a PDO sender obtained
// before Close included.
func TestOperationsAfterClose(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 2)
	m, err := master.New(buses[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	desc := nettest.Dict(t, 5, nil)
	mapping := pdo.Mapping{{Index: nettest.IdxOut8, SubIndex: 0, Bits: 8}}
	sender, err := m.Sender(canopen.NewCOBID(0x205), mapping, desc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := nettest.Ctx(t)
	sub, err := buses[1].Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sender.Send(ctx, uint8(1)); err != nil {
		t.Fatalf("Send before Close: %v", err)
	}
	if sent := sentBefore(t, buses[0], sub); len(sent) != 1 || sent[0].ID != 0x205 {
		t.Fatalf("before Close: %v, want the PDO", sent)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	for _, op := range []struct {
		name string
		call func() error
	}{
		{"NMT", func() error { return m.NMT(ctx, 5, canopen.NMTStart) }},
		{"StartNode", func() error { return m.StartNode(ctx, 0) }},
		{"ResetCommunication", func() error { return m.ResetCommunication(ctx, 0) }},
		{"Scan", func() error { _, err := m.Scan(ctx, []canopen.NodeID{5}); return err }},
		{"Sender", func() error { _, err := m.Sender(canopen.NewCOBID(0x205), mapping, desc); return err }},
		{"Sender.Send", func() error { return sender.Send(ctx, uint8(2)) }},
		{"SYNC.Send", func() error { return m.SYNC().Send(ctx) }},
		{"TIME.Send", func() error { return m.TIME().Send(ctx, time.Now()) }},
		{"SDO", func() error { _, err := m.SDO(5); return err }},
	} {
		if err := op.call(); !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("%s after Close = %v, want ErrClosed", op.name, err)
		}
	}
	if sent := sentBefore(t, buses[0], sub); len(sent) != 0 {
		t.Errorf("frames sent after Close: %v", sent)
	}
}

// TestCloseEndsOperations: a network boot-up and a scan in progress when
// the master is closed end with ErrClosed, within Close's own time.
func TestCloseEndsOperations(t *testing.T) {
	// Fake time: nothing here depends on the load of the machine, and the
	// test ends only when every goroutine of the master has.
	synctest.Test(t, func(t *testing.T) {
		buses := testbus.Ports(t, 2)
		d := mgrDict(t)
		set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
		assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
		// The NMT resets of the network boot-ups, and the scan's requests.
		sub, err := buses[1].Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		m, err := master.New(buses[0], master.WithDictionary(d), master.WithBootTiming(fastBoot))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		ctx := nettest.Ctx(t)
		boot, scan := make(chan error, 1), make(chan error, 1)
		go func() { boot <- m.BootNetwork(ctx) }()
		go func() { _, err := m.Scan(ctx, nil); scan <- err }()
		// Both are in progress once an NMT reset of a network boot-up and an
		// SDO request of the scan have been sent: slave 5 is absent, so the
		// boot-up waits for it, and the scan for 127 nodes that do not answer.
		reset, request := false, false
		for !reset || !request {
			f, err := sub.Receive(ctx)
			if err != nil {
				t.Fatalf("NMT reset seen %v, scan request seen %v: %v", reset, request, err)
			}
			reset = reset || f.ID == canopen.FuncNMT && f.Data[0] == byte(canopen.NMTResetCommunication)
			request = request || f.ID > canopen.FuncSDORx && f.ID <= canopen.FuncSDORx+0x7F
		}
		synctest.Wait() // both wait for answers that do not come
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		for name, ch := range map[string]chan error{"BootNetwork": boot, "Scan": scan} {
			select {
			case err := <-ch:
				if !errors.Is(err, canopen.ErrClosed) {
					t.Errorf("%s in progress at Close = %v, want ErrClosed", name, err)
				}
			case <-ctx.Done():
				t.Fatalf("%s still in progress after Close", name)
			}
		}
	})
}

// TestCloseEndsBootSlave: a boot slave process in progress when the
// master is closed ends with ErrClosed, within Close's own time.
func TestCloseEndsBootSlave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		buses := testbus.Ports(t, 1)
		d := mgrDict(t) // 0x1F80 is 0: no network boot-up
		assign(t, d, 6, master.NewAssignment(master.AssignSlave|master.AssignBoot, 0, 0))
		m, err := master.New(buses[0], master.WithDictionary(d), master.WithBootTiming(fastBoot))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		slave := make(chan error, 1)
		go func() { slave <- m.BootSlave(context.Background(), 6) }() // slave 6 is absent
		synctest.Wait()                                               // the process waits for an answer
		start := time.Now()
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if err := <-slave; !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("BootSlave in progress at Close = %v, want ErrClosed", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("BootSlave ended %v after Close, want at once", d)
		}
		if st, done := m.SlaveStatus(6); done {
			t.Errorf("a result (%s) is recorded for a process that was closed", st)
		}
	})
}

// TestBootSlaveCancelledByNetworkBoot: a network boot-up that starts
// while BootSlave configures a slave cancels that process before it
// resets the slaves. BootSlave returns the cancellation, and the network
// boot-up configures the slave again after its reset.
func TestBootSlaveCancelledByNetworkBoot(t *testing.T) {
	// Fake time: the SDO timeouts of the boot slave process do not depend
	// on the load of the machine.
	synctest.Test(t, func(t *testing.T) {
		buses := testbus.Ports(t, 2)
		ctx := nettest.Ctx(t)

		// Slave 5 holds a write of its heartbeat time, the object the
		// concise DCF configures, once the test has armed it.
		sd := nettest.Dict(t, 5, nil)
		entered, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		var armed atomic.Bool
		var writes atomic.Int32
		sd.OnWrite(od.IndexProducerHeartbeat, 0, func(*od.Variable, []byte) error {
			writes.Add(1)
			if armed.CompareAndSwap(true, false) {
				close(entered)
				<-held
			}
			return nil
		})
		booted := bootUps(t, buses[0], 5)
		startNode(t, buses[1], sd)
		booted()
		t.Cleanup(release) // before the slave is closed

		// The network boot-up of the manager's start configures the slave
		// a first time.
		d := mgrDict(t)
		set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster|master.StartupNoAutoOperational)
		assign(t, d, 5, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
		concise(t, d, 5, cdcf.Entry{Index: od.IndexProducerHeartbeat, SubIndex: 0, Data: u16(50)})
		m := startManager(t, buses, d)
		m.rec.wait(t, "the network boot-up of the start", kind(master.EventNetworkBootDone, masterID))
		first := writes.Load()

		armed.Store(true)
		slave, network := make(chan error, 1), make(chan error, 1)
		go func() { slave <- m.m.BootSlave(ctx, 5) }()
		select {
		case <-entered: // BootSlave is writing the configuration
		case <-ctx.Done():
			t.Fatal("BootSlave did not reach the configuration")
		}
		go func() { network <- m.m.BootNetwork(ctx) }()
		select {
		case err := <-slave:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("BootSlave during a network boot-up = %v, want the cancellation", err)
			}
		case <-ctx.Done():
			t.Fatal("BootSlave still running")
		}
		// The slave finishes the write it held, then its reset.
		release()
		select {
		case err := <-network:
			if err != nil {
				t.Fatalf("BootNetwork = %v", err)
			}
		case <-ctx.Done():
			t.Fatal("BootNetwork still running")
		}
		if st, done := m.m.SlaveStatus(5); !done || st != master.BootOK {
			t.Errorf("slave 5 after the network boot-up: status %s, done %v", st, done)
		}
		if v, _ := sd.GetUint(od.IndexProducerHeartbeat, 0); v != 50 {
			t.Errorf("slave 5 heartbeat time %d after the network boot-up, want the configured 50", v)
		}
		// The write BootSlave was held in, and the one after the reset.
		if n := writes.Load() - first; n != 2 {
			t.Errorf("slave 5 configured %d time(s) after the first network boot-up, want 2: by BootSlave, and again after its reset", n)
		}
	})
}
