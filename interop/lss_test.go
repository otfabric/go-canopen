// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/store"
)

// The LSS address of the shared test device (0x1018), except the serial
// number, which each scenario chooses.
const (
	lssVendor   uint32 = 0
	lssProduct  uint32 = 0x434F4931
	lssRevision uint32 = 0x00010000
)

func lssAddr(serial uint32) lss.Address {
	return lss.Address{Vendor: lssVendor, Product: lssProduct, Revision: lssRevision, Serial: serial}
}

// lssKnown restricts a Fastscan to the test devices.
var lssKnown = map[lss.Part]uint32{lss.PartVendor: lssVendor, lss.PartProduct: lssProduct, lss.PartRevision: lssRevision}

// lssSerial is the first serial number of an adapter's LSS scenarios:
// every device of the suite has its own LSS address.
func lssSerial(a string, dir int) uint32 {
	for i, x := range adapters {
		if x == a {
			return uint32(0x4700 + 0x100*dir + 0x10*i)
		}
	}
	return 0x4F00
}

// lssDeviceArgs are the arguments of a stack's unconfigured LSS slave
// (node-ID 255) with a serial number.
func lssDeviceArgs(ifc string, serial uint32) []string {
	return []string{"device", "--interface", ifc, "--node-id", "255", "--eds", deviceEDS, "--serial", fmt.Sprintf("0x%08X", serial), "--timeout-ms", "900000"}
}

// nodeIDDoc matches a device's node-id event.
func nodeIDDoc(id canopen.NodeID) func(map[string]any) bool {
	return isDoc("node-id", map[string]any{"node": float64(id)})
}

// activeNodeID waits for the device to report the node-ID active; when
// it does not by itself (lely: pending until NMT reset communication),
// the master sends reset communication to all nodes. It reports whether
// the node-ID became active without the NMT command.
func activeNodeID(t *testing.T, ctx context.Context, m *master.Master, dev *container, id canopen.NodeID) (byItself bool) {
	t.Helper()
	if dev.waitDoc(3*time.Second, nodeIDDoc(id)) != nil {
		return true
	}
	if err := m.NMT(ctx, 0, canopen.NMTResetCommunication); err != nil {
		failf(t, "NMT reset communication: %v", err)
	}
	if dev.waitDoc(5*time.Second, nodeIDDoc(id)) == nil {
		differ(t, "node-ID %d not active, also after NMT reset communication: %s", id, dev.stdout())
	}
	return false
}

var lssMasterScenarios = []string{
	"lss-identify-non-configured", "lss-identify-remote", "lss-switch-selective", "lss-switch-selective-none",
	"lss-inquire", "lss-bit-timing", "lss-configure-node-id", "lss-activate-on-waiting", "lss-inquire-node-id",
	"lss-store", "lss-fastscan-list", "lss-fastscan-select", "lss-fastscan-configured", "lss-assign-all",
}

// masterLSS: go-canopen's LSS master (master.LSS) against each stack's LSS
// slave (device --node-id 255 --serial N): identification, switch state
// selective, inquiries, bit timing, node-ID configuration and store,
// Fastscan (list and select) and AssignAll.
func masterLSS(t *testing.T, ifc, a string, c caps, sc scenarioFunc) {
	if !c.op("device") || !c.Features["lssSlave"] {
		for _, n := range lssMasterScenarios {
			sc(t, n, func(t *testing.T) { skipf(t, "%s's device has no LSS slave (lssSlave false)", a) })
		}
		return
	}
	var log eventLog
	m, _ := newMaster(t, ifc, master.WithEventHandler(log.add))
	lm := m.LSS()
	ctx := ctxT(t, 15*time.Minute)
	base := lssSerial(a, 0)

	t.Run("unconfigured", func(t *testing.T) {
		addr := lssAddr(base)
		dev := startContainer(t, a, lssDeviceArgs(ifc, base)...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		sc(t, "lss-identify-non-configured", func(t *testing.T) {
			ok, err := lm.IdentifyNonConfigured(ctx)
			if err != nil || !ok {
				differ(t, "identify non-configured remote slave: %v, %v", ok, err)
			}
		})
		sc(t, "lss-identify-remote", func(t *testing.T) {
			lo, hi := addr, addr
			lo.Serial, hi.Serial = base-1, base+1
			ok, err := lm.Identify(ctx, lo, hi)
			if err != nil || !ok {
				differ(t, "identify remote slave %v..%v: %v, %v", lo, hi, ok, err)
			}
			lo.Serial, hi.Serial = base+0x1000, base+0x2000
			ok, err = lm.Identify(ctx, lo, hi)
			if err != nil || ok {
				differ(t, "identify remote slave %v..%v (not on the bus): %v, %v", lo, hi, ok, err)
			}
		})
		sc(t, "lss-switch-selective", func(t *testing.T) {
			if err := lm.SwitchStateSelective(ctx, addr); err != nil {
				differ(t, "switch state selective %v: %v", addr, err)
			}
			if err := lm.SwitchStateGlobal(ctx, lss.Waiting); err != nil {
				failf(t, "%v", err)
			}
		})
		sc(t, "lss-switch-selective-none", func(t *testing.T) {
			other := addr
			other.Serial = 0x7FFFFFFF
			if err := lm.SwitchStateSelective(ctx, other); !errors.Is(err, canopen.ErrTimeout) {
				differ(t, "switch state selective of an absent address: %v, want a timeout", err)
			}
		})
		sc(t, "lss-inquire", func(t *testing.T) {
			var got lss.Address
			var id canopen.NodeID
			err := lm.Configure(ctx, addr, func(ctx context.Context) error {
				var err error
				if got, err = lm.InquireAddress(ctx); err != nil {
					return err
				}
				id, err = lm.InquireNodeID(ctx)
				return err
			})
			if err != nil || got != addr || id != lss.Unconfigured {
				differ(t, "inquire: address %v node-ID 0x%02X, %v; want %v and 0xFF", got, uint8(id), err, addr)
			}
			info(t).detail = fmt.Sprintf("address %v, node-ID 0x%02X", got, uint8(id))
		})
		sc(t, "lss-bit-timing", func(t *testing.T) {
			if !c.Features["lssSlaveBitTiming"] {
				skipf(t, "%s's LSS slave has no bit timing (lssSlaveBitTiming false)", a)
			}
			if err := lm.Configure(ctx, addr, func(ctx context.Context) error { return lm.SetBitTiming(ctx, lss.BitTiming250) }); err != nil {
				differ(t, "configure bit timing 250 kbit/s: %v", err)
			}
			if err := lm.SwitchStateGlobal(ctx, lss.Configuration); err != nil {
				failf(t, "%v", err)
			}
			if err := lm.ActivateBitTiming(ctx, 50*time.Millisecond); err != nil {
				failf(t, "activate bit timing: %v", err)
			}
			time.Sleep(150 * time.Millisecond) // switch delay, then silence for another delay
			if err := lm.SwitchStateGlobal(ctx, lss.Waiting); err != nil {
				failf(t, "%v", err)
			}
			if dev.waitDoc(5*time.Second, isDoc("bit-rate", map[string]any{"kbps": 250.0, "delayMs": 50.0})) == nil {
				differ(t, "no bit-rate event (250 kbit/s after 50 ms): %s", dev.stdout())
			}
			info(t).detail = "the device activates 250 kbit/s after 50 ms"
		})
		const newID canopen.NodeID = 40
		configured, byItself := false, false
		sc(t, "lss-configure-node-id", func(t *testing.T) {
			since := time.Now()
			if err := lm.ConfigureNodeID(ctx, addr, newID); err != nil {
				differ(t, "configure node-ID %d: %v", newID, err)
			}
			byItself = activeNodeID(t, ctx, m, dev, newID)
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := m.WaitBootUp(wctx, newID, since); err != nil {
				differ(t, "no boot-up of node %d: %v", newID, err)
			}
			cl, err := m.SDO(newID)
			if err != nil {
				t.Fatal(err)
			}
			if v, err := cl.ReadUint(ctx, 0x1018, 4); err != nil || v != uint64(base) {
				differ(t, "SDO 0x1018:04 of node %d = %#x, %v; want %#x", newID, v, err, base)
			}
			configured = true
			info(t).detail = fmt.Sprintf("node %d answers SDO with serial %#x", newID, base)
		})
		sc(t, "lss-activate-on-waiting", func(t *testing.T) {
			if !configured {
				failf(t, "not observed: lss-configure-node-id failed")
			}
			if !byItself {
				differ(t, "node-ID %d active only after NMT reset communication, not when the master switched the unconfigured slave back to waiting", newID)
			}
			info(t).detail = "node-ID active after switch state global: waiting (no NMT command)"
		})
		sc(t, "lss-inquire-node-id", func(t *testing.T) {
			var id canopen.NodeID
			err := lm.Configure(ctx, addr, func(ctx context.Context) error {
				var err error
				id, err = lm.InquireNodeID(ctx)
				return err
			})
			if err != nil || id != newID {
				differ(t, "inquire node-ID: %d, %v; want %d", id, err, newID)
			}
		})
		sc(t, "lss-store", func(t *testing.T) {
			err := lm.Configure(ctx, addr, lm.Store)
			if !c.Features["lssSlaveStore"] {
				var ce *lss.ConfigError
				if !errors.As(err, &ce) {
					differ(t, "store configuration on a slave without store: %v, want a refusal", err)
				}
				info(t).detail = "refused: " + err.Error()
				return
			}
			if err != nil {
				differ(t, "store configuration: %v", err)
			}
			res := dev.stop(t)
			if res["nodeId"] != float64(newID) {
				differ(t, "device result %v, want nodeId %d", res, newID)
			}
			info(t).detail = fmt.Sprintf("stored; the device ends with node-ID %v", res["nodeId"])
		})
	})

	t.Run("fastscan", func(t *testing.T) {
		addr := lssAddr(base + 1)
		dev := startContainer(t, a, lssDeviceArgs(ifc, base+1)...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		sc(t, "lss-fastscan-list", func(t *testing.T) {
			got, err := lm.Fastscan(ctx, lss.FastscanOptions{})
			if err != nil || len(got) != 1 || got[0] != addr {
				differ(t, "Fastscan: %v, %v; want [%v]", got, err, addr)
			}
			info(t).detail = fmt.Sprintf("found %v (whole address scanned)", got)
		})
		sc(t, "lss-fastscan-select", func(t *testing.T) {
			got, ok, err := lm.FastscanSelect(ctx, lss.FastscanOptions{})
			if err != nil || !ok || got != addr {
				differ(t, "FastscanSelect: %v %v, %v; want %v", got, ok, err, addr)
			}
			if err := lm.SetNodeID(ctx, 41); err != nil {
				differ(t, "configure node-ID 41 of the selected slave: %v", err)
			}
			if err := lm.SwitchStateGlobal(ctx, lss.Waiting); err != nil {
				failf(t, "%v", err)
			}
			activeNodeID(t, ctx, m, dev, 41)
			info(t).detail = fmt.Sprintf("selected %v, node-ID 41", got)
		})
	})

	t.Run("configured", func(t *testing.T) {
		dev := startContainer(t, a, deviceArgs(ifc, "--serial", fmt.Sprintf("0x%08X", base+2))...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		sc(t, "lss-fastscan-configured", func(t *testing.T) {
			got, err := lm.Fastscan(ctx, lss.FastscanOptions{})
			if err != nil || len(got) != 0 {
				differ(t, "the configured node %d (serial %#x) takes part in Fastscan: %v, %v", deviceNode, base+2, got, err)
			}
			if ok, err := lm.IdentifyNonConfigured(ctx); err != nil || ok {
				differ(t, "the configured node %d answers identify non-configured remote slave: %v, %v", deviceNode, ok, err)
			}
			info(t).detail = fmt.Sprintf("no answer from the configured node %d", deviceNode)
		})
	})

	t.Run("assign-all", func(t *testing.T) {
		var devs []*container
		for i := uint32(3); i <= 4; i++ {
			dev := startContainer(t, a, lssDeviceArgs(ifc, base+i)...)
			dev.await(t, `"type":"ready"`, 90*time.Second)
			devs = append(devs, dev)
		}
		sc(t, "lss-assign-all", func(t *testing.T) {
			opts := []lss.AssignOption{lss.AssignFastscan(lss.FastscanOptions{Known: lssKnown})}
			if c.Features["lssSlaveStore"] {
				opts = append(opts, lss.AssignStore())
			}
			got, err := lm.AssignAll(ctx, 126, opts...)
			want := []lss.Assignment{{Address: lssAddr(base + 3), NodeID: 126}, {Address: lssAddr(base + 4), NodeID: 127}}
			if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
				differ(t, "AssignAll from 126: %v, %v; want %v", got, err, want)
			}
			for i, dev := range devs {
				activeNodeID(t, ctx, m, dev, canopen.NodeID(126+i))
			}
			info(t).detail = fmt.Sprintf("%v", got)
		})
	})
}

// lssNode is a go-canopen node with an LSS slave (node.WithLSS) and a
// parameter store in memory (LSS store configuration).
type lssNode struct {
	*goDevice
	mu       sync.Mutex
	timings  []lss.BitTiming
	activate []time.Duration
}

// startLSSNode starts a go-canopen LSS node with the dictionary of the EDS,
// serial number serial and node-ID id (lss.Unconfigured: unconfigured).
func startLSSNode(t *testing.T, ifc string, f *eds.File, serial uint32, id canopen.NodeID) *lssNode {
	t.Helper()
	f.Object(0x1018).Sub(4).DefaultValue = fmt.Sprintf("0x%08X", serial)
	if o := f.Object(od.IndexProducerHeartbeat); o != nil {
		o.DefaultValue = "100"
	}
	d, err := f.Dictionary(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddObjects(d); err != nil {
		t.Fatal(err)
	}
	ln := &lssNode{}
	n, err := node.New(openBus(t, ifc), d, node.WithStore(store.NewMemory()), node.WithLSS(
		lss.WithBitTimingHandler(func(_ uint8, b lss.BitTiming) error {
			ln.mu.Lock()
			ln.timings = append(ln.timings, b)
			ln.mu.Unlock()
			return nil
		}),
		lss.WithActivateHandler(func(_ lss.Settings, delay time.Duration) {
			ln.mu.Lock()
			ln.activate = append(ln.activate, delay)
			ln.mu.Unlock()
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	g := &goDevice{n: n, d: d}
	var once sync.Once
	g.stop = func() { once.Do(func() { _ = n.Close() }) }
	t.Cleanup(g.stop)
	ln.goDevice = g
	return ln
}

var lssStackScenarios = []string{
	"lss-inquire", "lss-configure-node-id", "lss-store", "lss-bit-timing", "lss-fastscan", "lss-fastscan-configured", "lss-assign-all",
}

// stackLSS: each stack's LSS master (lss) against go-canopen LSS nodes
// (node.WithLSS, unconfigured or configured).
func stackLSS(t *testing.T, ifc, a string, c caps, load func() *eds.File, sc scenarioFunc) {
	if !c.op("lss") || !c.Features["lssMaster"] {
		for _, n := range lssStackScenarios {
			sc(t, n, func(t *testing.T) { skipf(t, "%s has no LSS master (lss)", a) })
		}
		return
	}
	sp := newSpy(t, ifc)
	base := lssSerial(a, 1)
	sel := func(serial uint32) string {
		return fmt.Sprintf("switch-selective:0x%08X:0x%08X:0x%08X:0x%08X", lssVendor, lssProduct, lssRevision, serial)
	}
	lssRun := func(t *testing.T, steps ...string) (run, []map[string]any) {
		args := []string{"lss", "--interface", ifc, "--timeout-ms", "120000"}
		for _, s := range steps {
			args = append(args, "--step", s)
		}
		r := adapterRun(t, "", a, args...)
		var ev []map[string]any
		for _, d := range docs(t, r.out) {
			if d["type"] == "lss" {
				ev = append(ev, d)
			}
		}
		return r, ev
	}
	nodeID := func(t *testing.T, n *lssNode, id canopen.NodeID, mark int) {
		t.Helper()
		if !waitFor(5*time.Second, func() bool { return n.n.NodeID() == id }) {
			differ(t, "node-ID %v, want %d", n.n.NodeID(), id)
		}
		if !sp.bootUp(mark, id, 5*time.Second) {
			differ(t, "no boot-up of node %d", id)
		}
	}

	t.Run("unconfigured", func(t *testing.T) {
		n := startLSSNode(t, ifc, load(), base, lss.Unconfigured)
		sc(t, "lss-inquire", func(t *testing.T) {
			r, ev := lssRun(t, "switch-global:configuration", "inquire:vendor-id", "inquire:product-code",
				"inquire:revision", "inquire:serial", "inquire:node-id", "switch-global:waiting")
			if r.code != 0 {
				differ(t, "lss: %v", r)
			}
			var vals []string
			var node any
			for _, e := range ev {
				if e["step"] == "inquire" {
					if v, ok := e["value"].(string); ok {
						vals = append(vals, v)
					} else {
						node = e["node"]
					}
				}
			}
			want := []string{fmt.Sprintf("0x%08X", lssVendor), fmt.Sprintf("0x%08X", lssProduct), fmt.Sprintf("0x%08X", lssRevision), fmt.Sprintf("0x%08X", base)}
			if fmt.Sprint(vals) != fmt.Sprint(want) || node != 255.0 {
				differ(t, "inquired %v node-ID %v; want %v and 255", vals, node, want)
			}
			info(t).detail = fmt.Sprintf("identity %v, node-ID %v", vals, node)
		})
		sc(t, "lss-configure-node-id", func(t *testing.T) {
			mark := sp.mark()
			r, _ := lssRun(t, sel(base), "configure-node-id:40", "switch-global:waiting")
			if r.code != 0 {
				differ(t, "lss: %v", r)
			}
			nodeID(t, n, 40, mark)
			if !waitFor(5*time.Second, func() bool { return n.n.State() == canopen.StatePreOperational }) {
				differ(t, "node state %v after the node-ID was applied", n.n.State())
			}
			info(t).detail = "node-ID 40 applied (boot-up 0x728)"
		})
		sc(t, "lss-store", func(t *testing.T) {
			before := n.n.LSS().Stats().Stores
			r, _ := lssRun(t, sel(base), "store", "switch-global:waiting")
			if r.code != 0 {
				differ(t, "lss: %v", r)
			}
			if st := n.n.LSS().Stats(); st.Stores != before+1 || n.n.LSS().Pending().NodeID != 40 {
				differ(t, "stores %d (before %d), pending %+v", st.Stores, before, n.n.LSS().Pending())
			}
		})
		sc(t, "lss-bit-timing", func(t *testing.T) {
			if !c.Features["lssBitTiming"] {
				skipf(t, "%s's LSS master has no bit timing (lssBitTiming false)", a)
			}
			r, _ := lssRun(t, sel(base), "configure-bit-timing:3", "switch-global:waiting",
				"switch-global:configuration", "activate-bit-timing:50", "switch-global:waiting")
			if r.code != 0 {
				differ(t, "lss: %v", r)
			}
			ok := waitFor(3*time.Second, func() bool {
				n.mu.Lock()
				defer n.mu.Unlock()
				return len(n.timings) == 1 && n.timings[0] == lss.BitTiming250 && len(n.activate) == 1 && n.activate[0] == 50*time.Millisecond
			})
			n.mu.Lock()
			defer n.mu.Unlock()
			if !ok {
				differ(t, "bit timing %v, activations %v; want [250 kbit/s] and [50ms]", n.timings, n.activate)
			}
			info(t).detail = fmt.Sprintf("bit timing %v activated after %v", n.timings, n.activate)
		})
	})

	t.Run("fastscan", func(t *testing.T) {
		n := startLSSNode(t, ifc, load(), base+1, lss.Unconfigured)
		sc(t, "lss-fastscan", func(t *testing.T) {
			if !c.Features["lssFastscan"] {
				skipf(t, "%s's LSS master has no Fastscan (lssFastscan false)", a)
			}
			mark := sp.mark()
			r, ev := lssRun(t, "fastscan", "configure-node-id:41", "switch-global:waiting")
			if r.code != 0 || len(ev) == 0 {
				differ(t, "lss: %v", r)
			}
			addr, _ := ev[0]["address"].(map[string]any)
			want := map[string]any{"vendorId": fmt.Sprintf("0x%08X", lssVendor), "productCode": fmt.Sprintf("0x%08X", lssProduct),
				"revision": fmt.Sprintf("0x%08X", lssRevision), "serial": fmt.Sprintf("0x%08X", base+1)}
			if fmt.Sprint(addr) != fmt.Sprint(want) {
				differ(t, "found %v, want %v", addr, want)
			}
			nodeID(t, n, 41, mark)
			info(t).detail = fmt.Sprintf("found %v, node-ID 41", addr["serial"])
		})
	})

	t.Run("configured", func(t *testing.T) {
		startLSSNode(t, ifc, load(), base+2, goNode)
		sc(t, "lss-fastscan-configured", func(t *testing.T) {
			if !c.Features["lssFastscan"] {
				skipf(t, "%s's LSS master has no Fastscan (lssFastscan false)", a)
			}
			r, ev := lssRun(t, "fastscan")
			if r.code != 3 {
				differ(t, "fastscan with only the configured node %d on the bus: %v %v, want exit 3", goNode, r, ev)
			}
			info(t).detail = fmt.Sprintf("no answer from the configured node %d (exit 3)", goNode)
		})
	})

	t.Run("assign-all", func(t *testing.T) {
		var nodes []*lssNode
		if c.Features["lssAssignAll"] {
			for i := uint32(3); i <= 4; i++ {
				nodes = append(nodes, startLSSNode(t, ifc, load(), base+i, lss.Unconfigured))
			}
		}
		sc(t, "lss-assign-all", func(t *testing.T) {
			if !c.Features["lssAssignAll"] {
				skipf(t, "%s's LSS master has no assign-all service (lssAssignAll false)", a)
			}
			r, ev := lssRun(t, "assign-all:126:store")
			if r.code != 0 || len(ev) == 0 {
				differ(t, "lss: %v", r)
			}
			list, _ := ev[0]["assigned"].([]any)
			var got []string
			for _, x := range list {
				e, _ := x.(map[string]any)
				ad, _ := e["address"].(map[string]any)
				got = append(got, fmt.Sprintf("%v:%v", e["node"], ad["serial"]))
			}
			sort.Strings(got)
			want := []string{fmt.Sprintf("126:0x%08X", base+3), fmt.Sprintf("127:0x%08X", base+4)}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				differ(t, "assigned %v, want %v", got, want)
			}
			for i, n := range nodes {
				if !waitFor(5*time.Second, func() bool { return n.n.NodeID() == canopen.NodeID(126+i) }) {
					differ(t, "node with serial %#x has node-ID %v, want %d", base+3+uint32(i), n.n.NodeID(), 126+i)
				}
				if st := n.n.LSS().Stats(); st.Stores != 1 {
					differ(t, "node %d: %d stores, want 1", 126+i, st.Stores)
				}
			}
			info(t).detail = fmt.Sprintf("node-IDs %v by serial number, stored", got)
		})
	})
}
