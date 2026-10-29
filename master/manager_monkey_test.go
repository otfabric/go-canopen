// SPDX-License-Identifier: MIT

package master_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

// TestMonkeyManagerBoot runs the CiA 302 network boot-up through random
// physical faults: a mandatory slave configured from a concise DCF and an
// optional one. It asserts that the boot-up ends (done, or failed with a
// clean cause and then retried), that a slave reported booted holds its
// configuration, and that nothing leaks.
func TestMonkeyManagerBoot(t *testing.T) {
	seeds := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < iterations(3); i++ {
		cfg := physicalFaults(seeds)
		t.Run(fmt.Sprintf("seed%d", cfg.Seed), func(t *testing.T) {
			nettest.CheckLeaks(t)
			monkeyBoot(t, cfg)
		})
	}
}

func monkeyBoot(t *testing.T, cfg chaos.Config) {
	a, b, br := testbus.Chaos(t, cfg)
	rng := rand.New(rand.NewSource(cfg.Seed))
	param := rng.Uint32()
	d5 := nettest.Dict(t, 5, nil)
	d6 := nettest.Dict(t, 6, func(c *od.Config) { c.HeartbeatMs = 20 })
	startNode(t, b, d5)
	startNode(t, b, d6)

	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	boot := master.AssignSlave | master.AssignBoot
	assign(t, d, 5, master.NewAssignment(boot|master.AssignMandatory, 0, 0))
	assign(t, d, 6, master.NewAssignment(boot, 0, 0))
	set32(t, d, master.IndexExpectedDeviceType, 5, nettest.DeviceType)
	consume(t, d, 1, 5, 400*time.Millisecond)
	consume(t, d, 2, 6, 400*time.Millisecond)
	concise(t, d, 5,
		cdcf.Entry{Index: od.IndexProducerHeartbeat, Data: u16(20)},
		cdcf.Entry{Index: nettest.IdxParam, Data: u32(param)})
	r := startManager(t, []*canbus.Bus{a}, d,
		master.WithSDOOptions(sdo.WithTimeout(60*time.Millisecond), sdo.WithRetries(3)),
		master.WithBootTiming(master.BootTiming{RetryDelay: 50 * time.Millisecond, ResetTimeout: 300 * time.Millisecond}))
	ctx := nettest.Ctx(t)

	ended := func(e master.Event) bool {
		return e.Kind == master.EventNetworkBootDone || e.Kind == master.EventNetworkBootFailed
	}
	ev := r.rec.wait(t, "network boot end", ended)
	for attempt := 0; ev.Kind == master.EventNetworkBootFailed; attempt++ {
		var be *master.BootError
		if !errors.As(ev.Err, &be) || !cleanErr(ev.Err) {
			t.Fatalf("unclean boot failure: %v", ev.Err)
		}
		if attempt == 5 {
			t.Fatalf("network boot failed %d times, last: %v", attempt+1, ev.Err)
		}
		err := r.m.BootNetwork(ctx)
		if err == nil {
			break
		}
		ev = master.Event{Kind: master.EventNetworkBootFailed, Err: err}
	}
	// A slave reported booted holds its configuration.
	if st, done := r.m.SlaveStatus(5); !done || !st.OK() {
		t.Fatalf("mandatory slave %s %v", st, done)
	}
	if v, _ := d5.GetUint(nettest.IdxParam, 0); uint32(v) != param {
		t.Fatalf("param %#x, want %#x", v, param)
	}
	if v, _ := d5.GetUint(od.IndexProducerHeartbeat, 0); v != 20 {
		t.Fatalf("heartbeat %d", v)
	}
	if r.m.Node().State() != canopen.StateOperational {
		t.Fatalf("master %s", r.m.Node().State())
	}
	t.Logf("chaos %+v", br.Stats())
}

// TestMonkeyLSSBoot runs the network boot-up of unconfigured devices
// through random physical faults: the LSS step must give each device the
// node-ID of the slave whose identity it has (never another one, never a
// node-ID twice), and the boot-up, retried while it fails cleanly, must
// end with both slaves configured.
func TestMonkeyLSSBoot(t *testing.T) {
	seeds := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < iterations(1); i++ {
		cfg := physicalFaults(seeds)
		t.Run(fmt.Sprintf("seed%d", cfg.Seed), func(t *testing.T) {
			nettest.CheckLeaks(t)
			monkeyLSSBoot(t, cfg)
		})
	}
}

func monkeyLSSBoot(t *testing.T, cfg chaos.Config) {
	a, b, br := testbus.Chaos(t, cfg)
	want := map[uint32]canopen.NodeID{0x2001: 5, 0x2002: 6}
	nodes := map[uint32]*node.Node{}
	for serial := range want {
		d := nettest.Dict(t, lss.Unconfigured, func(c *od.Config) { c.SerialNumber = serial })
		nodes[serial] = startNode(t, b, d, node.WithLSS())
	}
	d := mgrDict(t)
	set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
	set32(t, d, master.IndexBootTime, 0, 1500)
	for serial, id := range want {
		assign(t, d, id, master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0))
		set32(t, d, master.IndexExpectedVendorID, uint8(id), nettest.VendorID)
		set32(t, d, master.IndexExpectedSerialNumber, uint8(id), serial)
		concise(t, d, id, cdcf.Entry{Index: nettest.IdxParam, Data: u32(serial)})
	}
	fs := lss.FastscanOptions{Timeout: 5 * time.Millisecond, Retries: 2, Known: map[lss.Part]uint32{
		lss.PartVendor: nettest.VendorID, lss.PartProduct: nettest.ProductCode, lss.PartRevision: nettest.Revision,
	}}
	r := startManager(t, []*canbus.Bus{a}, d, master.WithLSSBoot(fs),
		master.WithLSSOptions(lss.WithRetries(3), lss.WithFastscanTimeout(10*time.Millisecond)),
		master.WithSDOOptions(sdo.WithTimeout(60*time.Millisecond), sdo.WithRetries(3)),
		master.WithBootTiming(master.BootTiming{RetryDelay: 50 * time.Millisecond, ResetTimeout: 300 * time.Millisecond}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ended := func(e master.Event) bool {
		return e.Kind == master.EventNetworkBootDone || e.Kind == master.EventNetworkBootFailed
	}
	// The LSS step (Fastscan rounds with retries) and the boot time of a
	// slave that missed it take seconds under load.
	var ev master.Event
	nettest.Eventually(t, 60*time.Second, "network boot end", func() bool {
		var ok bool
		ev, ok = r.rec.find(ended)
		return ok
	})
	for attempt := 0; ev.Kind == master.EventNetworkBootFailed; attempt++ {
		var be *master.BootError
		if !errors.As(ev.Err, &be) {
			t.Fatalf("unclean boot failure: %v", ev.Err)
		}
		if attempt == 8 {
			t.Fatalf("network boot failed %d times, last: %v", attempt+1, ev.Err)
		}
		err := r.m.BootNetwork(ctx)
		if err == nil {
			break
		}
		ev = master.Event{Kind: master.EventNetworkBootFailed, Err: err}
	}
	for serial, id := range want {
		n := nodes[serial]
		if n.NodeID() != id {
			t.Fatalf("device %#x has node-ID %d, want %d", serial, n.NodeID(), id)
		}
		if v, _ := n.Dictionary().GetUint(nettest.IdxParam, 0); uint32(v) != serial {
			t.Fatalf("node %d param %#x", id, v)
		}
	}
	// Every assignment reported names the right device.
	r.rec.mu.Lock()
	for _, e := range r.rec.evs {
		if e.Kind == master.EventLSSAssigned && want[e.LSS.Address.Serial] != e.Node {
			r.rec.mu.Unlock()
			t.Fatalf("wrong assignment %v", e)
		}
	}
	r.rec.mu.Unlock()
	t.Logf("chaos %+v", br.Stats())
}
