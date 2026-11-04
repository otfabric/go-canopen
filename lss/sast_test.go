// SPDX-License-Identifier: MIT

package lss

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// TestDelayMsBounds checks the edges of the 16-bit switch delay.
func TestDelayMsBounds(t *testing.T) {
	for _, d := range []time.Duration{-500 * time.Microsecond, -1, 65536*time.Millisecond - 1, 1 << 62} {
		ms, err := delayMs(d)
		want := d >= 0 && d < 65536*time.Millisecond
		if (err == nil) != want {
			t.Errorf("%v: %d, %v", d, ms, err)
		}
		if want && int64(ms) != d.Milliseconds() {
			t.Errorf("%v: %d ms", d, ms)
		}
	}
}

// TestAddressFromDictionaryWide: an identity entry wider than UNSIGNED32
// was truncated into the LSS address; it is now an error.
func TestAddressFromDictionaryWide(t *testing.T) {
	d, err := od.Standard(od.Config{NodeID: 3})
	if err != nil {
		t.Fatal(err)
	}
	d.Remove(od.IndexIdentity)
	u64 := func(sub uint8, v uint64) *od.Variable {
		return od.Sub(sub, "part", canopen.Unsigned64, od.AccessRO, v)
	}
	if err := d.Add(od.NewRecord(od.IndexIdentity, "Identity", u64(1, 1), u64(2, 2), u64(3, 1<<32|3), u64(4, 4))); err != nil {
		t.Fatal(err)
	}
	if a, err := AddressFromDictionary(d); err == nil || !strings.Contains(err.Error(), "exceeds UNSIGNED32") {
		t.Fatalf("address %v, %v from a 33-bit revision", a, err)
	}
}

// TestFastscanAlwaysAcknowledged: a peer acknowledging every Fastscan
// query looks like 2^128 devices; Fastscan walked a whole part before
// looking at Max and ran until the context ended. It now stops at Max,
// and without Max fails with ErrFastscan beyond 127 devices.
func TestFastscanAlwaysAcknowledged(t *testing.T) {
	checkLeaks(t)
	ports := testbus.Ports(t, 2)
	m := startMaster(t, ports[0], WithFastscanTimeout(2*time.Millisecond))
	responder(t, ports[1], func(f canbus.Frame) (canbus.Frame, bool) {
		return frame(SlaveID, CmdIdentifySlave), Command(f.Data[0]) == CmdFastscan
	})
	ctx := ctxT(t)
	got, err := m.Fastscan(ctx, FastscanOptions{Max: 3})
	if err != nil || len(got) != 3 {
		t.Fatalf("Max 3: %d addresses, %v", len(got), err)
	}
	for i, a := range got {
		if a != (Address{Serial: uint32(i)}) {
			t.Errorf("address %d: %v", i, a)
		}
	}
	start := time.Now()
	got, err = m.Fastscan(ctx, FastscanOptions{})
	if !errors.Is(err, ErrFastscan) || len(got) > maxDevices {
		t.Fatalf("no Max: %d addresses, %v", len(got), err)
	}
	if ctx.Err() != nil {
		t.Fatalf("ran into the context deadline after %v", time.Since(start))
	}
	t.Logf("no Max: %v, %d addresses", time.Since(start), len(got))
}
