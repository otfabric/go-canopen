// SPDX-License-Identifier: MIT

// Package testbus provides CAN buses for tests: ports of one virtual bus,
// and two virtual buses joined by a fault-injecting chaos bridge.
package testbus

import (
	"context"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/virtual"
)

// Ports returns n canbus.Bus values on one virtual bus (no faults). They
// are closed when the test ends.
func Ports(t testing.TB, n int, opts ...virtual.PortOption) []*canbus.Bus {
	t.Helper()
	vb := virtual.New(virtual.Config{Name: "vcan-test"})
	t.Cleanup(func() { _ = vb.Close() })
	return open(t, vb, n, opts...)
}

// Pair returns two connected buses on one virtual bus.
func Pair(t testing.TB) (a, b *canbus.Bus) {
	t.Helper()
	p := Ports(t, 2)
	return p[0], p[1]
}

// Virtual returns ports of one virtual bus together with the bus, whose
// faults (SetFaults) the test may change.
func Virtual(t testing.TB, n int, opts ...virtual.PortOption) (*virtual.Bus, []*canbus.Bus) {
	t.Helper()
	vb := virtual.New(virtual.Config{Name: "vcan-test"})
	t.Cleanup(func() { _ = vb.Close() })
	return vb, open(t, vb, n, opts...)
}

func open(t testing.TB, vb *virtual.Bus, n int, opts ...virtual.PortOption) []*canbus.Bus {
	t.Helper()
	out := make([]*canbus.Bus, n)
	for i := range out {
		p, err := vb.Open(opts...)
		if err != nil {
			t.Fatal(err)
		}
		b, err := canbus.NewBus(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		out[i] = b
	}
	return out
}

// Chaos returns two buses on separate virtual buses joined by a chaos
// bridge with cfg in both directions, and the bridge (for its Stats).
func Chaos(t testing.TB, cfg chaos.Config) (a, b *canbus.Bus, br *chaos.Bridge) {
	t.Helper()
	va := virtual.New(virtual.Config{Name: "vcan-a"})
	vb := virtual.New(virtual.Config{Name: "vcan-b"})
	t.Cleanup(func() { _ = va.Close(); _ = vb.Close() })
	ia, err := va.Open()
	if err != nil {
		t.Fatal(err)
	}
	ib, err := vb.Open()
	if err != nil {
		t.Fatal(err)
	}
	br, err = chaos.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = br.Both(ctx, ia, ib) }()
	t.Cleanup(func() { cancel(); <-done })
	a = open(t, va, 1)[0]
	b = open(t, vb, 1)[0]
	return a, b, br
}
