// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// goNode is the node-ID of the go-canopen node the stacks use; driveNode
// the one of the go-canopen CiA 402 drive.
const (
	goNode    canopen.NodeID = 6
	driveNode canopen.NodeID = 7
)

// spy records the frames on the interface (its own socket).
type spy struct {
	mu     sync.Mutex
	frames []canbus.Frame
}

func newSpy(t *testing.T, ifc string) *spy {
	t.Helper()
	bus := openBus(t, ifc)
	sub, err := bus.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	s := &spy{}
	go func() {
		for f := range sub.Frames() {
			s.mu.Lock()
			s.frames = append(s.frames, f)
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = sub.Close() })
	return s
}

func (s *spy) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// since returns the frames with the identifier received after mark.
func (s *spy) since(mark int, id canbus.ID) []canbus.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []canbus.Frame
	for _, f := range s.frames[mark:] {
		if f.ID == id {
			out = append(out, f)
		}
	}
	return out
}

// bootUp waits for a boot-up message (0x700+node, data 00) after mark.
func (s *spy) bootUp(mark int, node canopen.NodeID, d time.Duration) bool {
	return waitFor(d, func() bool {
		for _, f := range s.since(mark, canbus.ID(0x700+uint32(node))) {
			if f.Len == 1 && f.Data[0] == 0 {
				return true
			}
		}
		return false
	})
}

// waitFrames waits until n frames with the identifier arrived after mark.
func (s *spy) waitFrames(mark int, id canbus.ID, n int, d time.Duration) []canbus.Frame {
	deadline := time.Now().Add(d)
	for {
		fs := s.since(mark, id)
		if len(fs) >= n || time.Now().After(deadline) {
			return fs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// goDevice is a go-canopen node built from interop-device.eds.
type goDevice struct {
	n    *node.Node
	d    *od.Dictionary
	stop func()
}

// startGoDevice starts a go-canopen node with the dictionary of the EDS,
// producing heartbeats every 100 ms (0x1017, kept across resets).
func startGoDevice(t *testing.T, ifc string, f *eds.File, id canopen.NodeID, opts ...node.Option) *goDevice {
	t.Helper()
	if o := f.Object(od.IndexProducerHeartbeat); o != nil {
		o.DefaultValue = "100"
	}
	d, err := f.Dictionary(id)
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.New(openBus(t, ifc), d, opts...)
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
	return g
}

// tick increments 0x2002 every period until the returned function is
// called.
func (g *goDevice) tick(period time.Duration) func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(period)
		defer tk.Stop()
		for i := uint64(1); ; i++ {
			select {
			case <-done:
				return
			case <-tk.C:
				_ = g.d.SetUint(0x2002, 0, i)
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// stackObjects are the sub-objects the stacks' clients upload from the
// go-canopen node.
var stackObjects = []struct {
	index uint16
	sub   uint8
}{
	{0x1000, 0}, {0x1001, 0}, {0x1008, 0}, {0x1017, 0},
	{0x1018, 0}, {0x1018, 1}, {0x1018, 2}, {0x1018, 3}, {0x1018, 4},
	{0x1200, 1}, {0x1800, 1}, {0x1A00, 1},
	{0x2000, 0}, {0x2001, 0}, {0x2002, 0}, {0x2003, 0}, {0x2004, 0}, {0x2005, 0},
	{0x2006, 0}, {0x2007, 0}, {0x2008, 0}, {0x200A, 0},
}

// TestStackAgainstGoNode: each stack as SDO client, NMT master, consumer,
// PDO tool, SYNC producer and CiA 402 client of a go-canopen node (node
// 6, the dictionary of interop-device.eds) running in this process.
func TestStackAgainstGoNode(t *testing.T) {
	ifc := needVCAN(t)
	for _, a := range adapters {
		t.Run(a, func(t *testing.T) {
			c := needImage(t, a)
			dir := workDir(t)
			edsPath := fixture(t, a, "eds/interop-device.eds", dir)
			load := func() *eds.File {
				f, err := eds.ParseFile(edsPath)
				if err != nil {
					t.Fatal(err)
				}
				return f
			}
			sc := func(t *testing.T, name string, fn func(t *testing.T)) { scenario(t, dirDevice, a, name, fn) }
			t.Run("sdo", func(t *testing.T) { stackSDO(t, ifc, a, c, load(), sc) })
			t.Run("nmt", func(t *testing.T) { stackNMT(t, ifc, a, c, load(), sc) })
			t.Run("listen", func(t *testing.T) { stackListen(t, ifc, a, c, load(), sc) })
			t.Run("pdo", func(t *testing.T) { stackPDO(t, ifc, a, c, load(), sc) })
			t.Run("cia402", func(t *testing.T) { stackCiA402(t, ifc, a, c, load(), sc) })
		})
	}
}

func stackSDO(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	g := startGoDevice(t, ifc, f, goNode)
	node := fmt.Sprint(goNode)
	upload := func(t *testing.T, index uint16, sub uint8, block bool) run {
		args := []string{"sdo-upload", "--interface", ifc, "--node-id", node, "--index", fmt.Sprintf("0x%04X", index), "--subindex", fmt.Sprint(sub)}
		if block {
			args = append(args, "--block")
		}
		return adapterRun(t, "", a, args...)
	}
	download := func(t *testing.T, index uint16, sub uint8, data []byte, block bool) run {
		args := []string{"sdo-download", "--interface", ifc, "--node-id", node, "--index", fmt.Sprintf("0x%04X", index), "--subindex", fmt.Sprint(sub), "--data", hex.EncodeToString(data)}
		if block {
			args = append(args, "--block")
		}
		return adapterRun(t, "", a, args...)
	}
	modes := []struct {
		name  string
		block bool
	}{{"", false}, {"block-", true}}
	needBlock := func(t *testing.T, block bool) {
		if block && !c.Features["sdoBlock"] {
			skipf(t, "%s's SDO client has no block transfer (sdoBlock false)", a)
		}
	}
	for _, mode := range modes {
		for _, o := range stackObjects {
			o, mode := o, mode
			sc(t, mode.name+"upload-"+hexKey(o.index, o.sub), func(t *testing.T) {
				needBlock(t, mode.block)
				want, err := g.d.Get(o.index, o.sub)
				if err != nil {
					t.Fatal(err)
				}
				r := upload(t, o.index, o.sub, mode.block)
				if r.code != 0 {
					differ(t, "upload %s: %v", hexKey(o.index, o.sub), r)
				}
				if got, _ := r.doc["data"].(string); got != hex.EncodeToString(want) {
					differ(t, "upload %s = %q, dictionary %x", hexKey(o.index, o.sub), got, want)
				}
			})
		}
	}
	for _, mode := range modes {
		for _, w := range testWrites {
			w, mode := w, mode
			data := w.normal
			if mode.block {
				data = w.block
			}
			sc(t, mode.name+"download-"+hexKey(w.index, 0), func(t *testing.T) {
				needBlock(t, mode.block)
				r := download(t, w.index, 0, data, mode.block)
				if r.code != 0 {
					differ(t, "download %s %x: %v", hexKey(w.index, 0), data, r)
				}
				if got, _ := g.d.Get(w.index, 0); !bytes.Equal(got, data) {
					differ(t, "dictionary %s = %x, written %x", hexKey(w.index, 0), got, data)
				}
			})
		}
	}
	for _, mode := range modes {
		for _, n := range domainSizes {
			n, mode := n, mode
			sc(t, fmt.Sprintf("%sdomain-%d", mode.name, n), func(t *testing.T) {
				needBlock(t, mode.block)
				data := domainData(n)
				r := download(t, 0x2005, 0, data, mode.block)
				if r.code != 0 {
					differ(t, "download of %d bytes: %v", n, r)
				}
				if got, _ := g.d.Get(0x2005, 0); !bytes.Equal(got, data) {
					differ(t, "dictionary holds %d bytes, written %d", len(got), n)
				}
				r = upload(t, 0x2005, 0, mode.block)
				if got, _ := r.doc["data"].(string); r.code != 0 || got != hex.EncodeToString(data) {
					differ(t, "upload of %d bytes: %v", n, firstLine(r.String()))
				}
			})
		}
	}
	aborts := []struct {
		name string
		r    func(t *testing.T) run
		want []string
	}{
		{"abort-no-object", func(t *testing.T) run { return upload(t, 0x5FFF, 0, false) }, []string{"0x06020000"}},
		{"abort-no-subindex", func(t *testing.T) run { return upload(t, 0x2000, 1, false) }, []string{"0x06090011"}},
		{"abort-read-write-only", func(t *testing.T) run { return upload(t, 0x2009, 0, false) }, []string{"0x06010001"}},
		{"abort-write-read-only", func(t *testing.T) run { return download(t, 0x2008, 0, le(1, 4), false) }, []string{"0x06010002"}},
		{"abort-length-high", func(t *testing.T) run { return download(t, 0x2000, 0, []byte{1, 2}, false) }, []string{"0x06070012", "0x06070010"}},
		{"abort-length-low", func(t *testing.T) run { return download(t, 0x2001, 0, []byte{1}, false) }, []string{"0x06070013", "0x06070010"}},
		{"abort-limit-low", func(t *testing.T) run { return download(t, 0x200A, 0, []byte{5}, false) }, []string{"0x06090032"}},
		{"abort-limit-high", func(t *testing.T) run { return download(t, 0x200A, 0, []byte{201}, false) }, []string{"0x06090031"}},
	}
	for _, ab := range aborts {
		ab := ab
		sc(t, ab.name, func(t *testing.T) {
			r := ab.r(t)
			got, _ := r.doc["abortCode"].(string)
			if r.code == 5 {
				for _, w := range ab.want {
					if got == w {
						info(t).detail = "abort " + got
						return
					}
				}
			}
			differ(t, "want exit 5 and abort %v: %v", ab.want, r)
		})
	}
}

func stackNMT(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	sp := newSpy(t, ifc)
	g := startGoDevice(t, ifc, f, goNode)
	nmtRun := func(t *testing.T, cmd string) {
		r := adapterRun(t, "", a, "nmt", "--interface", ifc, "--node-id", fmt.Sprint(goNode), "--command", cmd)
		if r.code != 0 {
			differ(t, "nmt %s: %v", cmd, r)
		}
	}
	state := func(t *testing.T, st canopen.NMTState) {
		if !waitFor(5*time.Second, func() bool { return g.n.State() == st }) {
			differ(t, "node state %v, want %v", g.n.State(), st)
		}
	}
	for _, s := range []struct {
		cmd string
		st  canopen.NMTState
	}{
		{"start", canopen.StateOperational},
		{"stop", canopen.StateStopped},
		{"pre-operational", canopen.StatePreOperational},
		{"start", canopen.StateOperational},
	} {
		s := s
		sc(t, "nmt-"+s.cmd+"-"+s.st.String(), func(t *testing.T) {
			nmtRun(t, s.cmd)
			state(t, s.st)
		})
	}
	sc(t, "nmt-reset-communication", func(t *testing.T) {
		_ = g.d.SetUint(0x2000, 0, 0x77)
		mark := sp.mark()
		nmtRun(t, "reset-communication")
		if !sp.bootUp(mark, goNode, 5*time.Second) {
			differ(t, "no boot-up of node %d after reset communication", goNode)
		}
		state(t, canopen.StatePreOperational)
		if v, _ := g.d.GetUint(0x2000, 0); v != 0x77 {
			differ(t, "reset communication reset the application area: 0x2000 = %#x", v)
		}
	})
	sc(t, "nmt-reset-node", func(t *testing.T) {
		if err := g.n.SetState(canopen.StateOperational); err != nil {
			t.Fatal(err)
		}
		_ = g.d.SetUint(0x2000, 0, 0x77)
		mark := sp.mark()
		nmtRun(t, "reset-node")
		if !sp.bootUp(mark, goNode, 5*time.Second) {
			differ(t, "no boot-up of node %d after reset node", goNode)
		}
		state(t, canopen.StatePreOperational)
		if v, _ := g.d.GetUint(0x2000, 0); v != 0x11 {
			differ(t, "0x2000 = %#x after reset node, want the default 0x11", v)
		}
	})
}

func stackListen(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	if !c.op("listen") {
		for _, n := range []string{"listen-bootup", "listen-heartbeat", "listen-emcy"} {
			sc(t, n, func(t *testing.T) { skipf(t, "%s has no listen", a) })
		}
		return
	}
	g := startGoDevice(t, ifc, f, goNode)
	l := startContainer(t, a, "listen", "--interface", ifc, "--node-id", fmt.Sprint(goNode), "--count", "50", "--timeout-ms", "12000")
	l.await(t, `"type":"ready"`, 90*time.Second)
	time.Sleep(time.Second) // lely's listen resets the network first
	ctx := ctxT(t, time.Minute)
	if err := g.n.Reset(ctx, nmt.ResetApplication); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := g.n.SetState(canopen.StateOperational); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := g.n.EMCY().Raise(ctx, 0x2310, 0x02, [5]byte{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	_, _ = l.wait(t, 60*time.Second)
	evs := docs(t, l.stdout())
	find := func(f func(map[string]any) bool) map[string]any {
		for _, e := range evs {
			if f(e) {
				return e
			}
		}
		return nil
	}
	num := func(v any) float64 { x, _ := v.(float64); return x }
	sc(t, "listen-bootup", func(t *testing.T) {
		if !c.Features["listenBootup"] {
			skipf(t, "%s does not report boot-ups", a)
		}
		if find(func(e map[string]any) bool { return e["type"] == "bootup" && num(e["node"]) == float64(goNode) }) == nil {
			differ(t, "no boot-up event: %s", l.stdout())
		}
	})
	sc(t, "listen-heartbeat", func(t *testing.T) {
		if !c.Features["listenHeartbeat"] {
			skipf(t, "%s does not report heartbeats", a)
		}
		if find(func(e map[string]any) bool {
			return e["type"] == "heartbeat" && num(e["node"]) == float64(goNode) && e["state"] == "operational"
		}) == nil {
			differ(t, "no heartbeat event with state operational: %s", l.stdout())
		}
	})
	sc(t, "listen-emcy", func(t *testing.T) {
		if !c.Features["listenEmcy"] {
			skipf(t, "%s does not report EMCY", a)
		}
		e := find(func(e map[string]any) bool { return e["type"] == "emcy" && num(e["node"]) == float64(goNode) })
		// Raise sets the generic error bit (0x01) with the class bit.
		if e == nil || e["code"] != "0x2310" || e["register"] != "0x03" || e["data"] != "0102030405" {
			differ(t, "EMCY event %v: %s", e, l.stdout())
		}
	})
}

func stackPDO(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	sp := newSpy(t, ifc)
	g := startGoDevice(t, ifc, f, goNode)
	node := fmt.Sprint(goNode)
	sc(t, "pdo-receive-tpdo1", func(t *testing.T) {
		if !c.op("pdo-receive") {
			skipf(t, "%s has no pdo-receive", a)
		}
		if err := g.n.SetState(canopen.StateOperational); err != nil {
			t.Fatal(err)
		}
		stop := g.tick(20 * time.Millisecond)
		defer stop()
		r := adapterRun(t, "", a, "pdo-receive", "--interface", ifc, "--node-id", node, "--tpdo", "1", "--count", "5", "--timeout-ms", "15000")
		if r.code != 0 {
			differ(t, "pdo-receive: %v", r)
		}
		var prev uint64
		n := 0
		for _, e := range docs(t, r.out) {
			if e["type"] != "pdo" {
				continue
			}
			vals, _ := e["values"].(map[string]any)
			h, _ := vals["0x2002:00"].(string)
			b, err := hex.DecodeString(h)
			if err != nil || len(b) != 4 || e["cobId"] != fmt.Sprintf("0x%03X", 0x180+int(goNode)) {
				differ(t, "PDO event %v", e)
			}
			x := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24
			if n > 0 && x <= prev {
				differ(t, "counter not increasing: %s", r.out)
			}
			prev, n = x, n+1
		}
		if n != 5 {
			differ(t, "%d PDO events: %s", n, r.out)
		}
	})
	sc(t, "pdo-configure-tpdo2", func(t *testing.T) {
		if !c.op("pdo-configure") {
			skipf(t, "%s has no pdo-configure", a)
		}
		if err := g.n.SetState(canopen.StatePreOperational); err != nil {
			t.Fatal(err)
		}
		r := adapterRun(t, "", a, "pdo-configure", "--interface", ifc, "--node-id", node, "--tpdo", "2",
			"--map", "0x2000:00:8,0x2001:00:16", "--transmission-type", "255", "--event-timer-ms", "50")
		if r.code != 0 {
			differ(t, "pdo-configure: %v", r)
		}
		cfg, err := pdo.Load(g.d, true, 2)
		want := pdo.Mapping{{Index: 0x2000, Bits: 8}, {Index: 0x2001, Bits: 16}}
		if err != nil || cfg.COBID.ID() != 0x286 || !cfg.COBID.Valid() || cfg.TransmissionType != 255 || cfg.EventTimer != 50 || fmt.Sprint(cfg.Mapping) != fmt.Sprint(want) {
			differ(t, "TPDO2 configuration %+v, %v", cfg, err)
		}
		mark := sp.mark()
		if err := g.n.SetState(canopen.StateOperational); err != nil {
			t.Fatal(err)
		}
		fs := sp.waitFrames(mark, 0x286, 3, 5*time.Second)
		if len(fs) < 3 || !bytes.Equal(fs[0].Data[:fs[0].Len], []byte{0x11, 0x33, 0x22}) {
			differ(t, "%d TPDO2 frames: %v", len(fs), fs)
		}
	})
	sc(t, "sync-tpdo3", func(t *testing.T) {
		if !c.op("sync") {
			skipf(t, "%s has no sync", a)
		}
		// TPDO3: synchronous (every SYNC), 0x2008, configured over SDO.
		bus := openBus(t, ifc)
		cl, err := sdo.NewClient(bus, goNode)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		if err := g.n.SetState(canopen.StatePreOperational); err != nil {
			t.Fatal(err)
		}
		ctx := ctxT(t, time.Minute)
		if err := pdo.Configure(ctx, pdoSDO{cl}, true, 3, pdo.Config{
			COBID: canopen.NewCOBID(0x386), TransmissionType: 1,
			Mapping: pdo.Mapping{{Index: 0x2008, Bits: 32}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := g.n.SetState(canopen.StateOperational); err != nil {
			t.Fatal(err)
		}
		mark := sp.mark()
		r := adapterRun(t, "", a, "sync", "--interface", ifc, "--count", "20", "--period-ms", "20")
		if r.code != 0 || r.doc["sent"] != float64(20) {
			differ(t, "sync: %v", r)
		}
		syncs := sp.waitFrames(mark, 0x80, 20, 2*time.Second)
		tpdos := sp.waitFrames(mark, 0x386, 20, 2*time.Second)
		if len(syncs) != 20 || len(tpdos) != 20 || !bytes.Equal(tpdos[0].Data[:tpdos[0].Len], le(0x12345678, 4)) {
			differ(t, "%d SYNC, %d TPDO3 (want 20 each)", len(syncs), len(tpdos))
		}
		info(t).detail = fmt.Sprintf("%d SYNC, %d TPDO3", len(syncs), len(tpdos))
	})
}

func stackCiA402(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	if !c.op("cia402") {
		for _, n := range []string{"cia402-enable", "cia402-disable", "cia402-fault-reset"} {
			sc(t, n, func(t *testing.T) { skipf(t, "%s has no cia402 client", a) })
		}
		return
	}
	g := startGoDevice(t, ifc, f, driveNode, node.WithDrive(cia402.DriveConfig{}, 2*time.Millisecond))
	op := func(t *testing.T, o string) run {
		return adapterRun(t, "", a, "cia402", "--interface", ifc, "--node-id", fmt.Sprint(driveNode), "--op", o, "--timeout-ms", "20000")
	}
	check := func(t *testing.T, r run, theirs string, ours cia402.State) {
		got, _ := r.doc["state"].(string)
		if r.code != 0 || got != theirs {
			differ(t, "cia402: %v", r)
		}
		if !waitFor(2*time.Second, func() bool { return g.n.Drive().State() == ours }) {
			differ(t, "drive state %v, want %v", g.n.Drive().State(), ours)
		}
		var states []string
		for _, e := range docs(t, r.out) {
			if e["type"] == "cia402-state" {
				states = append(states, fmt.Sprint(e["state"]))
			}
		}
		info(t).detail = strings.Join(states, " -> ")
	}
	sc(t, "cia402-enable", func(t *testing.T) {
		check(t, op(t, "enable"), "OPERATION ENABLED", cia402.OperationEnabled)
	})
	sc(t, "cia402-disable", func(t *testing.T) {
		check(t, op(t, "disable"), "SWITCH ON DISABLED", cia402.SwitchOnDisabled)
	})
	sc(t, "cia402-fault-reset", func(t *testing.T) {
		if r := op(t, "enable"); r.code != 0 {
			differ(t, "enable: %v", r)
		}
		g.n.Drive().InjectFault(0x7300)
		if !waitFor(2*time.Second, func() bool { return g.n.Drive().State() == cia402.Fault }) {
			t.Fatalf("drive state %v after a fault", g.n.Drive().State())
		}
		// BaseNode402.reset_from_fault clears the fault and goes on to
		// OPERATION ENABLED.
		check(t, op(t, "fault-reset"), "OPERATION ENABLED", cia402.OperationEnabled)
	})
}
