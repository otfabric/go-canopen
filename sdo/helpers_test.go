// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

const testNode canopen.NodeID = 5

// Objects of the test dictionary.
const (
	idxDomain   uint16 = 0x2000 // DOMAIN rw
	idxString   uint16 = 0x2001 // VISIBLE_STRING ro
	idxLimited  uint16 = 0x2002 // UNSIGNED32 rw, 10..1000
	idxWO       uint16 = 0x2003 // UNSIGNED16 wo
	idxU8       uint16 = 0x2004 // UNSIGNED8 rw
	idxHook     uint16 = 0x2006 // DOMAIN rw whose hooks fail with a plain error
	idxI16      uint16 = 0x2007 // INTEGER16 rw
	idxReal     uint16 = 0x2008 // REAL32 rw
	idxSmallDom uint16 = 0x2009 // DOMAIN rw, MaxSize 16
)

const testString = "go-canopen SDO test device"

func testDict(t testing.TB) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: testNode, DeviceType: 0x00020192})
	if err != nil {
		t.Fatal(err)
	}
	lim := od.NewVar(idxLimited, "limited", canopen.Unsigned32, od.AccessRW, uint32(100))
	lim.Subs[0].Low, _ = canopen.Encode(canopen.Unsigned32, uint32(10))
	lim.Subs[0].High, _ = canopen.Encode(canopen.Unsigned32, uint32(1000))
	small := od.NewVar(idxSmallDom, "small", canopen.Domain, od.AccessRW, []byte{})
	small.Subs[0].MaxSize = 16
	for _, o := range []*od.Object{
		od.NewVar(idxDomain, "domain", canopen.Domain, od.AccessRW, []byte{}),
		od.NewVar(idxString, "string", canopen.VisibleString, od.AccessRO, testString),
		lim,
		od.NewVar(idxWO, "wo", canopen.Unsigned16, od.AccessWO, nil),
		od.NewVar(idxU8, "u8", canopen.Unsigned8, od.AccessRW, nil),
		od.NewVar(idxHook, "hook", canopen.Domain, od.AccessRW, []byte{1, 2, 3}),
		od.NewVar(idxI16, "i16", canopen.Integer16, od.AccessRW, int16(-2)),
		od.NewVar(idxReal, "real", canopen.Real32, od.AccessRW, float32(1.5)),
		small,
	} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	errHook := errors.New("hook failed")
	d.OnRead(idxHook, 0, func(*od.Variable, []byte) ([]byte, error) { return nil, errHook })
	d.OnWrite(idxHook, 0, func(*od.Variable, []byte) error { return errHook })
	return d
}

// checkLeaks fails the test when goroutines started during it are still
// running at its end. Call it first: its cleanup then runs last.
func checkLeaks(t testing.TB) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:n])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func ctxT(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// startServer starts a server for d on bus.
func startServer(t testing.TB, bus canopen.Bus, d *od.Dictionary, opts ...ServerOption) *Server {
	t.Helper()
	s, err := NewServer(bus, d, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newClient(t testing.TB, bus canopen.Bus, opts ...ClientOption) *Client {
	t.Helper()
	c, err := NewClient(bus, testNode, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// setup returns a client and a server of the test dictionary on a pair.
func setup(t testing.TB, sopts []ServerOption, copts ...ClientOption) (*Client, *Server, *od.Dictionary) {
	t.Helper()
	a, b := testbus.Pair(t)
	d := testDict(t)
	s := startServer(t, b, d, sopts...)
	return newClient(t, a, copts...), s, d
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// wantAbort checks that err is an abort with the code, remote or local.
func wantAbort(t testing.TB, err error, code canopen.AbortCode, remote bool) {
	t.Helper()
	var ae *canopen.AbortError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want abort %v", err, code)
	}
	if ae.Code != code || ae.Remote != remote {
		t.Fatalf("abort = %v (remote %v), want %v (remote %v)", ae.Code, ae.Remote, code, remote)
	}
}

// peer is a raw CAN endpoint that sends and receives SDO frames.
type peer struct {
	t   testing.TB
	bus *canbus.Bus
	sub *canbus.Subscription
	tx  canopen.COBID
}

func newPeer(t testing.TB, bus *canbus.Bus, rx, tx canopen.COBID) *peer {
	t.Helper()
	sub, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{rx.Filter()}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Close() })
	return &peer{t: t, bus: bus, sub: sub, tx: tx}
}

func (p *peer) send(f frame) {
	p.t.Helper()
	p.sendLen(f, 8)
}

func (p *peer) sendLen(f frame, n int) {
	p.t.Helper()
	cf := canFrame(p.tx, f)
	cf.Len = uint8(n)
	if err := p.bus.Send(context.Background(), &cf); err != nil {
		p.t.Fatal(err)
	}
}

// recv returns the next frame, or fails the test after d.
func (p *peer) recv(d time.Duration) frame {
	p.t.Helper()
	f, ok := p.tryRecv(d)
	if !ok {
		p.t.Fatalf("no frame within %v", d)
	}
	return f
}

func (p *peer) tryRecv(d time.Duration) (frame, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cf, err := p.sub.Receive(ctx)
	if err != nil {
		return frame{}, false
	}
	if cf.Len != 8 {
		p.t.Errorf("frame of %d bytes sent, want 8", cf.Len)
	}
	f, _ := payload(&cf)
	return f, true
}

// script runs a fake server on bus: for each request it sends the frames
// respond returns. It stops at the end of the test.
func script(t testing.TB, bus *canbus.Bus, respond func(req frame) []frame) *[]frame {
	t.Helper()
	p := newPeer(t, bus, canopen.NewCOBID(0x600+canbus.ID(testNode)), canopen.NewCOBID(0x580+canbus.ID(testNode)))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var got []frame
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			cf, err := p.sub.Receive(ctx)
			if err != nil {
				return
			}
			f, _ := payload(&cf)
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
			for _, r := range respond(f) {
				out := canFrame(p.tx, r)
				_ = bus.Send(ctx, &out)
			}
		}
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })
	return &got
}

// recorder collects the frames a machine sends.
type recorder struct{ out []frame }

func (r *recorder) send(f frame) { r.out = append(r.out, f) }

func (r *recorder) take() []frame {
	o := r.out
	r.out = nil
	return o
}

func newMachine(d *od.Dictionary, maxSize int, blk uint8) (*machine, *recorder) {
	r := &recorder{}
	return &machine{d: d, out: r.send, maxSize: maxSize, blkSize: blk, crcOK: true, st: &serverStats{}}, r
}

func equal(t testing.TB, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("data mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

// eventually waits up to 2 s for cond.
func eventually(t testing.TB, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
	}
}
