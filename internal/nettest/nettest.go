// SPDX-License-Identifier: MIT

// Package nettest provides what the network-level tests (node, master,
// e2e) share: a device dictionary with application objects, contexts,
// polling and a goroutine leak check.
package nettest

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Application objects of Dict.
const (
	IdxOut8   uint16 = 0x2000 // UNSIGNED8, rww, mappable (process output)
	IdxOut16  uint16 = 0x2001 // UNSIGNED16, rww, mappable
	IdxOut32  uint16 = 0x2002 // UNSIGNED32, rww, mappable
	IdxIn32   uint16 = 0x2100 // INTEGER32, rwr, mappable (process input)
	IdxIn8    uint16 = 0x2101 // UNSIGNED8, rwr, mappable
	IdxDomain uint16 = 0x2200 // DOMAIN, rw
	IdxParam  uint16 = 0x2300 // UNSIGNED32, rw, default 0x12345678
)

// Identity values of Dict (the serial number is the node-ID).
const (
	DeviceType  uint32 = 0x00020191
	VendorID    uint32 = 0x00000F7A
	ProductCode uint32 = 0x00001234
	Revision    uint32 = 0x00010002
)

// Dict returns a device dictionary for a node: od.Standard with 4 RPDOs and
// 4 TPDOs, 4 heartbeat consumer entries, an error history of 8, node
// guarding, TIME, the error behaviour object 0x1029 (sub 1 = 1, no state
// change) and the application objects above. edit, when not nil, may
// change the configuration first.
func Dict(t testing.TB, node canopen.NodeID, edit func(*od.Config)) *od.Dictionary {
	t.Helper()
	cfg := od.Config{
		NodeID: node, DeviceType: DeviceType,
		VendorID: VendorID, ProductCode: ProductCode, Revision: Revision, SerialNumber: uint32(node),
		DeviceName: "nettest device", HeartbeatConsumers: 4, ErrorHistory: 8,
		RPDOs: 4, TPDOs: 4, NodeGuarding: true, Time: true,
	}
	if edit != nil {
		edit(&cfg)
	}
	d, err := od.Standard(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u8, u16, u32 := canopen.Unsigned8, canopen.Unsigned16, canopen.Unsigned32
	objs := []*od.Object{
		od.NewRecord(0x1029, "Error behaviour", od.Sub(1, "Communication error", u8, od.AccessRW, uint8(1))),
		mappable(od.NewVar(IdxOut8, "Out8", u8, od.AccessRWW, nil)),
		mappable(od.NewVar(IdxOut16, "Out16", u16, od.AccessRWW, nil)),
		mappable(od.NewVar(IdxOut32, "Out32", u32, od.AccessRWW, nil)),
		mappable(od.NewVar(IdxIn32, "In32", canopen.Integer32, od.AccessRWR, nil)),
		mappable(od.NewVar(IdxIn8, "In8", u8, od.AccessRWR, nil)),
		od.NewVar(IdxDomain, "Domain", canopen.Domain, od.AccessRW, nil),
		od.NewVar(IdxParam, "Param", u32, od.AccessRW, uint32(0x12345678)),
	}
	for _, o := range objs {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func mappable(o *od.Object) *od.Object {
	for _, v := range o.Subs {
		v.PDOMappable = true
	}
	return o
}

// Ctx returns a context that ends with the test or after 20 s.
func Ctx(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Eventually polls cond every 2 ms until it holds, failing the test after
// timeout.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// CheckLeaks fails the test when goroutines started during it are still
// running 2 s after it ended (its cleanups run first, as registered
// before them).
func CheckLeaks(t testing.TB) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<18)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:n])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}
