// SPDX-License-Identifier: MIT

package node_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

func TestDriveOverSDO(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	d := nettest.Dict(t, 5, nil)
	var userErrs atomic.Int32
	n := startNode(t, r.buses[1], d, node.WithDrive(cia402.DriveConfig{OnError: func(uint16) { userErrs.Add(1) }}, 2*time.Millisecond))
	r.rec.wait(t, "boot-up", func(e master.Event) bool { return e.Kind == master.EventBootUp })
	if n.Drive() == nil {
		t.Fatal("no drive")
	}
	ctl := r.m.Drive(5, &cia402.ControllerOptions{Timeout: 3 * time.Second, PollInterval: 2 * time.Millisecond, ResetFault: true})
	if err := ctl.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctl.MoveTo(ctx, 2000, &cia402.MoveOptions{Wait: true, Velocity: 200000, Acceleration: 2000000, Deceleration: 2000000}); err != nil {
		t.Fatal(err)
	}
	if p, err := ctl.Position(ctx); err != nil || p != 2000 {
		t.Fatalf("position %d, %v", p, err)
	}

	// A fault is an emergency with the drive's code.
	n.Drive().InjectFault(0x2310)
	ev := r.rec.wait(t, "EMCY 0x2310", emcyFrom(5, 0x2310))
	if ev.EMCY.Register&(emcy.RegCurrent|emcy.RegDeviceProfile|emcy.RegGeneric) != emcy.RegCurrent|emcy.RegDeviceProfile|emcy.RegGeneric {
		t.Fatalf("register %s", ev.EMCY.Register)
	}
	if code, _ := ctl.FaultCode(ctx); code != 0x2310 {
		t.Fatalf("0x603F = %x", code)
	}
	nettest.Eventually(t, 3*time.Second, "fault state", func() bool { return n.Drive().State() == cia402.Fault })

	// It survives a reset communication: raised again after the boot-up.
	r.rec.reset()
	since := time.Now()
	_ = r.m.ResetCommunication(ctx, 5)
	if err := r.m.WaitBootUp(ctx, 5, since); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "EMCY 0x2310 after reset", emcyFrom(5, 0x2310))
	if reg, _ := d.GetUint(od.IndexErrorRegister, 0); reg&uint64(emcy.RegDeviceProfile) == 0 {
		t.Fatalf("register %x after reset", reg)
	}

	// The fault reset clears it.
	if err := ctl.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	r.rec.wait(t, "error reset", emcyFrom(5, emcy.CodeNoError))
	if userErrs.Load() < 2 {
		t.Fatalf("user OnError called %d times", userErrs.Load())
	}
	if st, _ := ctl.State(ctx); st != cia402.OperationEnabled {
		t.Fatalf("drive state %s", st)
	}
	_ = r.m.StartNode(ctx, 5)
	waitState(t, n, canopen.StateOperational)
}
