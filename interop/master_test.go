// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// deviceNode is the node-ID of the stacks' devices.
const deviceNode canopen.NodeID = 5

// deviceEDS is the shared test device in the images.
const deviceEDS = "/fixtures/eds/interop-device.eds"

// domainSizes are the DOMAIN (0x2005) sizes transferred; the canopennode
// device backs the DOMAIN with 4096 bytes.
var domainSizes = []int{0, 1, 4, 7, 8, 100, 889, 1024, 4096}

// testWrite is a value written to a test object, with normal and block
// transfers.
type testWrite struct {
	index         uint16
	normal, block []byte
}

func le(x uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(x >> (8 * i))
	}
	return b
}

var testWrites = []testWrite{
	{0x2000, []byte{0x5A}, []byte{0xA5}},
	{0x2001, le(0xBEEF, 2), le(0x1234, 2)},
	{0x2002, le(0x01020304, 4), le(0xA1B2C3D4, 4)},
	{0x2003, le(uint64(0xFFFFFFFE), 4), le(uint64(0x7FFFFFF0), 4)}, // -2, 2147483632
	{0x2004, []byte("go-canopen interop string, sixty-four characters long: 0123456!"), []byte("block transfer of the visible string (64 characters) abcdefghijk")},
	{0x2006, le(0x3FC00000, 4), le(0xC2F60000, 4)}, // 1.5, -123.0
	{0x2007, le(0x1122334455667788, 8), le(0x8877665544332211, 8)},
	{0x2009, le(0xCAFEBABE, 4), le(0x0BADF00D, 4)},
	{0x200A, []byte{150}, []byte{10}},
}

func domainData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + n)
	}
	return b
}

func hexKey(index uint16, sub uint8) string { return fmt.Sprintf("0x%04X:%02X", index, sub) }

// newMaster starts a go-canopen master on its own socket.
func newMaster(t *testing.T, ifc string, opts ...master.Option) (*master.Master, *canbus.Bus) {
	t.Helper()
	bus := openBus(t, ifc)
	opts = append([]master.Option{master.WithSDOOptions(sdo.WithTimeout(2 * time.Second))}, opts...)
	m, err := master.New(bus, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, bus
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// deviceArgs are the arguments of a stack's device (node 5).
func deviceArgs(ifc string, extra ...string) []string {
	return append([]string{"device", "--interface", ifc, "--node-id", fmt.Sprint(deviceNode), "--eds", deviceEDS, "--timeout-ms", "900000"}, extra...)
}

// abortCode returns the abort code of err (0 if it is not an abort).
func abortCode(err error) canopen.AbortCode {
	var ae *canopen.AbortError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return 0
}

// readable lists the readable sub-objects of a dictionary.
func readable(d *od.Dictionary, from, to uint16) []*od.Variable {
	var vs []*od.Variable
	for _, o := range d.Objects() {
		if o.Index < from || o.Index > to {
			continue
		}
		for _, v := range o.Subs {
			if v.Access.Readable() {
				vs = append(vs, v)
			}
		}
	}
	return vs
}

// expectedUpload is what a fresh device holds in a sub-object: its EDS
// default for node 5, except where the device run changes it.
func expectedUpload(desc *od.Dictionary, v *od.Variable, heartbeat bool) []byte {
	if v.Index == od.IndexProducerHeartbeat && heartbeat {
		return le(100, 2)
	}
	b, _ := desc.Get(v.Index, v.SubIndex)
	return b
}

// TestMasterAgainstDevice: go-canopen as master and SDO client of each
// stack's device (node 5, the shared interop-device.eds), described by
// the same EDS read with eds.ParseFile.
func TestMasterAgainstDevice(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range adapters {
		t.Run(a, func(t *testing.T) {
			c := needImage(t, a)
			dir := workDir(t)
			f, err := eds.ParseFile(fixture(t, a, "eds/interop-device.eds", dir))
			if err != nil {
				t.Fatal(err)
			}
			desc, err := f.Dictionary(deviceNode)
			if err != nil {
				t.Fatal(err)
			}
			dcf, err := eds.ParseFile(fixture(t, a, "eds/interop-device-node5.dcf", dir))
			if err != nil {
				t.Fatal(err)
			}
			sc := func(t *testing.T, name string, fn func(t *testing.T)) { scenario(t, dirClient, a, name, fn) }
			t.Run("sdo", func(t *testing.T) { masterSDO(t, ifc, a, c, desc, sc) })
			t.Run("nmt", func(t *testing.T) { masterNMT(t, ifc, a, c, sc) })
			t.Run("heartbeat", func(t *testing.T) { masterHeartbeatLoss(t, ifc, a, c, sc) })
			t.Run("pdo", func(t *testing.T) { masterPDO(t, ifc, a, c, desc, sc) })
			t.Run("emcy", func(t *testing.T) { masterEMCY(t, ifc, a, c, sc) })
			t.Run("dcf", func(t *testing.T) { masterDCF(t, ifc, a, c, dcf, sc) })
			// Contract 1.1.
			load := func(rel string) func() *eds.File {
				return func() *eds.File {
					f, err := eds.ParseFile(fixture(t, a, rel, dir))
					if err != nil {
						t.Fatal(err)
					}
					return f
				}
			}
			other := load("eds/interop-device-other-product.eds")()
			t.Run("syncwindow", func(t *testing.T) { masterSyncWindow(t, ifc, a, c, desc, sc) })
			t.Run("lss", func(t *testing.T) { masterLSS(t, ifc, a, c, sc) })
			t.Run("boot", func(t *testing.T) { masterBoot(t, ifc, a, c, load("eds/interop-device.eds"), other, sc) })
			t.Run("mpdo", func(t *testing.T) { masterMPDO(t, ifc, a, c, load(mpdoEDS), sc) })
			t.Run("fd", func(t *testing.T) { fdSkips(t, a, []string{"fd-usdo-client", "fd-pdo"}, sc) })
		})
	}
}

type scenarioFunc func(t *testing.T, name string, fn func(t *testing.T))

// masterSDO: uploads of every object, downloads of every writable test
// object, the DOMAIN sizes, aborts, scan; verified with the device's dump.
func masterSDO(t *testing.T, ifc, a string, c caps, desc *od.Dictionary, sc scenarioFunc) {
	m, _ := newMaster(t, ifc)
	args := []string{}
	hb := c.Features["heartbeatProducer"]
	if hb {
		args = append(args, "--heartbeat-ms", "100")
	}
	for _, w := range testWrites {
		args = append(args, "--dump", hexKey(w.index, 0))
	}
	args = append(args, "--dump", "0x2005:00")
	dev := startContainer(t, a, deviceArgs(ifc, args...)...)
	dev.await(t, `"type":"ready"`, 90*time.Second)
	cl, err := m.SDO(deviceNode)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t, 10*time.Minute)
	block := sdo.UseBlockMode(sdo.BlockAlways)
	normal := sdo.UseBlockMode(sdo.BlockNever)
	modes := []struct {
		name string
		opt  sdo.CallOption
	}{{"", normal}, {"block-", block}}

	// Uploads of every readable sub-object, compared byte for byte with the
	// EDS default and as typed values.
	for _, mode := range modes {
		for _, v := range readable(desc, 0x1000, 0x2FFF) {
			v, mode := v, mode
			sc(t, mode.name+"upload-"+hexKey(v.Index, v.SubIndex), func(t *testing.T) {
				if mode.name != "" && !c.Features["deviceBlock"] {
					skipf(t, "%s's device has no SDO block transfer (deviceBlock false)", a)
				}
				want := expectedUpload(desc, v, hb)
				got, err := cl.Upload(ctx, v.Index, v.SubIndex, mode.opt)
				if err != nil {
					differ(t, "upload %s: %v (want %x)", hexKey(v.Index, v.SubIndex), err, want)
				}
				if !bytes.Equal(got, want) {
					differ(t, "upload %s = %x, EDS default %x", hexKey(v.Index, v.SubIndex), got, want)
				}
				gv, err1 := canopen.Decode(v.DataType, got)
				wv, err2 := canopen.Decode(v.DataType, want)
				if err1 != nil || err2 != nil || fmt.Sprint(gv) != fmt.Sprint(wv) {
					failf(t, "typed %s: %v (%v), want %v (%v)", v.DataType, gv, err1, wv, err2)
				}
			})
		}
	}

	// Downloads of every writable test object, read back (except the
	// write-only 0x2009, verified by the dump).
	last := map[string][]byte{}
	for _, mode := range modes {
		for _, w := range testWrites {
			w, mode := w, mode
			data := w.normal
			if mode.name != "" {
				data = w.block
			}
			sc(t, mode.name+"download-"+hexKey(w.index, 0), func(t *testing.T) {
				if mode.name != "" && !c.Features["deviceBlock"] {
					skipf(t, "%s's device has no SDO block transfer (deviceBlock false)", a)
				}
				if err := cl.Download(ctx, w.index, 0, data, mode.opt); err != nil {
					differ(t, "download %s %x: %v", hexKey(w.index, 0), data, err)
				}
				last[hexKey(w.index, 0)] = data
				if w.index == 0x2009 {
					return
				}
				got, err := cl.Upload(ctx, w.index, 0, mode.opt)
				if err != nil || !bytes.Equal(got, data) {
					differ(t, "read back %s = %x, %v; wrote %x", hexKey(w.index, 0), got, err, data)
				}
			})
		}
	}

	// DOMAIN 0x2005: every size, both ways.
	for _, mode := range modes {
		for _, n := range domainSizes {
			n, mode := n, mode
			sc(t, fmt.Sprintf("%sdomain-%d", mode.name, n), func(t *testing.T) {
				if mode.name != "" && !c.Features["deviceBlock"] {
					skipf(t, "%s's device has no SDO block transfer (deviceBlock false)", a)
				}
				data := domainData(n)
				if err := cl.Download(ctx, 0x2005, 0, data, mode.opt); err != nil {
					differ(t, "download of %d bytes: %v", n, err)
				}
				last["0x2005:00"] = data
				got, err := cl.Upload(ctx, 0x2005, 0, mode.opt)
				if err != nil {
					differ(t, "upload of %d bytes: %v", n, err)
				}
				if !bytes.Equal(got, data) {
					differ(t, "read back %d bytes, wrote %d (%x...)", len(got), n, got[:min(len(got), 16)])
				}
			})
		}
	}

	// Aborts.
	type abortCase struct {
		name    string
		feature string
		do      func() error
		want    []canopen.AbortCode
	}
	down := func(index uint16, sub uint8, data []byte) func() error {
		return func() error { return cl.Download(ctx, index, sub, data, normal) }
	}
	up := func(index uint16, sub uint8) func() error {
		return func() error { _, err := cl.Upload(ctx, index, sub, normal); return err }
	}
	aborts := []abortCase{
		{"abort-no-object", "", up(0x5FFF, 0), []canopen.AbortCode{canopen.AbortNoObject}},
		{"abort-no-object-download", "", down(0x5FFF, 0, []byte{1}), []canopen.AbortCode{canopen.AbortNoObject}},
		{"abort-no-subindex", "", up(0x2000, 1), []canopen.AbortCode{canopen.AbortNoSubIndex}},
		{"abort-no-subindex-record", "", up(0x1018, 7), []canopen.AbortCode{canopen.AbortNoSubIndex}},
		{"abort-read-write-only", "", up(0x2009, 0), []canopen.AbortCode{canopen.AbortWriteOnly}},
		{"abort-write-read-only", "", down(0x2008, 0, le(1, 4)), []canopen.AbortCode{canopen.AbortReadOnly}},
		{"abort-write-const", "", down(0x1008, 0, []byte("x")), []canopen.AbortCode{canopen.AbortReadOnly}},
		// CiA 301 allows the general length abort or the specific one.
		{"abort-length-high", "", down(0x2000, 0, []byte{1, 2}), []canopen.AbortCode{canopen.AbortLengthHigh, canopen.AbortLength}},
		{"abort-length-low", "", down(0x2001, 0, []byte{1}), []canopen.AbortCode{canopen.AbortLengthLow, canopen.AbortLength}},
		{"abort-length-high-segmented", "", down(0x2002, 0, []byte{1, 2, 3, 4, 5, 6}), []canopen.AbortCode{canopen.AbortLengthHigh, canopen.AbortLength}},
		{"abort-limit-low", "deviceLimits", down(0x200A, 0, []byte{5}), []canopen.AbortCode{canopen.AbortValueLow}},
		{"abort-limit-high", "deviceLimits", down(0x200A, 0, []byte{201}), []canopen.AbortCode{canopen.AbortValueHigh}},
	}
	for _, ac := range aborts {
		ac := ac
		sc(t, ac.name, func(t *testing.T) {
			if ac.feature != "" && !c.Features[ac.feature] {
				skipf(t, "%s's device does not check LowLimit/HighLimit (%s false)", a, ac.feature)
			}
			err := ac.do()
			got := abortCode(err)
			for _, w := range ac.want {
				if got == w {
					info(t).detail = fmt.Sprintf("abort 0x%08X", uint32(got))
					return
				}
			}
			differ(t, "got %v (abort 0x%08X), want abort %v", err, uint32(got), ac.want)
		})
	}
	// The limits: the values in range are accepted.
	sc(t, "limits-in-range", func(t *testing.T) {
		for _, x := range []byte{10, 200} {
			if err := cl.Download(ctx, 0x200A, 0, []byte{x}, normal); err != nil {
				differ(t, "write %d to 0x200A (limits 10..200): %v", x, err)
			}
		}
		_ = cl.Download(ctx, 0x200A, 0, last["0x200A:00"], normal)
	})

	sc(t, "scan-identity", func(t *testing.T) {
		nodes := []canopen.NodeID{1, 2, 3, 4, 5, 6, 7, 8}
		found, err := m.Scan(ctx, nodes)
		if err != nil {
			failf(t, "scan: %v", err)
		}
		for _, n := range found {
			if n.Node != deviceNode {
				continue
			}
			id := n.Identity
			if n.DeviceType != 0x000F0191 || id.VendorID != 0 || id.ProductCode != 0x434F4931 || id.Revision != 0x00010000 || id.SerialNumber != 1 {
				differ(t, "node 5: %+v", n)
			}
			return
		}
		failf(t, "node 5 not found: %+v", found)
	})

	sc(t, "device-dump", func(t *testing.T) {
		res := dev.stop(t)
		vals, _ := res["values"].(map[string]any)
		if vals == nil {
			failf(t, "no device result: %v", res)
		}
		var bad, padded []string
		for _, k := range sortedKeys(last) {
			got, _ := vals[k].(string)
			switch {
			case got == hex.EncodeToString(last[k]):
			case k == "0x2004:00" && nulCut(got) == hex.EncodeToString(last[k]):
				padded = append(padded, k)
			default:
				bad = append(bad, fmt.Sprintf("%s = %v, wrote %x", k, vals[k], last[k]))
			}
		}
		if len(bad) > 0 {
			differ(t, "%s", strings.Join(bad, "; "))
		}
		if len(padded) > 0 {
			differKind(t, "nul-terminated", "%s dumped with NUL padding", strings.Join(padded, ","))
		}
	})
}

// eventLog collects a master's events.
type eventLog struct {
	mu  sync.Mutex
	evs []master.Event
}

func (l *eventLog) add(e master.Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *eventLog) has(f func(master.Event) bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.evs {
		if f(e) {
			return true
		}
	}
	return false
}

func (l *eventLog) wait(ctx context.Context, f func(master.Event) bool) bool {
	for {
		if l.has(f) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (l *eventLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var s []string
	for _, e := range l.evs {
		s = append(s, e.String())
	}
	return strings.Join(s, "; ")
}

// masterNMT: NMT commands, boot-ups and heartbeat states observed by the
// master's heartbeat consumer.
func masterNMT(t *testing.T, ifc, a string, c caps, sc scenarioFunc) {
	var log eventLog
	m, _ := newMaster(t, ifc, master.WithEventHandler(log.add))
	hb := c.Features["heartbeatProducer"]
	args := []string{}
	if hb {
		args = append(args, "--heartbeat-ms", "100")
	}
	since := time.Now()
	dev := startContainer(t, a, deviceArgs(ifc, args...)...)
	dev.await(t, `"type":"ready"`, 90*time.Second)
	ctx := ctxT(t, 5*time.Minute)
	wait := func(t *testing.T, d time.Duration, f func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return f(ctx)
	}
	needHB := func(t *testing.T) {
		if !hb {
			skipf(t, "%s's device has no heartbeat producer", a)
		}
	}
	state := func(t *testing.T, st canopen.NMTState) {
		t.Helper()
		if err := wait(t, 10*time.Second, func(ctx context.Context) error { return m.WaitState(ctx, deviceNode, st) }); err != nil {
			differ(t, "state %v not reported by heartbeat (now %v): %v; events: %s", st, m.State(deviceNode), err, log.String())
		}
	}
	sc(t, "bootup", func(t *testing.T) {
		if err := wait(t, 10*time.Second, func(ctx context.Context) error { return m.WaitBootUp(ctx, deviceNode, since) }); err != nil {
			differ(t, "no boot-up: %v; events: %s", err, log.String())
		}
	})
	sc(t, "heartbeat-pre-operational", func(t *testing.T) {
		needHB(t)
		state(t, canopen.StatePreOperational)
	})
	steps := []struct {
		name string
		cmd  func(context.Context) error
		st   canopen.NMTState
	}{
		{"nmt-start", func(ctx context.Context) error { return m.StartNode(ctx, deviceNode) }, canopen.StateOperational},
		{"nmt-stop", func(ctx context.Context) error { return m.StopNode(ctx, deviceNode) }, canopen.StateStopped},
		{"nmt-pre-operational", func(ctx context.Context) error { return m.PreOperational(ctx, deviceNode) }, canopen.StatePreOperational},
		{"nmt-start-all", func(ctx context.Context) error { return m.NMT(ctx, 0, canopen.NMTStart) }, canopen.StateOperational},
		{"nmt-pre-operational-all", func(ctx context.Context) error {
			return m.NMT(ctx, 0, canopen.NMTEnterPreOperational)
		}, canopen.StatePreOperational},
	}
	for _, s := range steps {
		s := s
		sc(t, s.name, func(t *testing.T) {
			needHB(t)
			if err := s.cmd(ctx); err != nil {
				failf(t, "%v", err)
			}
			state(t, s.st)
		})
	}
	resets := []struct {
		name string
		cmd  func(context.Context) error
	}{
		{"nmt-reset-communication", func(ctx context.Context) error { return m.ResetCommunication(ctx, deviceNode) }},
		{"nmt-reset-node", func(ctx context.Context) error { return m.ResetNode(ctx, deviceNode) }},
	}
	for _, r := range resets {
		r := r
		sc(t, r.name, func(t *testing.T) {
			if err := m.StartNode(ctx, deviceNode); err != nil {
				failf(t, "%v", err)
			}
			if hb {
				state(t, canopen.StateOperational)
			}
			since := time.Now()
			if err := r.cmd(ctx); err != nil {
				failf(t, "%v", err)
			}
			if err := wait(t, 30*time.Second, func(ctx context.Context) error { return m.WaitBootUp(ctx, deviceNode, since) }); err != nil {
				differ(t, "no boot-up after %s: %v; events: %s", r.name, err, log.String())
			}
			if hb {
				state(t, canopen.StatePreOperational)
			}
		})
	}

}

// masterHeartbeatLoss: the master supervises the device's heartbeat
// (Monitor) and reports its loss when the device ends.
func masterHeartbeatLoss(t *testing.T, ifc, a string, c caps, sc scenarioFunc) {
	sc(t, "heartbeat-lost", func(t *testing.T) {
		if !c.Features["heartbeatProducer"] {
			skipf(t, "%s's device has no heartbeat producer", a)
		}
		var log eventLog
		m, _ := newMaster(t, ifc, master.WithEventHandler(log.add))
		dev := startContainer(t, a, deviceArgs(ifc, "--heartbeat-ms", "100")...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		if err := m.Monitor(deviceNode, 500*time.Millisecond); err != nil {
			failf(t, "%v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := m.WaitState(ctx, deviceNode, canopen.StatePreOperational); err != nil {
			failf(t, "no heartbeat: %v; events: %s", err, log.String())
		}
		time.Sleep(300 * time.Millisecond)
		if st := m.Status(deviceNode); !st.Active {
			failf(t, "supervision not active: %+v", st)
		}
		dev.stop(t)
		lost := func(e master.Event) bool { return e.Kind == master.EventHeartbeatLost && e.Node == deviceNode }
		if !log.wait(ctx, lost) {
			failf(t, "no heartbeat loss reported; events: %s", log.String())
		}
	})
}

// nulCut cuts a hex string at the first NUL byte.
func nulCut(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil {
		return h
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return hex.EncodeToString(b)
}

// masterPDO: the device's TPDO1 (counter ticking), TPDO2 remapped by
// pdo.Configure, TPDO3 driven by the master's SYNC, RPDO1 to the device.
func masterPDO(t *testing.T, ifc, a string, c caps, desc *od.Dictionary, sc scenarioFunc) {
	m, bus := newMaster(t, ifc)
	args := []string{"--dump", "0x2001:00", "--dump", "0x2003:00"}
	if c.Features["tick"] {
		args = append(args, "--tick-ms", "20")
	}
	dev := startContainer(t, a, deviceArgs(ifc, args...)...)
	dev.await(t, `"type":"ready"`, 90*time.Second)
	cl, err := m.SDO(deviceNode)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t, 5*time.Minute)
	needTPDO := func(t *testing.T) {
		if !c.Features["deviceTpdo"] {
			skipf(t, "%s's device transmits no TPDOs (deviceTpdo false)", a)
		}
	}
	// listen decodes n PDOs of a COB-ID with a mapping read by SDO.
	listen := func(t *testing.T, n int, tpdo int, sync bool) [][]pdo.Value {
		t.Helper()
		cfg, err := pdo.ReadConfig(ctx, pdoSDO{cl}, true, tpdo)
		if err != nil {
			failf(t, "read TPDO%d: %v", tpdo, err)
		}
		dec, err := pdo.NewDecoder(cfg.Mapping, desc)
		if err != nil {
			failf(t, "decoder: %v", err)
		}
		ch := make(chan []pdo.Value, 256)
		l := pdo.NewListener(bus, cfg.COBID, dec, func(_ *canbus.Frame, v []pdo.Value) {
			select {
			case ch <- v:
			default:
			}
		})
		if err := l.Start(); err != nil {
			failf(t, "listener: %v", err)
		}
		defer l.Close()
		if sync {
			m.SYNC().SetPeriod(10 * time.Millisecond)
			defer m.SYNC().SetPeriod(0)
		}
		var out [][]pdo.Value
		timeout := time.After(10 * time.Second)
		for len(out) < n {
			select {
			case v := <-ch:
				out = append(out, v)
			case <-timeout:
				differ(t, "%d of %d TPDO%d (COB-ID %v) received", len(out), n, tpdo, cfg.COBID)
			}
		}
		return out
	}

	sc(t, "tpdo1-read-config", func(t *testing.T) {
		cfg, err := pdo.ReadConfig(ctx, pdoSDO{cl}, true, 1)
		if err != nil {
			failf(t, "%v", err)
		}
		want := pdo.Mapping{{Index: 0x2002, SubIndex: 0, Bits: 32}}
		if cfg.COBID.ID() != 0x185 || !cfg.COBID.Valid() || cfg.TransmissionType != 255 || fmt.Sprint(cfg.Mapping) != fmt.Sprint(want) {
			differ(t, "TPDO1 %+v", cfg)
		}
	})
	sc(t, "nmt-start", func(t *testing.T) {
		if err := m.StartNode(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
	})
	sc(t, "tpdo1-counter", func(t *testing.T) {
		needTPDO(t)
		if !c.Features["tick"] {
			skipf(t, "%s's device has no --tick-ms", a)
		}
		vals := listen(t, 5, 1, false)
		var prev uint64
		for i, v := range vals {
			if len(v) != 1 || v[0].Index != 0x2002 || v[0].Type != canopen.Unsigned32 {
				failf(t, "PDO %d: %v", i, v)
			}
			x := v[0].Value.(uint64)
			if i > 0 && x <= prev {
				differ(t, "counter not increasing: %v", vals)
			}
			prev = x
		}
		info(t).detail = fmt.Sprintf("counter %v .. %d", vals[0][0].Value, prev)
	})
	sc(t, "tpdo2-remap", func(t *testing.T) {
		needTPDO(t)
		if err := m.PreOperational(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
		cfg := pdo.Config{
			COBID:            canopen.NewCOBID(0x285),
			TransmissionType: 255,
			EventTimer:       50,
			Mapping: pdo.Mapping{
				{Index: 0x2000, SubIndex: 0, Bits: 8},
				{Index: 0x2001, SubIndex: 0, Bits: 16},
				{Index: 0x2003, SubIndex: 0, Bits: 32},
			},
		}
		if err := pdo.Configure(ctx, pdoSDO{cl}, true, 2, cfg); err != nil {
			differ(t, "configure TPDO2: %v", err)
		}
		back, err := pdo.ReadConfig(ctx, pdoSDO{cl}, true, 2)
		if err != nil || back.COBID.ID() != 0x285 || !back.COBID.Valid() || back.EventTimer != 50 || fmt.Sprint(back.Mapping) != fmt.Sprint(cfg.Mapping) {
			differ(t, "TPDO2 read back %+v, %v", back, err)
		}
		if err := m.StartNode(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
		vals := listen(t, 3, 2, false)
		v := vals[len(vals)-1]
		if len(v) != 3 || v[0].Value != uint64(0x11) || v[1].Value != uint64(0x2233) || v[2].Value != int64(-12345) {
			differ(t, "TPDO2 values %v", v)
		}
	})
	sc(t, "tpdo3-sync", func(t *testing.T) {
		needTPDO(t)
		if err := m.PreOperational(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
		cfg := pdo.Config{
			COBID:            canopen.NewCOBID(0x385),
			TransmissionType: 1,
			Mapping:          pdo.Mapping{{Index: 0x2008, SubIndex: 0, Bits: 32}},
		}
		if err := pdo.Configure(ctx, pdoSDO{cl}, true, 3, cfg); err != nil {
			differ(t, "configure TPDO3: %v", err)
		}
		if err := m.StartNode(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
		vals := listen(t, 5, 3, true)
		if v := vals[0]; len(v) != 1 || v[0].Value != uint64(0x12345678) {
			differ(t, "TPDO3 values %v", v)
		}
	})
	sc(t, "rpdo1", func(t *testing.T) {
		if !c.Features["deviceRpdo"] {
			skipf(t, "%s's device applies no RPDOs (deviceRpdo false)", a)
		}
		if err := m.StartNode(ctx, deviceNode); err != nil {
			failf(t, "%v", err)
		}
		s, err := m.RPDOSender(ctx, deviceNode, 1, desc)
		if err != nil {
			failf(t, "RPDO1 sender: %v", err)
		}
		if err := s.Send(ctx, uint16(0x7788), int32(-99)); err != nil {
			failf(t, "send: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			a, err1 := cl.ReadUint(ctx, 0x2001, 0)
			b, err2 := cl.ReadInt(ctx, 0x2003, 0)
			if err1 == nil && err2 == nil && a == 0x7788 && b == -99 {
				break
			}
			if time.Now().After(deadline) {
				differ(t, "after RPDO1: 0x2001 = %#x (%v), 0x2003 = %d (%v)", a, err1, b, err2)
			}
			time.Sleep(50 * time.Millisecond)
		}
		res := dev.stop(t)
		vals, _ := res["values"].(map[string]any)
		if vals["0x2001:00"] != "8877" || vals["0x2003:00"] != "9dffffff" {
			differ(t, "device dump %v", vals)
		}
	})
}

// masterEMCY: the device's EMCY decoded by an emcy consumer and the master.
func masterEMCY(t *testing.T, ifc, a string, c caps, sc scenarioFunc) {
	sc(t, "emcy", func(t *testing.T) {
		if !c.Features["emcy"] {
			skipf(t, "%s's device sends no EMCY on request", a)
		}
		var log eventLog
		m, bus := newMaster(t, ifc, master.WithEventHandler(log.add))
		ch := make(chan emcy.Event, 16)
		cons := emcy.NewConsumer(bus, emcy.WithHandler(func(e emcy.Event) { ch <- e }))
		if err := cons.Start(); err != nil {
			t.Fatal(err)
		}
		defer cons.Close()
		dev := startContainer(t, a, deviceArgs(ifc, "--emcy", "1000:01:2b01020304", "--emcy-after-ms", "1000")...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		select {
		case e := <-ch:
			if e.Node != deviceNode || e.Code != 0x1000 || e.Register != 0x01 || e.MSEF != [5]byte{0x2b, 1, 2, 3, 4} || e.COBID.ID() != 0x85 {
				differ(t, "EMCY %+v", e)
			}
			info(t).detail = fmt.Sprintf("code 0x%04X (%s), register 0x%02X, msef %x", e.Code, emcy.CodeText(e.Code), uint8(e.Register), e.MSEF)
		case <-time.After(15 * time.Second):
			failf(t, "no EMCY within 15 s")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !log.wait(ctx, func(e master.Event) bool { return e.Kind == master.EventEMCY && e.Node == deviceNode }) {
			failf(t, "master: no EventEMCY")
		}
		if last, ok := m.LastEMCY(deviceNode); !ok || last.Code != 0x1000 {
			failf(t, "master LastEMCY %+v %v", last, ok)
		}
	})
}

// masterDCF: ConfigureFromDCF with interop-device-node5.dcf, checked by
// SDO and the device's dump.
func masterDCF(t *testing.T, ifc, a string, c caps, dcf *eds.File, sc scenarioFunc) {
	sc(t, "configure-from-dcf", func(t *testing.T) {
		m, _ := newMaster(t, ifc)
		dev := startContainer(t, a, deviceArgs(ifc, "--dump", "0x1017:00", "--dump", "0x2001:00", "--dump", "0x2004:00")...)
		dev.await(t, `"type":"ready"`, 90*time.Second)
		ctx := ctxT(t, time.Minute)
		entries, err := master.DCFEntries(dcf, 0)
		if err != nil {
			failf(t, "DCF entries: %v", err)
		}
		if err := m.ConfigureFromDCF(ctx, deviceNode, dcf); err != nil {
			differ(t, "ConfigureFromDCF (%d entries): %v", len(entries), err)
		}
		cl, _ := m.SDO(deviceNode)
		if v, err := cl.ReadUint(ctx, 0x2001, 0); err != nil || v != 0x4455 {
			differ(t, "0x2001 = %#x, %v", v, err)
		}
		if s, err := cl.ReadString(ctx, 0x2004, 0); err != nil || s != "configured by DCF" {
			differ(t, "0x2004 = %q, %v", s, err)
		}
		res := dev.stop(t)
		vals, _ := res["values"].(map[string]any)
		want := map[string]string{"0x1017:00": "e803", "0x2001:00": "5544", "0x2004:00": hex.EncodeToString([]byte("configured by DCF"))}
		padded := false
		for _, k := range sortedKeys(want) {
			got, _ := vals[k].(string)
			switch {
			case got == want[k]:
			case k == "0x2004:00" && nulCut(got) == want[k]:
				padded = true
			default:
				differ(t, "device dump %s = %v, want %s (%v)", k, vals[k], want[k], vals)
			}
		}
		if padded {
			differKind(t, "nul-terminated", "0x2004:00 dumped as %v", vals["0x2004:00"])
		}
		info(t).detail = fmt.Sprintf("%d entries", len(entries))
	})
}

// pdoSDO adapts an SDO client to pdo.SDO.
type pdoSDO struct{ c *sdo.Client }

func (p pdoSDO) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	return p.c.Upload(ctx, index, sub)
}

func (p pdoSDO) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	return p.c.Download(ctx, index, sub, data)
}
