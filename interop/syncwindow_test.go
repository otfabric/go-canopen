// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

// syncWindowCases are the synchronous RPDO cases of the SYNC window
// (0x1007, µs): the RPDO follows a SYNC after delay ms; whether a device
// that enforces the window (go-canopen, lely) applies it.
var syncWindowCases = []struct {
	name    string
	window  uint32
	delayMs int
	applied bool
}{
	{"rpdo-sync-no-window", 0, 150, true},
	{"rpdo-sync-in-window", 50000, 5, true},
	{"rpdo-sync-after-window", 50000, 150, false},
}

// stackSyncWindow: each stack's RPDO sender (rpdo-send) against RPDO 1 of
// a go-canopen node (0x2001/16, 0x2003/32): event-driven, then
// synchronous with and without the SYNC window. go-canopen discards a
// synchronous RPDO received after the window (CiA 301 allows it).
func stackSyncWindow(t *testing.T, ifc, a string, c caps, f *eds.File, sc scenarioFunc) {
	names := []string{"rpdo-event"}
	for _, s := range syncWindowCases {
		names = append(names, s.name)
	}
	if !c.op("rpdo-send") || !c.Features["rpdoSend"] {
		for _, n := range names {
			sc(t, n, func(t *testing.T) { skipf(t, "%s has no RPDO sender (rpdo-send)", a) })
		}
		return
	}
	g := startGoDevice(t, ifc, f, goNode)
	if err := g.n.SetState(canopen.StateOperational); err != nil {
		t.Fatal(err)
	}
	cl, err := sdo.NewClient(openBus(t, ifc), goNode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	ctx := ctxT(t, 5*time.Minute)
	send := func(t *testing.T, v1, v3 string, sync bool, delay int) run {
		args := []string{"rpdo-send", "--interface", ifc, "--node-id", fmt.Sprint(goNode), "--rpdo", "1",
			"--set", "0x2001:00=" + v1, "--set", "0x2003:00=" + v3}
		if sync {
			args = append(args, "--sync", "--delay-ms", fmt.Sprint(delay))
		}
		return adapterRun(t, "", a, args...)
	}
	value := func() uint64 { v, _ := g.d.GetUint(0x2001, 0); return v }
	sc(t, "rpdo-event", func(t *testing.T) {
		r := send(t, "a1b2", "01020304", false, 0)
		if r.code != 0 {
			differ(t, "rpdo-send: %v", r)
		}
		ok := waitFor(2*time.Second, func() bool { return value() == 0xB2A1 })
		if v3, _ := g.d.GetUint(0x2003, 0); !ok || v3 != 0x04030201 {
			differ(t, "0x2001 = %#x, 0x2003 = %#x; want 0xb2a1, 0x04030201", value(), v3)
		}
	})
	synced := false
	for i, s := range syncWindowCases {
		s := s
		v1 := fmt.Sprintf("%02x%02x", 0xc3+2*i, 0xd4+2*i)
		sc(t, s.name, func(t *testing.T) {
			if !synced {
				if err := cl.Download(ctx, 0x1400, 2, []byte{1}); err != nil {
					failf(t, "RPDO 1 transmission type 1: %v", err)
				}
				synced = true
			}
			if err := cl.Download(ctx, od.IndexSyncWindow, 0, le(uint64(s.window), 4)); err != nil {
				failf(t, "0x1007 = %d: %v", s.window, err)
			}
			before := value()
			outside := g.n.Stats().RPDO[1].OutsideWindow
			r := send(t, v1, "05060708", true, s.delayMs)
			if r.code != 0 {
				differ(t, "rpdo-send: %v", r)
			}
			want := before
			if s.applied {
				want = uint64(r2u16(v1))
			}
			time.Sleep(100 * time.Millisecond)
			after := value()
			discarded := g.n.Stats().RPDO[1].OutsideWindow - outside
			if after != want || discarded != map[bool]uint64{true: 0, false: 1}[s.applied] {
				differ(t, "window %d µs, RPDO %d ms after SYNC: 0x2001 %#x -> %#x (want %#x), %d discarded", s.window, s.delayMs, before, after, want, discarded)
			}
			what := "applied"
			if !s.applied {
				what = "discarded (outside the window)"
			}
			info(t).detail = fmt.Sprintf("window %d µs, RPDO %d ms after SYNC: %s", s.window, s.delayMs, what)
		})
	}
}

// r2u16 decodes 4 hex digits of a little-endian UNSIGNED16.
func r2u16(h string) uint16 {
	var b [2]byte
	_, _ = fmt.Sscanf(h, "%02x%02x", &b[0], &b[1])
	return uint16(b[0]) | uint16(b[1])<<8
}

// masterSyncWindow: go-canopen's master sends a synchronous RPDO 1
// (Master.RPDOSender) between two SYNCs (Master.SYNC) to each stack's
// device, with and without the SYNC window: a device that declares
// syncWindowRpdo discards it after the window; one that does not applies
// it (CiA 301 lets the device choose).
func masterSyncWindow(t *testing.T, ifc, a string, c caps, desc *od.Dictionary, sc scenarioFunc) {
	if !c.Features["deviceRpdo"] {
		for _, s := range syncWindowCases {
			sc(t, s.name, func(t *testing.T) { skipf(t, "%s's device applies no RPDOs (deviceRpdo false)", a) })
		}
		return
	}
	m, _ := newMaster(t, ifc)
	dev := startContainer(t, a, deviceArgs(ifc)...)
	dev.await(t, `"type":"ready"`, 90*time.Second)
	ctx := ctxT(t, 5*time.Minute)
	cl, err := m.SDO(deviceNode)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Download(ctx, 0x1400, 2, []byte{1}); err != nil {
		t.Fatalf("RPDO 1 transmission type 1: %v", err)
	}
	if err := m.StartNode(ctx, deviceNode); err != nil {
		t.Fatal(err)
	}
	s, err := m.RPDOSender(ctx, deviceNode, 1, desc)
	if err != nil {
		t.Fatalf("RPDO1 sender: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	for i, k := range syncWindowCases {
		k := k
		v := uint16(0x5100 + i)
		sc(t, k.name, func(t *testing.T) {
			if err := cl.Download(ctx, od.IndexSyncWindow, 0, le(uint64(k.window), 4)); err != nil {
				failf(t, "0x1007 = %d: %v", k.window, err)
			}
			before, err := cl.ReadUint(ctx, 0x2001, 0)
			if err != nil {
				failf(t, "%v", err)
			}
			if err := m.SYNC().Send(ctx); err != nil {
				failf(t, "SYNC: %v", err)
			}
			time.Sleep(time.Duration(k.delayMs) * time.Millisecond)
			if err := s.Send(ctx, v, int32(-7)); err != nil {
				failf(t, "RPDO: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
			if err := m.SYNC().Send(ctx); err != nil {
				failf(t, "SYNC: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			after, err := cl.ReadUint(ctx, 0x2001, 0)
			if err != nil {
				failf(t, "%v", err)
			}
			applied := k.applied || !c.Features["syncWindowRpdo"]
			want := before
			if applied {
				want = uint64(v)
			}
			if after != want {
				differ(t, "window %d µs, RPDO %d ms after SYNC: 0x2001 %#x -> %#x, want %#x", k.window, k.delayMs, before, after, want)
			}
			what := "applied"
			switch {
			case !applied:
				what = "discarded (syncWindowRpdo)"
			case !k.applied:
				what = "applied: the device does not discard RPDOs after the window (syncWindowRpdo false, CiA 301 'may')"
			}
			info(t).detail = fmt.Sprintf("window %d µs, RPDO %d ms after SYNC: %s", k.window, k.delayMs, what)
		})
	}
}

// fdSkips records CANopen FD (CiA 1301: USDO, CAN FD PDOs) as skips: no
// canopen-interop image declares it, because none of the pinned stacks
// (nor their development branches) implements it.
func fdSkips(t *testing.T, a string, names []string, sc scenarioFunc) {
	for _, n := range names {
		sc(t, n, func(t *testing.T) {
			skipf(t, "%s implements no CANopen FD (CiA 1301): contract 1.1 has no FD operation or feature", a)
		})
	}
}
