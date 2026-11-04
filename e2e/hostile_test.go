// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/usdo"
)

// The hostile tests put a scripted, misbehaving peer (raw CAN frames) on
// the virtual bus next to a real go-canopen node and master. Each case
// asserts that the victim
//
//   - fails with a sensible error or abort, promptly (well within the
//     limit below, never at the end of the test's context),
//   - does not panic or deadlock,
//   - does not leak goroutines (nettest.CheckLeaks),
//   - keeps memory bounded where the peer announces or streams too much,
//   - and serves a well-formed request afterwards.

// Node-IDs of the hostile tests.
const (
	hVictim  canopen.NodeID = 5    // a real node
	hHostile canopen.NodeID = 9    // the scripted peer posing as a server
	hUSDOCli canopen.NodeID = 0x7F // the master's USDO client
	hEvilCli canopen.NodeID = 0x7E // the scripted peer posing as a USDO client
)

// hPrompt is how long a hostile exchange may take before the victim is
// considered stuck; hCtx bounds every call so that a hang fails the case
// instead of the test binary.
const (
	hPrompt = 1500 * time.Millisecond
	hCtx    = 4 * time.Second
	hMaxSDO = 4096 // the SDO and USDO clients' size limit
)

// hnet is one virtual network of a hostile case.
type hnet struct {
	vb *virtual.Bus
	fd bool
}

func newHNet(t *testing.T, fd bool) *hnet {
	t.Helper()
	nettest.CheckLeaks(t)
	vb := virtual.New(virtual.Config{Name: "hostile"})
	t.Cleanup(func() { _ = vb.Close() })
	return &hnet{vb: vb, fd: fd}
}

// port opens a bus on the network.
func (h *hnet) port(t *testing.T, opts ...virtual.PortOption) *canbus.Bus {
	t.Helper()
	if h.fd {
		opts = append(opts, virtual.WithFD())
	}
	p, err := h.vb.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := canbus.NewBus(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// raw injects frames without validation, as a broken transceiver or a
// hostile node would put them on the bus.
func (h *hnet) raw(t *testing.T, fs ...canbus.Frame) {
	t.Helper()
	for _, f := range fs {
		if err := h.vb.InjectRaw(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
}

// victimNode starts the real node hVictim on its own port.
func (h *hnet) victimNode(t *testing.T, opts ...node.Option) *node.Node {
	t.Helper()
	return h.victimNodeDict(t, nettest.Dict(t, hVictim, nil), opts...)
}

// victimNodeDict starts the real node hVictim with dictionary d.
func (h *hnet) victimNodeDict(t *testing.T, d *od.Dictionary, opts ...node.Option) *node.Node {
	t.Helper()
	if h.fd {
		opts = append([]node.Option{node.WithFD(false)}, opts...)
	}
	return startNode(t, h.port(t), d, opts...)
}

// hDomains is the number of extra DOMAIN objects (from hDomain) of the
// session flood's dictionary.
const (
	hDomain  uint16 = 0x2400
	hDomains uint16 = 40
)

// victimMaster starts a master with short SDO (and, on CAN FD, USDO)
// timeouts and a size limit of hMaxSDO.
func (h *hnet) victimMaster(t *testing.T, opts ...master.Option) *master.Master {
	t.Helper()
	base := []master.Option{
		master.WithSDOOptions(sdo.WithTimeout(60*time.Millisecond), sdo.WithMaxSize(hMaxSDO)),
		master.WithLSSOptions(lss.WithTimeout(30*time.Millisecond), lss.WithRetries(0),
			lss.WithFastscanTimeout(3*time.Millisecond), lss.WithFastscanRetries(0)),
	}
	if h.fd {
		base = append(base, master.WithFD(false),
			master.WithUSDOClient(hUSDOCli, usdo.WithTimeout(80*time.Millisecond), usdo.WithMaxSize(hMaxSDO)))
	}
	m, err := master.New(h.port(t), append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// stillServes checks that the master reads the real node's parameter
// (over SDO, or USDO on CAN FD), the well-formed request every case ends
// with.
func (h *hnet) stillServes(t *testing.T, m *master.Master) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hCtx)
	defer cancel()
	var got []byte
	var err error
	for range 20 { // the node may still be booting or recovering
		if h.fd {
			var c *usdo.Client
			if c, err = m.USDO(); err != nil {
				t.Fatal(err)
			}
			got, err = c.Upload(ctx, hVictim, nettest.IdxParam, 0)
		} else {
			var c *sdo.Client
			if c, err = m.SDO(hVictim); err != nil {
				t.Fatal(err)
			}
			got, err = c.Upload(ctx, nettest.IdxParam, 0)
		}
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("victim no longer serves a well-formed request: %v", err)
	}
	if len(got) != 4 || binary.LittleEndian.Uint32(got) != 0x12345678 {
		t.Fatalf("well-formed read returned % X", got)
	}
}

// prompt runs f with a bounded context and fails the test when it takes
// longer than hPrompt or runs into the context's deadline (a hang).
func prompt(t *testing.T, what string, f func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hCtx)
	defer cancel()
	start := time.Now()
	err := f(ctx)
	if el := time.Since(start); el > hPrompt {
		t.Errorf("%s: took %v (limit %v): %v", what, el, hPrompt, err)
	}
	if ctx.Err() != nil {
		t.Errorf("%s: ran into the context deadline (hang): %v", what, err)
	}
	return err
}

// hpeer is the scripted hostile peer: it answers the frames it subscribed
// to with whatever its script returns.
type hpeer struct {
	bus    *canbus.Bus
	sub    *canbus.Subscription
	script atomic.Pointer[func(f canbus.Frame) []canbus.Frame]
	served atomic.Int64 // frames sent in answer
	stop   chan struct{}
	done   chan struct{}
}

// newPeer starts a peer that receives the frames with the given IDs.
func (h *hnet) newPeer(t *testing.T, ids ...canbus.ID) *hpeer {
	t.Helper()
	bus := h.port(t)
	var fs canbus.FilterSet
	for _, id := range ids {
		fs.Filters = append(fs.Filters, canbus.ExactStandard(id))
	}
	sub, err := bus.Subscribe(canbus.WithFilters(fs), canbus.WithQueueSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	p := &hpeer{bus: bus, sub: sub, stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	t.Cleanup(func() { close(p.stop); <-p.done; _ = sub.Close() })
	return p
}

func (p *hpeer) set(f func(canbus.Frame) []canbus.Frame) { p.script.Store(&f) }

func (p *hpeer) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case <-p.sub.Done():
			return
		case f := <-p.sub.Frames():
			s := p.script.Load()
			if s == nil {
				continue
			}
			for _, out := range (*s)(f) {
				if err := p.bus.Send(context.Background(), &out); err != nil {
					return
				}
				p.served.Add(1)
			}
		}
	}
}

// std returns a classical frame.
func std(id canbus.ID, data ...byte) canbus.Frame {
	var f canbus.Frame
	f.ID = id
	f.SetPayload(data)
	return f
}

// sdoFrame returns an SDO frame with the multiplexer and a 32-bit value.
func sdoFrame(id canbus.ID, b0 byte, index uint16, sub uint8, v uint32) canbus.Frame {
	d := make([]byte, 8)
	d[0] = b0
	binary.LittleEndian.PutUint16(d[1:], index)
	d[3] = sub
	binary.LittleEndian.PutUint32(d[4:], v)
	return std(id, d...)
}

// sdoSeg returns an SDO segment (or block segment): the command byte and
// seven data bytes.
func sdoSeg(id canbus.ID, b0 byte, fill byte) canbus.Frame {
	return std(id, b0, fill, fill, fill, fill, fill, fill, fill)
}

// wantSDOAbort checks that err is an SDO abort with one of codes.
func wantSDOAbort(t *testing.T, err error, codes ...canopen.AbortCode) {
	t.Helper()
	var ae *canopen.AbortError
	if !errors.As(err, &ae) {
		t.Fatalf("want an SDO abort %v, got %v", codes, err)
	}
	for _, c := range codes {
		if ae.Code == c {
			return
		}
	}
	t.Fatalf("want abort %v, got %v", codes, err)
}

// goodServer answers the client's expedited read of IdxParam as a
// well-behaved server: after a hostile exchange the client must work
// with the same peer again.
func goodServer(tx canbus.ID) func(canbus.Frame) []canbus.Frame {
	return func(f canbus.Frame) []canbus.Frame {
		idx, sub := binary.LittleEndian.Uint16(f.Data[1:]), f.Data[3]
		switch f.Data[0] & 0xE0 {
		case 0x40: // initiate upload
			return []canbus.Frame{sdoFrame(tx, 0x43, idx, sub, 0x12345678)}
		case 0x80: // abort
			return nil
		default:
			return []canbus.Frame{sdoFrame(tx, 0x80, idx, sub, uint32(canopen.AbortCommand))}
		}
	}
}

// TestHostileSDOServer runs the master's SDO client against a scripted
// server that violates the protocol: oversized, overlong, endless and
// misdirected responses, toggle and block-transfer errors, abort storms.
func TestHostileSDOServer(t *testing.T) {
	tx := canopen.FuncSDOTx + canbus.ID(hHostile)
	rx := canopen.FuncSDORx + canbus.ID(hHostile)
	const idx = nettest.IdxDomain
	type tc struct {
		name   string
		block  bool
		upload bool
		script func(f canbus.Frame) []canbus.Frame
		codes  []canopen.AbortCode // nil: any error
		served int64               // > 0: most frames the peer may have had to send
	}
	toggle := func(f canbus.Frame) byte { return f.Data[0] & 0x10 }
	cases := []tc{
		{name: "upload size 0xFFFFFFFF", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0x40 {
				return []canbus.Frame{sdoFrame(tx, 0x41, idx, 0, 0xFFFFFFFF)}
			}
			return []canbus.Frame{sdoSeg(tx, toggle(f), 0xAA)}
		}, codes: []canopen.AbortCode{canopen.AbortOutOfMemory}, served: 1},
		{name: "upload size above client max", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0x40 {
				return []canbus.Frame{sdoFrame(tx, 0x41, idx, 0, hMaxSDO+1)}
			}
			return []canbus.Frame{sdoSeg(tx, toggle(f), 0xAA)}
		}, codes: []canopen.AbortCode{canopen.AbortOutOfMemory}, served: 1},
		{name: "segmented upload longer than indicated", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0x40 {
				return []canbus.Frame{sdoFrame(tx, 0x41, idx, 0, 10)}
			}
			return []canbus.Frame{sdoSeg(tx, toggle(f), 0xAA)} // 7 bytes, never the last
		}, codes: []canopen.AbortCode{canopen.AbortLengthHigh}},
		{name: "unsized endless segmented upload", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0x40 {
				return []canbus.Frame{sdoFrame(tx, 0x40, idx, 0, 0)} // size not indicated
			}
			return []canbus.Frame{sdoSeg(tx, toggle(f), 0xAA)}
		}, codes: []canopen.AbortCode{canopen.AbortOutOfMemory}, served: hMaxSDO/7 + 3},
		{name: "toggle bit not alternated", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0x40 {
				return []canbus.Frame{sdoFrame(tx, 0x41, idx, 0, 30)}
			}
			return []canbus.Frame{sdoSeg(tx, 0x00, 0xAA)} // always toggle 0
		}, codes: []canopen.AbortCode{canopen.AbortToggle}},
		{name: "response for another object", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			return []canbus.Frame{sdoFrame(tx, 0x43, 0x1000, 0, 0xDEADBEEF)}
		}},
		{name: "response for another sub-index", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			return []canbus.Frame{sdoFrame(tx, 0x43, idx, 7, 0xDEADBEEF)}
		}},
		{name: "abort storm", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			out := make([]canbus.Frame, 64)
			for i := range out {
				out[i] = sdoFrame(tx, 0x80, idx, 0, uint32(canopen.AbortGeneral))
			}
			return out
		}, codes: []canopen.AbortCode{canopen.AbortGeneral}},
		{name: "unknown server command", upload: true, script: func(f canbus.Frame) []canbus.Frame {
			return []canbus.Frame{sdoFrame(tx, 0xE0, idx, 0, 0)}
		}, codes: []canopen.AbortCode{canopen.AbortCommand}},
		{name: "block download blksize 0", block: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0]&0xE0 == 0xC0 {
				return []canbus.Frame{sdoFrame(tx, 0xA4, idx, 0, 0)}
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortBlockSize}},
		{name: "block download blksize 128", block: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0]&0xE0 == 0xC0 {
				return []canbus.Frame{sdoFrame(tx, 0xA4, idx, 0, 128)}
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortBlockSize}},
		{name: "block download blksize 255", block: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0]&0xE0 == 0xC0 {
				return []canbus.Frame{sdoFrame(tx, 0xA4, idx, 0, 255)}
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortBlockSize}},
		{name: "block upload seqno never in order", block: true, upload: true, script: func(f canbus.Frame) []canbus.Frame {
			switch f.Data[0] {
			case 0xA4: // initiate: 100 bytes
				return []canbus.Frame{sdoFrame(tx, 0xC6, idx, 0, 100)}
			case 0xA3, 0xA2: // start, or an acknowledgement: always sequence 2 first
				return []canbus.Frame{sdoSeg(tx, 2, 0xAA), sdoSeg(tx, 1, 0xAA)}
			}
			return nil
		}},
		{name: "block upload wrong CRC", block: true, upload: true, script: func(f canbus.Frame) []canbus.Frame {
			switch f.Data[0] {
			case 0xA4:
				return []canbus.Frame{sdoFrame(tx, 0xC6, idx, 0, 7)}
			case 0xA3:
				return []canbus.Frame{sdoSeg(tx, 0x81, 0xAA)}
			case 0xA2:
				return []canbus.Frame{std(tx, 0xC1, 0x34, 0x12, 0, 0, 0, 0, 0)}
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortCRC}},
		{name: "block upload endless sub-blocks", block: true, upload: true, script: func(f canbus.Frame) []canbus.Frame {
			switch f.Data[0] {
			case 0xA4: // size not indicated
				return []canbus.Frame{sdoFrame(tx, 0xC4, idx, 0, 0)}
			case 0xA3, 0xA2: // a whole sub-block, never the last segment
				blk := int(f.Data[2])
				if f.Data[0] == 0xA3 || blk == 0 {
					blk = 127
				}
				out := make([]canbus.Frame, blk)
				for i := range out {
					out[i] = sdoSeg(tx, byte(i+1), 0xAA)
				}
				return out
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortOutOfMemory}, served: 2*hMaxSDO/7 + 256},
		{name: "block upload size 0xFFFFFFFF", block: true, upload: true, script: func(f canbus.Frame) []canbus.Frame {
			if f.Data[0] == 0xA4 {
				return []canbus.Frame{sdoFrame(tx, 0xC6, idx, 0, 0xFFFFFFFF)}
			}
			return nil
		}, codes: []canopen.AbortCode{canopen.AbortOutOfMemory}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHNet(t, false)
			h.victimNode(t)
			p := h.newPeer(t, rx)
			m := h.victimMaster(t)
			p.set(func(f canbus.Frame) []canbus.Frame {
				if f.Data[0] == 0x80 { // the client's abort ends the exchange
					return nil
				}
				return c.script(f)
			})
			cl, err := m.SDO(hHostile)
			if err != nil {
				t.Fatal(err)
			}
			mode := sdo.UseBlockMode(sdo.BlockNever)
			if c.block {
				mode = sdo.UseBlockMode(sdo.BlockAlways)
			}
			var data []byte
			err = prompt(t, c.name, func(ctx context.Context) error {
				if c.upload {
					var e error
					data, e = cl.Upload(ctx, idx, 0, mode)
					return e
				}
				return cl.Download(ctx, idx, 0, make([]byte, 100), mode)
			})
			if err == nil {
				t.Fatalf("hostile exchange succeeded (% X)", data)
			}
			if c.codes != nil {
				wantSDOAbort(t, err, c.codes...)
			}
			var ae *canopen.AbortError
			if !errors.As(err, &ae) && !errors.Is(err, canopen.ErrTimeout) {
				t.Errorf("unclean error %v", err)
			}
			if c.served > 0 {
				if n := p.served.Load(); n > c.served {
					t.Errorf("the client kept the peer streaming: %d frames (bound %d)", n, c.served)
				}
			}
			// Let the hostile frames still in flight arrive, then the
			// same client must work with the same (now sane) peer.
			p.set(goodServer(tx))
			time.Sleep(20 * time.Millisecond)
			err = prompt(t, "well-formed read after "+c.name, func(ctx context.Context) error {
				v, err := cl.ReadUint(ctx, nettest.IdxParam, 0)
				if err == nil && v != 0x12345678 {
					t.Errorf("read %x after the hostile exchange", v)
				}
				return err
			})
			if err != nil {
				t.Errorf("client unusable after %s: %v", c.name, err)
			}
			h.stillServes(t, m)
		})
	}
}

// TestHostileSDOUnsolicited floods the master's SDO client with server
// responses while it is idle, then transfers.
func TestHostileSDOUnsolicited(t *testing.T) {
	h := newHNet(t, false)
	h.victimNode(t)
	m := h.victimMaster(t)
	tx := canopen.FuncSDOTx + canbus.ID(hVictim)
	if _, err := m.SDO(hVictim); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		h.raw(t, sdoFrame(tx, byte(i), nettest.IdxParam, 0, 0xDEADBEEF))
	}
	time.Sleep(20 * time.Millisecond)
	h.stillServes(t, m)
}

// rawClient is a scripted SDO client talking to the real node's server.
type rawClient struct {
	bus *canbus.Bus
	sub *canbus.Subscription
	rx  canbus.ID
}

func (h *hnet) newRawClient(t *testing.T, node canopen.NodeID) *rawClient {
	t.Helper()
	bus := h.port(t)
	sub, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{
		canbus.ExactStandard(canopen.FuncSDOTx + canbus.ID(node))}}), canbus.WithQueueSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return &rawClient{bus: bus, sub: sub, rx: canopen.FuncSDORx + canbus.ID(node)}
}

func (c *rawClient) send(t *testing.T, fs ...canbus.Frame) {
	t.Helper()
	for i := range fs {
		f := fs[i]
		f.ID = c.rx
		if err := c.bus.Send(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
	}
}

// recv returns the next response, or ok false after wait.
func (c *rawClient) recv(wait time.Duration) (canbus.Frame, bool) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case f := <-c.sub.Frames():
		return f, true
	case <-t.C:
		return canbus.Frame{}, false
	}
}

// rr sends a request and waits for its response.
func (c *rawClient) rr(t *testing.T, f canbus.Frame) canbus.Frame {
	t.Helper()
	c.send(t, f)
	r, ok := c.recv(time.Second)
	if !ok {
		t.Fatalf("no response to % X", f.Payload())
	}
	return r
}

// drain discards pending responses.
func (c *rawClient) drain(wait time.Duration) int {
	n := 0
	for {
		if _, ok := c.recv(wait); !ok {
			return n
		}
		n++
	}
}

// wantAbortFrame checks that r is an SDO abort with code.
func wantAbortFrame(t *testing.T, r canbus.Frame, code canopen.AbortCode) {
	t.Helper()
	if r.Data[0] != 0x80 || canopen.AbortCode(binary.LittleEndian.Uint32(r.Data[4:])) != code {
		t.Fatalf("want abort %v, got % X", code, r.Payload())
	}
}

// totalAlloc returns the bytes allocated so far.
func totalAlloc() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.TotalAlloc
}

// TestHostileSDOClient runs a scripted SDO client against the real
// node's SDO server: announced sizes up to 4 GiB must be refused without
// allocating them, endless downloads end at the server's limit, broken
// segments and abort storms leave the server serving.
func TestHostileSDOClient(t *testing.T) {
	const idx = nettest.IdxDomain
	const limit = 4096 // the node's SDO server size limit
	setup := func(t *testing.T) (*hnet, *master.Master, *rawClient) {
		h := newHNet(t, false)
		h.victimNode(t, node.WithSDOServerOptions(sdo.WithMaxSize(limit), sdo.WithTimeout(200*time.Millisecond)))
		m := h.victimMaster(t)
		h.stillServes(t, m) // the node's server is open
		return h, m, h.newRawClient(t, hVictim)
	}
	// allocBound is the most the server may allocate for one hostile
	// transfer (the whole process is measured: generous, but far below
	// an announced 4 GiB or 1 MiB preallocation).
	const allocBound = 8 << 20

	for _, c := range []struct {
		name string
		init canbus.Frame
	}{
		{"segmented download of 4 GiB", sdoFrame(0, 0x21, idx, 0, 0xFFFFFFFF)},
		{"segmented download of limit+1", sdoFrame(0, 0x21, idx, 0, limit+1)},
		{"block download of 4 GiB", sdoFrame(0, 0xC6, idx, 0, 0xFFFFFFFF)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, m, rc := setup(t)
			before := totalAlloc()
			r := rc.rr(t, c.init)
			if grew := totalAlloc() - before; grew > allocBound {
				t.Errorf("the server allocated %d bytes for an announced download", grew)
			}
			wantAbortFrame(t, r, canopen.AbortOutOfMemory)
			stillServesNode(t, m)
		})
	}

	t.Run("unsized endless segmented download", func(t *testing.T) {
		_, m, rc := setup(t)
		if r := rc.rr(t, sdoFrame(0, 0x20, idx, 0, 0)); r.Data[0] != 0x60 {
			t.Fatalf("initiate: % X", r.Payload())
		}
		before := totalAlloc()
		var last canbus.Frame
		n := 0
		for ; n < 4*limit/7; n++ {
			last = rc.rr(t, sdoSeg(0, byte(n&1)<<4, 0x55))
			if last.Data[0] == 0x80 {
				break
			}
		}
		wantAbortFrame(t, last, canopen.AbortOutOfMemory)
		if n > limit/7+1 {
			t.Errorf("the server took %d segments (%d bytes) beyond its limit %d", n, 7*n, limit)
		}
		if grew := totalAlloc() - before; grew > allocBound {
			t.Errorf("allocated %d bytes", grew)
		}
		stillServesNode(t, m)
	})

	t.Run("unsized endless block download", func(t *testing.T) {
		_, m, rc := setup(t)
		r := rc.rr(t, sdoFrame(0, 0xC4, idx, 0, 0))
		if r.Data[0]&0xE3 != 0xA0 || r.Data[4] == 0 || r.Data[4] > 127 {
			t.Fatalf("block initiate: % X", r.Payload())
		}
		blk := int(r.Data[4])
		var aborted bool
		for round := 0; round < 4*limit/(7*blk)+4 && !aborted; round++ {
			for s := 1; s <= blk; s++ {
				rc.send(t, sdoSeg(0, byte(s), 0x55))
			}
			resp, ok := rc.recv(time.Second)
			if !ok {
				t.Fatal("no block acknowledgement")
			}
			switch resp.Data[0] {
			case 0x80:
				wantAbortFrame(t, resp, canopen.AbortOutOfMemory)
				aborted = true
			case 0xA2:
				if resp.Data[2] == 0 || resp.Data[2] > 127 {
					t.Fatalf("ack with blksize %d", resp.Data[2])
				}
				blk = int(resp.Data[2])
			default:
				t.Fatalf("unexpected % X", resp.Payload())
			}
		}
		if !aborted {
			t.Fatal("the server accepted an endless block download beyond its limit")
		}
		rc.drain(20 * time.Millisecond)
		stillServesNode(t, m)
	})

	t.Run("block download sequence garbage", func(t *testing.T) {
		_, m, rc := setup(t)
		r := rc.rr(t, sdoFrame(0, 0xC6, idx, 0, 100))
		if r.Data[0]&0xE3 != 0xA0 {
			t.Fatalf("block initiate: % X", r.Payload())
		}
		// Sequence numbers 0, 128..255 with and without the last flag,
		// then a sub-block that never starts at 1: the server must
		// re-acknowledge or abort, never accept.
		for _, s := range []byte{0, 0x80, 0x7F, 0xFF, 5, 3} {
			rc.send(t, sdoSeg(0, s, 0x55))
		}
		rc.drain(50 * time.Millisecond)
		stillServesNode(t, m)
	})

	t.Run("segments and block frames without a transfer", func(t *testing.T) {
		_, m, rc := setup(t)
		for _, b0 := range []byte{0x00, 0x10, 0x01, 0x60, 0x70, 0xA1, 0xA2, 0xA3, 0xC1, 0xE0, 0xFF} {
			rc.send(t, sdoSeg(0, b0, 0x55))
		}
		rc.drain(30 * time.Millisecond)
		stillServesNode(t, m)
	})

	t.Run("toggle errors", func(t *testing.T) {
		_, m, rc := setup(t)
		if r := rc.rr(t, sdoFrame(0, 0x21, idx, 0, 30)); r.Data[0] != 0x60 {
			t.Fatalf("initiate: % X", r.Payload())
		}
		if r := rc.rr(t, sdoSeg(0, 0x00, 1)); r.Data[0] != 0x20 {
			t.Fatalf("segment 1: % X", r.Payload())
		}
		wantAbortFrame(t, rc.rr(t, sdoSeg(0, 0x00, 2)), canopen.AbortToggle)
		stillServesNode(t, m)
	})

	t.Run("abort storm", func(t *testing.T) {
		_, m, rc := setup(t)
		if r := rc.rr(t, sdoFrame(0, 0x21, idx, 0, 30)); r.Data[0] != 0x60 {
			t.Fatalf("initiate: % X", r.Payload())
		}
		for i := range 500 {
			rc.send(t, sdoFrame(0, 0x80, uint16(i), byte(i), uint32(canopen.AbortGeneral)))
		}
		if n := rc.drain(30 * time.Millisecond); n != 0 {
			t.Errorf("the server answered %d aborts", n)
		}
		stillServesNode(t, m)
	})

	t.Run("short and long frames", func(t *testing.T) {
		h, m, rc := setup(t)
		for n := range 8 { // SDO requests are 8 bytes
			d := make([]byte, n)
			if n > 0 {
				d[0] = 0x40
			}
			h.raw(t, std(canopen.FuncSDORx+canbus.ID(hVictim), d...))
		}
		for _, n := range []uint8{9, 15, 64, 255} {
			f := sdoFrame(canopen.FuncSDORx+canbus.ID(hVictim), 0x40, nettest.IdxParam, 0, 0)
			f.Len = n
			h.raw(t, f)
		}
		rc.drain(30 * time.Millisecond)
		stillServesNode(t, m)
	})
}

// stillServesNode is stillServes on a classical network.
func stillServesNode(t *testing.T, m *master.Master) {
	t.Helper()
	(&hnet{}).stillServes(t, m)
}

// usdoFrame returns a USDO frame sent by sender.
func usdoFrame(t *testing.T, m usdo.Message, sender canopen.NodeID, response bool) canbus.Frame {
	t.Helper()
	f, err := usdo.Frame(m, sender, response, false)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// usdoSegment returns a full upload segment.
func usdoSegment(t *testing.T, client canopen.NodeID, session, counter uint8) canbus.Frame {
	t.Helper()
	data := make([]byte, usdo.SegmentSize)
	for i := range data {
		data[i] = 0xAA
	}
	return usdoFrame(t, usdo.Message{Dest: usdo.Node(client), Cmd: usdo.CmdUploadSegmentData, Session: session,
		Counter: counter, Data: data}, hHostile, true)
}

// TestHostileUSDOServer runs the master's USDO client (CANopen FD)
// against a scripted server.
func TestHostileUSDOServer(t *testing.T) {
	const idx = nettest.IdxDomain
	type tc struct {
		name   string
		script func(t *testing.T, req usdo.Message) []canbus.Frame
		codes  []usdo.AbortCode // nil: any error
		served int64
	}
	resp := func(t *testing.T, m usdo.Message) canbus.Frame {
		m.Dest = usdo.Node(hUSDOCli)
		return usdoFrame(t, m, hHostile, true)
	}
	initSize := func(size uint32) func(t *testing.T, req usdo.Message) []canbus.Frame {
		return func(t *testing.T, req usdo.Message) []canbus.Frame {
			switch req.Cmd {
			case usdo.CmdUpload:
				return []canbus.Frame{resp(t, usdo.Message{Cmd: usdo.CmdUploadInit, Session: req.Session,
					Index: req.Index, SubIndex: req.SubIndex, Size: size})}
			case usdo.CmdUploadSegment:
				return []canbus.Frame{usdoSegment(t, hUSDOCli, req.Session, req.Counter)}
			default:
				return nil
			}
		}
	}
	cases := []tc{
		{name: "upload size 0xFFFFFFFF", script: initSize(0xFFFFFFFF),
			codes: []usdo.AbortCode{usdo.AbortOutOfMemory}, served: 1},
		{name: "upload size above client max", script: initSize(hMaxSDO + 1),
			codes: []usdo.AbortCode{usdo.AbortOutOfMemory}, served: 1},
		{name: "upload longer than indicated", script: initSize(100), codes: []usdo.AbortCode{usdo.AbortSize}},
		{name: "upload segment counter wrong", script: func(t *testing.T, req usdo.Message) []canbus.Frame {
			if req.Cmd == usdo.CmdUpload {
				return initSize(1000)(t, req)
			}
			return []canbus.Frame{usdoSegment(t, hUSDOCli, req.Session, req.Counter+5)}
		}, codes: []usdo.AbortCode{usdo.AbortCounter}},
		{name: "response for another object", script: func(t *testing.T, req usdo.Message) []canbus.Frame {
			return []canbus.Frame{resp(t, usdo.Message{Cmd: usdo.CmdUploadExpedited, Session: req.Session,
				Index: 0x1000, SubIndex: 0, Size: 4, Data: []byte{1, 2, 3, 4}})}
		}},
		{name: "response for another session", script: func(t *testing.T, req usdo.Message) []canbus.Frame {
			return []canbus.Frame{resp(t, usdo.Message{Cmd: usdo.CmdUploadExpedited, Session: req.Session + 1,
				Index: req.Index, SubIndex: req.SubIndex, Size: 4, Data: []byte{1, 2, 3, 4}})}
		}},
		{name: "abort storm", script: func(t *testing.T, req usdo.Message) []canbus.Frame {
			out := make([]canbus.Frame, 64)
			for i := range out {
				out[i] = resp(t, usdo.Message{Cmd: usdo.CmdAbort, Session: req.Session, Index: req.Index,
					SubIndex: req.SubIndex, Abort: usdo.AbortHardware})
			}
			return out
		}, codes: []usdo.AbortCode{usdo.AbortHardware}},
		{name: "unknown command", script: func(t *testing.T, req usdo.Message) []canbus.Frame {
			f := resp(t, usdo.Message{Cmd: usdo.CmdDownloadLastOK, Session: req.Session})
			f.Data[1] = 0x55
			return []canbus.Frame{f}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHNet(t, true)
			h.victimNode(t)
			p := h.newPeer(t, canopen.FuncSDORx+canbus.ID(hUSDOCli))
			m := h.victimMaster(t)
			var stopped atomic.Bool
			p.set(func(f canbus.Frame) []canbus.Frame {
				req, err := usdo.Decode(f.Payload())
				if err != nil || req.Dest != usdo.Node(hHostile) || stopped.Load() {
					return nil
				}
				return c.script(t, req)
			})
			cl, err := m.USDO()
			if err != nil {
				t.Fatal(err)
			}
			var data []byte
			err = prompt(t, c.name, func(ctx context.Context) error {
				var e error
				data, e = cl.Upload(ctx, hHostile, idx, 0)
				return e
			})
			stopped.Store(true)
			if err == nil {
				t.Fatalf("hostile exchange succeeded (%d bytes)", len(data))
			}
			var ae *usdo.AbortError
			if !errors.As(err, &ae) && !errors.Is(err, canopen.ErrTimeout) {
				t.Errorf("unclean error %v", err)
			}
			if c.codes != nil {
				ok := false
				for _, code := range c.codes {
					ok = ok || errors.Is(err, code)
				}
				if !ok {
					t.Errorf("want abort %v, got %v", c.codes, err)
				}
			}
			if c.served > 0 {
				if n := p.served.Load(); n > c.served {
					t.Errorf("the client kept the peer streaming: %d frames (%d bytes; bound %d frames)",
						n, n*usdo.SegmentSize, c.served)
				}
			}
			time.Sleep(20 * time.Millisecond)
			h.stillServes(t, m)
		})
	}
}

// TestHostileUSDOClient runs a scripted USDO client against the real
// node's USDO server: an announced 4 GiB download, a session flood (each
// session announcing the server's limit), endless segments and garbage.
func TestHostileUSDOClient(t *testing.T) {
	const idx = nettest.IdxDomain
	const limit = 1 << 20 // the USDO server's default size limit
	setup := func(t *testing.T) (*hnet, *master.Master, *rawClient) {
		h := newHNet(t, true)
		d := nettest.Dict(t, hVictim, nil)
		for i := range hDomains {
			if err := d.Add(od.NewVar(hDomain+i, "domain", canopen.Domain, od.AccessRW, nil)); err != nil {
				t.Fatal(err)
			}
		}
		h.victimNodeDict(t, d, node.WithUSDOServerOptions(usdo.WithTimeout(150*time.Millisecond)))
		m := h.victimMaster(t)
		h.stillServes(t, m)
		// The node's USDO server answers on 580h + its node-ID; the
		// scripted client sends on 600h + its own.
		rc := h.newRawClient(t, hVictim)
		rc.rx = canopen.FuncSDORx + canbus.ID(hEvilCli)
		return h, m, rc
	}
	req := func(t *testing.T, m usdo.Message) canbus.Frame {
		m.Dest = usdo.Node(hVictim)
		return usdoFrame(t, m, hEvilCli, false)
	}
	decode := func(t *testing.T, f canbus.Frame) usdo.Message {
		t.Helper()
		m, err := usdo.Decode(f.Payload())
		if err != nil {
			t.Fatalf("server sent an undecodable response % X: %v", f.Payload(), err)
		}
		return m
	}

	t.Run("download of 4 GiB", func(t *testing.T) {
		_, m, rc := setup(t)
		before := totalAlloc()
		r := decode(t, rc.rr(t, req(t, usdo.Message{Cmd: usdo.CmdDownloadInit, Session: 1, Index: idx, Size: 0xFFFFFFFF})))
		if grew := totalAlloc() - before; grew > 8<<20 {
			t.Errorf("the server allocated %d bytes for an announced download", grew)
		}
		if r.Cmd != usdo.CmdAbort || r.Abort != usdo.AbortOutOfMemory {
			t.Fatalf("want an out-of-memory abort, got %v", r)
		}
		stillServesFD(t, m)
	})

	t.Run("session flood at the size limit", func(t *testing.T) {
		_, m, rc := setup(t)
		before := totalAlloc()
		var accepted, busy int
		for s := range 256 {
			rc.send(t, req(t, usdo.Message{Cmd: usdo.CmdBulkInit, Session: uint8(s), Index: hDomain + uint16(s)%hDomains, Size: limit}))
		}
		for {
			f, ok := rc.recv(100 * time.Millisecond)
			if !ok {
				break
			}
			switch r := decode(t, f); {
			case r.Cmd == usdo.CmdBulkInitOK:
				accepted++
			case r.Cmd == usdo.CmdAbort && (r.Abort == usdo.AbortBusy || r.Abort == usdo.AbortParallel):
				busy++
			default:
				t.Errorf("unexpected response %v", r)
			}
		}
		grew := totalAlloc() - before
		t.Logf("%d sessions accepted, %d refused busy, %d bytes allocated", accepted, busy, grew)
		if accepted > 16 {
			t.Errorf("the server accepted %d sessions (limit 16)", accepted)
		}
		if grew > 16<<20 {
			t.Errorf("the server allocated %d bytes for %d sessions", grew, accepted)
		}
		// The sessions time out (150 ms); then the master is served.
		stillServesFD(t, m)
	})

	t.Run("endless bulk segments", func(t *testing.T) {
		_, m, rc := setup(t)
		r := decode(t, rc.rr(t, req(t, usdo.Message{Cmd: usdo.CmdBulkInit, Session: 3, Index: idx, Size: 1000})))
		if r.Cmd != usdo.CmdBulkInitOK {
			t.Fatalf("bulk init: %v", r)
		}
		data := make([]byte, usdo.SegmentSize)
		var abort usdo.Message
		for n := 1; n < 200 && abort.Cmd == 0; n++ {
			rc.send(t, req(t, usdo.Message{Cmd: usdo.CmdBulkSegment, Session: 3, Counter: uint8(n), Data: data}))
			if f, ok := rc.recv(5 * time.Millisecond); ok {
				abort = decode(t, f)
			}
		}
		if abort.Cmd != usdo.CmdAbort || abort.Abort != usdo.AbortSize {
			t.Fatalf("want a size abort for segments beyond the announced size, got %v", abort)
		}
		stillServesFD(t, m)
	})

	t.Run("garbage", func(t *testing.T) {
		h, m, rc := setup(t)
		for cmd := range 256 {
			f := req(t, usdo.Message{Cmd: usdo.CmdDownloadLastOK, Session: 7})
			f.Data[1] = byte(cmd)
			rc.send(t, f)
		}
		// Truncated messages, classical frames and a remote frame on the
		// USDO identifier.
		for n := range 12 {
			f, err := canbus.NewFD(canopen.FuncSDORx+canbus.ID(hEvilCli), false, false, []byte{byte(hVictim), 0x02, 1, 0, 0, 0x22, 0, 0, 0, 0, 0, 0}[:n])
			if err == nil {
				rc.send(t, f)
			}
		}
		h.raw(t, std(canopen.FuncSDORx+canbus.ID(hEvilCli), byte(hVictim), 0x11, 1, 0, 0, 0x23, 0, 0))
		rtr, _ := canbus.NewRemote(canopen.FuncSDORx+canbus.ID(hEvilCli), false, 8)
		h.raw(t, rtr)
		rc.drain(30 * time.Millisecond)
		stillServesFD(t, m)
	})
}

// stillServesFD is stillServes on a CAN FD network.
func stillServesFD(t *testing.T, m *master.Master) {
	t.Helper()
	(&hnet{fd: true}).stillServes(t, m)
}

// lssAnswer returns an LSS slave answer (7E4h) with a 32-bit value.
func lssAnswer(cs lss.Command, v uint32) canbus.Frame {
	d := make([]byte, 8)
	d[0] = byte(cs)
	binary.LittleEndian.PutUint32(d[1:], v)
	return std(lss.SlaveID, d...)
}

// TestHostileLSS runs the master's LSS service against unsolicited,
// duplicate and always-acknowledging slave answers.
func TestHostileLSS(t *testing.T) {
	t.Run("answers without a request", func(t *testing.T) {
		h := newHNet(t, false)
		m := h.victimMaster(t)
		for i := range 200 {
			h.raw(t, lssAnswer(lss.CmdInquireVendor, 0xBAD00000+uint32(i)), lssAnswer(lss.CmdIdentifySlave, 0))
		}
		time.Sleep(20 * time.Millisecond)
		// No slave: every inquiry must time out, not return a stale value.
		for range 3 {
			err := prompt(t, "inquire", func(ctx context.Context) error {
				v, err := m.LSS().Inquire(ctx, lss.PartVendor)
				if err == nil {
					t.Errorf("inquiry without a slave returned 0x%08X", v)
				}
				return err
			})
			if !errors.Is(err, canopen.ErrTimeout) {
				t.Errorf("want a timeout, got %v", err)
			}
		}
	})

	t.Run("duplicate answers", func(t *testing.T) {
		h := newHNet(t, false)
		p := h.newPeer(t, lss.MasterID)
		m := h.victimMaster(t)
		var n atomic.Uint32
		p.set(func(f canbus.Frame) []canbus.Frame {
			cs := lss.Command(f.Data[0])
			if cs < lss.CmdInquireVendor || cs > lss.CmdInquireSerial {
				return nil
			}
			v := n.Add(1)
			return []canbus.Frame{lssAnswer(cs, v), lssAnswer(cs, v), lssAnswer(cs, 0xDEAD0000+v)}
		})
		for i := range uint32(4) {
			var v uint32
			err := prompt(t, "inquire", func(ctx context.Context) error {
				var err error
				v, err = m.LSS().Inquire(ctx, lss.PartVendor+lss.Part(i))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if v != i+1 {
				t.Errorf("inquiry %d returned 0x%X, want %d (a stale duplicate)", i, v, i+1)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	t.Run("fastscan always acknowledged", func(t *testing.T) {
		// A peer acknowledging every Fastscan query looks like 2^128
		// devices. A Fastscan limited to Max devices must stop.
		h := newHNet(t, false)
		p := h.newPeer(t, lss.MasterID)
		m := h.victimMaster(t)
		p.set(func(f canbus.Frame) []canbus.Frame {
			if lss.Command(f.Data[0]) == lss.CmdFastscan {
				return []canbus.Frame{lssAnswer(lss.CmdIdentifySlave, 0)}
			}
			return nil
		})
		var got []lss.Address
		err := prompt(t, "fastscan", func(ctx context.Context) error {
			var err error
			got, err = m.LSS().Fastscan(ctx, lss.FastscanOptions{Timeout: 2 * time.Millisecond, Max: 2})
			return err
		})
		t.Logf("fastscan: %d addresses, %v", len(got), err)
		if len(got) > 2 {
			t.Errorf("fastscan returned %d addresses with Max 2", len(got))
		}
	})

	t.Run("fastscan select always acknowledged", func(t *testing.T) {
		h := newHNet(t, false)
		p := h.newPeer(t, lss.MasterID)
		m := h.victimMaster(t)
		p.set(func(f canbus.Frame) []canbus.Frame {
			if lss.Command(f.Data[0]) == lss.CmdFastscan {
				return []canbus.Frame{lssAnswer(lss.CmdIdentifySlave, 0), lssAnswer(lss.CmdIdentifySlave, 0)}
			}
			return nil
		})
		_ = prompt(t, "fastscan select", func(ctx context.Context) error {
			_, _, err := m.LSS().FastscanSelect(ctx, lss.FastscanOptions{Timeout: 2 * time.Millisecond})
			return err
		})
	})
}

// TestHostileFloods floods the master and a node with heartbeats and
// emergencies while the master's event handler is blocked: memory must
// stay bounded and both must serve afterwards.
func TestHostileFloods(t *testing.T) {
	const frames = 50000
	// retained is the heap the master may keep for a flood (the frames
	// themselves are 50000 x ~100 bytes, so a queue of every event is
	// well above it).
	const retained = 4 << 20
	// maxQueued is the most events the master may hold for a handler
	// that does not return.
	const maxQueued = 8192
	flood := func(t *testing.T, mk func(i int) canbus.Frame) {
		h := newHNet(t, false)
		h.victimNode(t)
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		var handled atomic.Int64
		m := h.victimMaster(t, master.WithEventHandler(func(master.Event) {
			<-release
			handled.Add(1)
		}))
		t.Cleanup(unblock) // registered after the master's: runs before its Close
		for id := canopen.NodeID(1); id <= canopen.MaxNodeID; id++ {
			if id != hVictim {
				_ = m.Monitor(id, time.Hour)
			}
		}
		runtime.GC()
		var ms0, ms1 runtime.MemStats
		runtime.ReadMemStats(&ms0)
		ctx := context.Background()
		for i := range frames {
			if err := h.vb.Inject(ctx, mk(i)); err != nil {
				t.Fatal(err)
			}
			if i%512 == 511 {
				time.Sleep(time.Millisecond) // let the receivers keep up
			}
		}
		time.Sleep(50 * time.Millisecond)
		runtime.GC()
		runtime.ReadMemStats(&ms1)
		grew := int64(ms1.HeapAlloc) - int64(ms0.HeapAlloc)
		t.Logf("heap grew %d bytes over %d frames while the handler was blocked", grew, frames)
		if grew > retained {
			t.Errorf("master retained %d bytes for a flood of %d frames with a blocked event handler (bound %d)", grew, frames, retained)
		}
		if n := len(m.Nodes()); n > int(canopen.MaxNodeID) {
			t.Errorf("master tracks %d nodes", n)
		}
		queued := handled.Load()
		unblock()
		// Every event the master queued while the handler was blocked is
		// delivered now: their number is what it retained.
		time.Sleep(50 * time.Millisecond)
		queued = handled.Load() - queued
		t.Logf("%d events queued for the blocked handler", queued)
		if queued > maxQueued {
			t.Errorf("master queued %d events for a blocked handler (bound %d): the event queue is unbounded", queued, maxQueued)
		}
		h.stillServes(t, m)
	}
	t.Run("emergency", func(t *testing.T) {
		flood(t, func(i int) canbus.Frame {
			id := canbus.ID(i%127 + 1)
			return std(canopen.FuncEMCY+id, 0x00, 0x10, 0x01, byte(i), byte(i>>8), 0, 0, 0)
		})
	})
	t.Run("heartbeat", func(t *testing.T) {
		states := []byte{0x00, 0x04, 0x05, 0x7F, 0x55}
		flood(t, func(i int) canbus.Frame {
			id := canbus.ID(i%127 + 1)
			if id == canbus.ID(hVictim) {
				id = 1
			}
			return std(canopen.FuncHeartbeat+id, states[i%len(states)])
		})
	})
}

// TestHostileNMT sends NMT commands for every node-ID, broadcast, out of
// range and malformed: the node takes only the ones addressed to it
// (or to all), the master refuses to send invalid ones.
func TestHostileNMT(t *testing.T) {
	h := newHNet(t, false)
	n := h.victimNode(t)
	m := h.victimMaster(t)
	h.stillServes(t, m)
	waitState := func(st canopen.NMTState) {
		t.Helper()
		nettest.Eventually(t, 2*time.Second, "state "+st.String(), func() bool { return n.State() == st })
	}
	nmtF := func(cmd, id byte) canbus.Frame { return std(canopen.FuncNMT, cmd, id) }

	// Out of range node-IDs and unknown commands: no effect.
	for id := 128; id < 256; id++ {
		h.raw(t, nmtF(byte(canopen.NMTStop), byte(id)))
	}
	for _, cmd := range []byte{0x00, 0x03, 0x7F, 0x83, 0xFF} {
		h.raw(t, nmtF(cmd, byte(hVictim)), nmtF(cmd, 0))
	}
	// Wrong lengths (an NMT command is 2 bytes), extended and remote
	// frames, and classical frames longer than 8 bytes.
	for _, l := range []int{0, 1, 3, 8} {
		f := std(canopen.FuncNMT, make([]byte, l)...)
		if l > 0 {
			f.Data[0] = byte(canopen.NMTStop)
		}
		if l > 1 {
			f.Data[1] = byte(hVictim)
		}
		h.raw(t, f)
	}
	for _, l := range []uint8{9, 64, 255} {
		f := nmtF(byte(canopen.NMTStop), byte(hVictim))
		f.Len = l
		h.raw(t, f)
	}
	ext := nmtF(byte(canopen.NMTStop), byte(hVictim))
	ext.Flags |= canbus.FlagExtended
	rtr, _ := canbus.NewRemote(canopen.FuncNMT, false, 2)
	h.raw(t, ext, rtr)
	time.Sleep(30 * time.Millisecond)
	if st := n.State(); st != canopen.StatePreOperational {
		t.Fatalf("node state %v after invalid NMT commands", st)
	}

	// Broadcast (node 0) and the node's own ID act.
	h.raw(t, nmtF(byte(canopen.NMTStop), 0))
	waitState(canopen.StateStopped)
	h.raw(t, nmtF(byte(canopen.NMTEnterPreOperational), byte(hVictim)))
	waitState(canopen.StatePreOperational)
	// A flood of start/stop for other nodes: no effect.
	for i := range 2000 {
		id := byte(i%127 + 1)
		if id == byte(hVictim) {
			continue
		}
		h.raw(t, nmtF(byte(canopen.NMTStop), id), nmtF(byte(canopen.NMTResetNode), id))
	}
	time.Sleep(30 * time.Millisecond)
	if st := n.State(); st != canopen.StatePreOperational {
		t.Fatalf("node state %v after NMT commands for other nodes", st)
	}

	// The master refuses node-IDs out of range.
	ctx := nettest.Ctx(t)
	for _, id := range []canopen.NodeID{128, 200, 255} {
		if err := m.NMT(ctx, id, canopen.NMTStop); err == nil {
			t.Errorf("master sent NMT to node %d", id)
		}
	}
	if err := m.NMT(ctx, hVictim, canopen.NMTCommand(0x55)); err == nil {
		t.Error("master sent an unknown NMT command")
	}
	h.stillServes(t, m)
}

// TestHostileNMTResetFence sends malformed NMT reset commands, which the
// NMT slave rightly ignores (an NMT command is a 2-byte data frame): a
// remote frame, frames of 3, 8 and more bytes, and a CAN FD frame on a
// classical node. The SDO server fences itself at a reset of its node
// (sdo.WithResetFence) and opens again at the boot-up; a reset that
// never happens must not fence it.
func TestHostileNMTResetFence(t *testing.T) {
	v := byte(hVictim)
	cases := []struct {
		name string
		f    canbus.Frame
	}{
		{"remote frame", canbus.Frame{ID: canopen.FuncNMT, Flags: canbus.FlagRemote, Len: 2,
			Data: [canbus.MaxFDLen]byte{byte(canopen.NMTResetCommunication), 0}}},
		{"3 bytes", std(canopen.FuncNMT, byte(canopen.NMTResetCommunication), v, 0)},
		{"8 bytes", std(canopen.FuncNMT, byte(canopen.NMTResetNode), 0, 0, 0, 0, 0, 0, 0)},
		{"Len 64 classical", canbus.Frame{ID: canopen.FuncNMT, Len: 64,
			Data: [canbus.MaxFDLen]byte{byte(canopen.NMTResetNode), v}}},
		{"CAN FD frame", canbus.Frame{ID: canopen.FuncNMT, Flags: canbus.FlagFD, Len: 2,
			Data: [canbus.MaxFDLen]byte{byte(canopen.NMTResetCommunication), v}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The CAN FD case needs ports that carry CAN FD frames; the
			// node and master stay classical.
			h := newHNet(t, false)
			h.fd = c.f.Flags&canbus.FlagFD != 0
			n := startNode(t, h.port(t), nettest.Dict(t, hVictim, nil))
			h.fd = false
			m := h.victimMaster(t)
			h.stillServes(t, m)
			h.raw(t, c.f)
			time.Sleep(30 * time.Millisecond)
			if st := n.State(); st != canopen.StatePreOperational {
				t.Fatalf("node state %v after a malformed reset", st)
			}
			h.stillServes(t, m)
		})
	}
}

// TestHostileFrames puts frames a conforming controller never produces
// on every CANopen identifier the node and master listen to: classical
// frames longer than 8 bytes, remote frames, extended frames with
// CANopen IDs and empty frames. Nothing may panic, and both serve
// afterwards.
func TestHostileFrames(t *testing.T) {
	for _, fd := range []bool{false, true} {
		name := "classical"
		if fd {
			name = "fd"
		}
		t.Run(name, func(t *testing.T) {
			h := newHNet(t, fd)
			n := h.victimNode(t)
			m := h.victimMaster(t)
			h.stillServes(t, m)
			v := canbus.ID(hVictim)
			ids := []canbus.ID{
				canopen.FuncNMT, canopen.FuncSYNC, canopen.FuncEMCY + v, canopen.FuncTIME,
				canopen.FuncTPDO1 + v, canopen.FuncRPDO1 + v, canopen.FuncTPDO4 + v, canopen.FuncRPDO4 + v,
				canopen.FuncSDOTx + v, canopen.FuncSDORx + v, canopen.FuncSDORx + canbus.ID(hUSDOCli),
				canopen.FuncHeartbeat + v, canopen.FuncHeartbeat + 1, lss.MasterID, lss.SlaveID,
			}
			pattern := make([]byte, 64)
			for i := range pattern {
				pattern[i] = byte(i*37 + 1)
			}
			for _, id := range ids {
				// Classical frames with Len beyond 8, up to 255 (Payload
				// clamps at 64; the receivers must not index beyond it).
				for _, l := range []uint8{0, 9, 12, 16, 63, 64, 65, 255} {
					var f canbus.Frame
					f.ID = id
					copy(f.Data[:], pattern)
					f.Len = l
					h.raw(t, f)
				}
				// Remote frames of every DLC.
				for dlc := range uint8(9) {
					f := canbus.Frame{ID: id, Flags: canbus.FlagRemote, Len: dlc}
					h.raw(t, f)
				}
				// The same IDs as extended frames.
				f := canbus.Frame{ID: id, Flags: canbus.FlagExtended, Len: 8}
				copy(f.Data[:], pattern)
				h.raw(t, f)
				if fd {
					fdf := canbus.Frame{ID: id, Flags: canbus.FlagFD, Len: 64}
					copy(fdf.Data[:], pattern)
					h.raw(t, fdf)
				}
			}
			// Node guarding: remote frames on 700h + node-ID are a valid
			// request; a flood of them must not hurt.
			for range 500 {
				h.raw(t, canbus.Frame{ID: canopen.FuncHeartbeat + v, Flags: canbus.FlagRemote, Len: 1})
			}
			time.Sleep(50 * time.Millisecond)
			if st := n.State(); st != canopen.StatePreOperational {
				t.Errorf("node state %v after the garbage", st)
			}
			h.stillServes(t, m)
		})
	}
}
