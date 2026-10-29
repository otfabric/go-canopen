// SPDX-License-Identifier: MIT

package usdo

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// Objects of the test devices.
const (
	idxU8     uint16 = 0x2000
	idxU32    uint16 = 0x2001
	idxDomain uint16 = 0x2002 // DOMAIN, any size
	idxRO     uint16 = 0x2003
	idxWO     uint16 = 0x2004
	idxStr    uint16 = 0x2005 // VISIBLE_STRING, at most 100 bytes
)

// device returns a dictionary of a node with the test objects.
func device(t testing.TB, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: node})
	if err != nil {
		t.Fatal(err)
	}
	str := od.NewVar(idxStr, "str", canopen.VisibleString, od.AccessRW, "hello")
	str.Subs[0].MaxSize = 100
	for _, o := range []*od.Object{
		od.NewVar(idxU8, "u8", canopen.Unsigned8, od.AccessRW, nil),
		od.NewVar(idxU32, "u32", canopen.Unsigned32, od.AccessRW, nil),
		od.NewVar(idxDomain, "domain", canopen.Domain, od.AccessRW, nil),
		od.NewVar(idxRO, "ro", canopen.Unsigned16, od.AccessRO, nil),
		od.NewVar(idxWO, "wo", canopen.Unsigned16, od.AccessWO, nil),
		str,
	} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// fdPorts returns n CAN FD ports of one virtual bus.
func fdPorts(t testing.TB, n int) []*canbus.Bus {
	t.Helper()
	return testbus.Ports(t, n, virtual.WithFD())
}

// startServer starts a server for a new device on bus.
func startServer(t testing.TB, bus canopen.Bus, node canopen.NodeID, opts ...Option) (*Server, *od.Dictionary) {
	t.Helper()
	d := device(t, node)
	s, err := NewServer(bus, d, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, d
}

// startClient starts a client of node on bus.
func startClient(t testing.TB, bus canopen.Bus, node canopen.NodeID, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(bus, node, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// pattern returns n bytes of test data.
func pattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func tctx(t testing.TB) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// raw is a bus port used to inject and observe frames.
type raw struct {
	t   testing.TB
	bus *canbus.Bus
	sub *canbus.Subscription
}

func newRaw(t testing.TB, bus *canbus.Bus) *raw {
	t.Helper()
	sub, err := bus.Subscribe(canbus.WithFilters(filter(canopen.FuncSDOTx)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return &raw{t, bus, sub}
}

// request sends payload as a request of client node.
func (r *raw) request(node canopen.NodeID, payload []byte) {
	r.t.Helper()
	f, err := canbus.NewFD(canopen.FuncSDORx+canbus.ID(node), false, false, canbus.PadFD(payload, 0))
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.bus.Send(context.Background(), &f); err != nil {
		r.t.Fatal(err)
	}
}

// send sends a message as a request of client node.
func (r *raw) send(node canopen.NodeID, m Message) {
	r.t.Helper()
	b, err := m.Encode()
	if err != nil {
		r.t.Fatal(err)
	}
	r.request(node, b)
}

// recv returns the next response, failing after d.
func (r *raw) recv(d time.Duration) (canopen.NodeID, Message) {
	r.t.Helper()
	select {
	case f := <-r.sub.Frames():
		from, ok := sender(&f, canopen.FuncSDOTx)
		if !ok || !f.IsFD() {
			r.t.Fatalf("unexpected frame %v", f)
		}
		m, err := Decode(f.Payload())
		if err != nil {
			r.t.Fatalf("decode %v: %v", f, err)
		}
		return from, m
	case <-time.After(d):
		r.t.Fatal("no response")
	}
	return 0, Message{}
}

// none checks that no response comes within d.
func (r *raw) none(d time.Duration) {
	r.t.Helper()
	select {
	case f := <-r.sub.Frames():
		r.t.Fatalf("unexpected response %v", f)
	case <-time.After(d):
	}
}

func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func equal(a, b []byte) bool { return bytes.Equal(a, b) }
