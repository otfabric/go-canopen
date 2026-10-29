// SPDX-License-Identifier: MIT

package master_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
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
