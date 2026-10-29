// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/od"
)

// otherProductEDS is the shared device with product code 0x434F4932: a
// device of interop-device.eds does not match its identity.
const otherProductEDS = "/fixtures/eds/interop-device-other-product.eds"

// managerNode is the node-ID of the go-canopen CiA 302 manager.
const managerNode canopen.NodeID = 1

// newManager starts a go-canopen master that is a CiA 302 manager
// (WithDictionary, node 1, 0x1F80 bit 0) whose network list configure
// fills: the network boot-up runs on its own at the boot-up of the
// master's node, as CiA 302-2 describes; its end is an event.
func newManager(t *testing.T, ifc string, log *eventLog, configure func(md *od.Dictionary), opts ...master.Option) *master.Master {
	t.Helper()
	md, err := od.Standard(od.Config{NodeID: managerNode, HeartbeatConsumers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := master.AddManagerObjects(md); err != nil {
		t.Fatal(err)
	}
	configure(md)
	if err := md.SetUint(master.IndexNMTStartup, 0, uint64(master.StartupMaster)); err != nil {
		t.Fatal(err)
	}
	opts = append([]master.Option{master.WithEventHandler(log.add), master.WithDictionary(md),
		master.WithBootTiming(master.BootTiming{RetryDelay: 200 * time.Millisecond, ResetTimeout: 3 * time.Second})}, opts...)
	m, _ := newMaster(t, ifc, opts...)
	return m
}

// networkBoot waits for the end of the network boot-up of the master's
// node (EventNetworkBootDone or EventNetworkBootFailed).
func networkBoot(t *testing.T, ctx context.Context, log *eventLog) master.Event {
	t.Helper()
	wctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var ev master.Event
	end := func(e master.Event) bool {
		if e.Kind == master.EventNetworkBootDone || e.Kind == master.EventNetworkBootFailed {
			ev = e
			return true
		}
		return false
	}
	if !log.wait(wctx, end) {
		failf(t, "the network boot-up did not end; events: %s", log.String())
	}
	return ev
}

// expectSlave puts a node into the manager's network list (mandatory,
// boot) with the device type and identity (vendor, product, revision;
// and the serial number when not 0) the EDS describes.
func expectSlave(t *testing.T, md *od.Dictionary, id canopen.NodeID, f *eds.File, serial uint32) {
	t.Helper()
	d, err := f.Dictionary(id)
	if err != nil {
		t.Fatal(err)
	}
	set := func(index uint16, v uint64) {
		if err := md.SetUint(index, uint8(id), v); err != nil {
			t.Fatal(err)
		}
	}
	get := func(index uint16, sub uint8) uint64 {
		v, err := d.GetUint(index, sub)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	set(master.IndexSlaveAssignment, uint64(master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0)))
	set(master.IndexExpectedDeviceType, get(0x1000, 0))
	set(master.IndexExpectedVendorID, get(0x1018, 1))
	set(master.IndexExpectedProductCode, get(0x1018, 2))
	set(master.IndexExpectedRevision, get(0x1018, 3))
	if serial != 0 {
		set(master.IndexExpectedSerialNumber, uint64(serial))
	}
}

// configureSlave gives a slave a concise DCF (0x2001 and its heartbeat
// producer time) and supervises its heartbeat (0x1016).
func configureSlave(t *testing.T, md *od.Dictionary, id canopen.NodeID, sub uint8) {
	t.Helper()
	b, err := cdcf.Encode([]cdcf.Entry{
		{Index: 0x2001, Data: le(0x4400+uint64(id), 2)},
		{Index: od.IndexProducerHeartbeat, Data: le(200, 2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := md.Set(cdcf.IndexConciseDCF, uint8(id), b); err != nil {
		t.Fatal(err)
	}
	if err := md.SetUint(od.IndexConsumerHeartbeat, sub, uint64(nmt.ConsumerEntry(id, time.Second))); err != nil {
		t.Fatal(err)
	}
}

// bootChecks verifies a booted slave: EventBootDone, SlaveStatus, the
// concise DCF's values by SDO and the state operational by heartbeat.
func bootChecks(t *testing.T, ctx context.Context, m *master.Master, log *eventLog, id canopen.NodeID) {
	t.Helper()
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !log.wait(wctx, func(e master.Event) bool { return e.Kind == master.EventBootDone && e.Node == id }) {
		differ(t, "no boot done of node %d; events: %s", id, log.String())
	}
	if st, done := m.SlaveStatus(id); !done || st != master.BootOK {
		differ(t, "slave status of node %d: %v (done %v)", id, st, done)
	}
	if err := m.WaitState(wctx, id, canopen.StateOperational); err != nil {
		differ(t, "node %d not operational by heartbeat: %v (%v)", id, err, m.State(id))
	}
	cl, err := m.SDO(id)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := cl.ReadUint(ctx, 0x2001, 0); err != nil || v != 0x4400+uint64(id) {
		differ(t, "node %d: 0x2001 = %#x, %v; the concise DCF writes %#x", id, v, err, 0x4400+uint64(id))
	}
	if v, err := cl.ReadUint(ctx, od.IndexProducerHeartbeat, 0); err != nil || v != 200 {
		differ(t, "node %d: 0x1017 = %d, %v; the concise DCF writes 200", id, v, err)
	}
}

// masterBoot: go-canopen's CiA 302 manager boots each stack's device: the
// boot slave process with a concise DCF, an identity that differs from
// the network list (error status M), and the LSS step of the network
// boot-up (WithLSSBoot) for an unconfigured LSS slave.
func masterBoot(t *testing.T, ifc, a string, c caps, f func() *eds.File, other *eds.File, sc scenarioFunc) {
	sc(t, "boot-slave", func(t *testing.T) {
		const id canopen.NodeID = 21
		var log eventLog
		ctx := ctxT(t, 3*time.Minute)
		dev := startContainer(t, a, "device", "--interface", ifc, "--node-id", fmt.Sprint(id), "--eds", deviceEDS, "--timeout-ms", "900000")
		dev.await(t, `"type":"ready"`, 90*time.Second)
		file := f()
		m := newManager(t, ifc, &log, func(md *od.Dictionary) {
			expectSlave(t, md, id, file, 0)
			configureSlave(t, md, id, 1)
		})
		if ev := networkBoot(t, ctx, &log); ev.Kind != master.EventNetworkBootDone {
			differ(t, "network boot-up: %v; events: %s", ev, log.String())
		}
		bootChecks(t, ctx, m, &log, id)
		info(t).detail = "identity checked, concise DCF written, heartbeat supervised, started"
	})
	sc(t, "boot-identity-mismatch", func(t *testing.T) {
		const id canopen.NodeID = 24
		var log eventLog
		ctx := ctxT(t, 3*time.Minute)
		dev := startContainer(t, a, "device", "--interface", ifc, "--node-id", fmt.Sprint(id), "--eds", deviceEDS, "--timeout-ms", "900000")
		dev.await(t, `"type":"ready"`, 90*time.Second)
		m := newManager(t, ifc, &log, func(md *od.Dictionary) { expectSlave(t, md, id, other, 0) })
		ev := networkBoot(t, ctx, &log)
		var be *master.BootError
		err := ev.Err
		if ev.Kind != master.EventNetworkBootFailed || !errors.As(err, &be) || be.Status != master.BootProductCode || be.Node != id {
			differ(t, "network boot-up: %v, want failed with error status M of node %d; events: %s", ev, id, log.String())
		}
		if st, done := m.SlaveStatus(id); !done || st != master.BootProductCode {
			differ(t, "slave status %v (done %v), want M", st, done)
		}
		if !log.has(func(e master.Event) bool {
			return e.Kind == master.EventBootFailed && e.Node == id && e.Status == master.BootProductCode
		}) {
			differ(t, "no EventBootFailed with status M; events: %s", log.String())
		}
		info(t).detail = "error status M: " + err.Error()
	})
	sc(t, "boot-lss", func(t *testing.T) {
		if !c.Features["lssSlave"] {
			skipf(t, "%s's device has no LSS slave (lssSlave false)", a)
		}
		const id canopen.NodeID = 22
		serial := lssSerial(a, 2)
		var log eventLog
		ctx := ctxT(t, 3*time.Minute)
		dev := startContainer(t, a, lssDeviceArgs(ifc, serial)...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		file := f()
		m := newManager(t, ifc, &log, func(md *od.Dictionary) {
			expectSlave(t, md, id, file, serial)
			configureSlave(t, md, id, 1)
		}, master.WithLSSBoot(lss.FastscanOptions{Known: lssKnown}))
		if ev := networkBoot(t, ctx, &log); ev.Kind != master.EventNetworkBootDone {
			differ(t, "network boot-up: %v; events: %s; device: %s", ev, log.String(), dev.stdout())
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if !log.wait(wctx, func(e master.Event) bool {
			return e.Kind == master.EventLSSAssigned && e.Node == id && e.LSS != nil && e.LSS.Address.Serial == serial
		}) {
			differ(t, "no LSS assignment of node %d; events: %s", id, log.String())
		}
		if dev.waitDoc(5*time.Second, nodeIDDoc(id)) == nil {
			differ(t, "the device reports no node-ID %d: %s", id, dev.stdout())
		}
		bootChecks(t, ctx, m, &log, id)
		info(t).detail = fmt.Sprintf("serial %#x got node-ID %d by Fastscan, then booted", serial, id)
	})
}

// stackBoot: each stack's CiA 302 boot master (boot) boots a go-canopen
// node: the boot slave process with a concise DCF written by the stack's
// configuration tool, and an identity that differs (error status M).
func stackBoot(t *testing.T, ifc, a string, c caps, load func() *eds.File, sc scenarioFunc) {
	need := func(t *testing.T) {
		if !c.op("boot") || !c.Features["boot"] {
			skipf(t, "%s has no CiA 302 boot master (boot)", a)
		}
	}
	bootEvent := func(r run, id canopen.NodeID) map[string]any {
		var last map[string]any
		for _, d := range docs(t, r.out) {
			if d["type"] == "boot" && d["node"] == float64(id) {
				last = d
			}
		}
		return last
	}
	sc(t, "boot-slave", func(t *testing.T) {
		need(t)
		const id canopen.NodeID = 21
		g := startGoDevice(t, ifc, load(), id)
		r := adapterRun(t, "", a, "boot", "--interface", ifc, "--timeout-ms", "20000",
			"--slave", fmt.Sprintf("%d:%s:mandatory", id, deviceEDS),
			"--sdo", fmt.Sprintf("%d:0x2001:00=0x%04X", id, 0x4400+int(id)), "--sdo", fmt.Sprintf("%d:0x1017:00=200", id))
		e := bootEvent(r, id)
		if r.code != 0 || e == nil || e["ok"] != true || e["errorStatus"] != nil {
			differ(t, "boot: event %v; %v", e, r)
		}
		if v, _ := g.d.GetUint(0x2001, 0); v != 0x4400+uint64(id) {
			differ(t, "0x2001 = %#x, the concise DCF writes %#x", v, 0x4400+uint64(id))
		}
		if v, _ := g.d.GetUint(od.IndexProducerHeartbeat, 0); v != 200 {
			differ(t, "0x1017 = %d, the concise DCF writes 200", v)
		}
		if !waitFor(5*time.Second, func() bool { return g.n.State() == canopen.StateOperational }) {
			differ(t, "node state %v, want operational", g.n.State())
		}
		info(t).detail = "booted: concise DCF (0x2001, 0x1017) written, started"
	})
	sc(t, "boot-identity-mismatch", func(t *testing.T) {
		need(t)
		const id canopen.NodeID = 24
		g := startGoDevice(t, ifc, load(), id)
		r := adapterRun(t, "", a, "boot", "--interface", ifc, "--timeout-ms", "20000",
			"--slave", fmt.Sprintf("%d:%s:mandatory", id, otherProductEDS))
		e := bootEvent(r, id)
		if r.code != 5 || e == nil || e["errorStatus"] != "M" {
			differ(t, "boot: event %v; %v; want exit 5 and error status M", e, r)
		}
		if st := g.n.State(); st == canopen.StateOperational {
			differ(t, "the node was started although its identity differs")
		}
		info(t).detail = fmt.Sprintf("error status M: %v", e["message"])
	})
}
