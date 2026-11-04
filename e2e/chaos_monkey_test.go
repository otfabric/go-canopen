// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

// chaosIterations is the number of seeds TestMonkeyNodeSDO runs:
// CANOPEN_CHAOS_ITERATIONS overrides the default (make chaos runs many).
func chaosIterations(def int) int {
	if n, err := strconv.Atoi(os.Getenv("CANOPEN_CHAOS_ITERATIONS")); err == nil && n > 0 {
		return n
	}
	return def
}

// chaosBase returns the seed the seeds are drawn from:
// CANOPEN_CHAOS_SEED replays a run, otherwise the time.
func chaosBase() uint64 {
	if s, err := strconv.ParseUint(os.Getenv("CANOPEN_CHAOS_SEED"), 0, 64); err == nil {
		return s
	}
	return uint64(time.Now().UnixNano())
}

// TestMonkeyNodeSDO runs a full node (heartbeat producer, NMT slave, SDO
// server with its reset fence, PDOs) and an SDO client through a chaos
// bridge that drops, duplicates, delays and reorders frames, with random
// expedited, segmented and block transfers and NMT commands (stop,
// pre-operational, reset communication) in between. Invariants:
//
//   - a transfer either succeeds with the right data (a download is
//     stored as sent, an upload returns the value stored) or fails with
//     an abort or a timeout, never with another error, never a hang;
//   - a stopped node refuses SDO cleanly, and after reset communication
//     the node comes back pre-operational and serves;
//   - after the faults, a client on the node's side of the bridge (no
//     faults) transfers correctly: the node still serves;
//   - everything shuts down without leaking goroutines.
//
// The base seed is logged; CANOPEN_CHAOS_SEED replays a run, each
// sub-test's name carries its own seed and fault mix.
func TestMonkeyNodeSDO(t *testing.T) {
	base := chaosBase()
	t.Logf("base seed %d (CANOPEN_CHAOS_SEED=%d replays)", base, base)
	seeds := rand.New(rand.NewPCG(base, 0x9E3779B97F4A7C15))
	for range chaosIterations(2) {
		seed := seeds.Uint64()
		rng := rand.New(rand.NewPCG(seed, 1))
		cfg := chaos.Config{
			Seed:      int64(seed & 0x7FFFFFFFFFFFFFFF),
			Drop:      rng.Float64() * 0.03,
			Duplicate: rng.Float64() * 0.03,
			Delay:     rng.Float64() * 0.05,
			DelayBy:   time.Duration(rng.IntN(3)+1) * time.Millisecond,
			Reorder:   rng.Float64() * 0.02,
		}
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Logf("faults %+v", cfg)
			nettest.CheckLeaks(t)
			monkeyNodeSDO(t, cfg, rng)
		})
	}
}

// chaosClean reports the documented failures of a transfer over a
// faulty bus: an abort (either side) or a timeout.
func chaosClean(err error) bool {
	var ae *canopen.AbortError
	return errors.As(err, &ae) || errors.Is(err, canopen.ErrTimeout)
}

func monkeyNodeSDO(t *testing.T, cfg chaos.Config, rng *rand.Rand) {
	// Client side and node side, joined by the chaos bridge.
	va := virtual.New(virtual.Config{Name: "monkey-client"})
	vn := virtual.New(virtual.Config{Name: "monkey-node"})
	t.Cleanup(func() { _ = va.Close(); _ = vn.Close() })
	ba, err := va.Open()
	if err != nil {
		t.Fatal(err)
	}
	bn, err := vn.Open()
	if err != nil {
		t.Fatal(err)
	}
	br, err := chaos.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = br.Both(ctx, ba, bn) }()
	t.Cleanup(func() { cancel(); <-done })
	open := func(vb *virtual.Bus) *canbus.Bus {
		p, err := vb.Open()
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

	d := nettest.Dict(t, hVictim, func(c *od.Config) { c.HeartbeatMs = 20 })
	n := startNode(t, open(vn), d, node.WithSDOServerOptions(sdo.WithTimeout(80*time.Millisecond)))
	cb := open(va)
	c, err := sdo.NewClient(cb, hVictim, sdo.WithTimeout(60*time.Millisecond), sdo.WithRetries(3), sdo.WithBlockSize(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	nmtBus := open(va)
	tctx := nettest.Ctx(t)

	sendNMT := func(cmd canopen.NMTCommand) {
		// NMT is unconfirmed: send it until the node took the state.
		f, _ := canbus.NewStandard(canopen.FuncNMT, []byte{byte(cmd), byte(hVictim)})
		want := canopen.StatePreOperational
		if cmd == canopen.NMTStop {
			want = canopen.StateStopped
		}
		nettest.Eventually(t, 5*time.Second, "node takes "+cmd.String(), func() bool {
			if n.State() == want {
				return true
			}
			_ = nmtBus.Send(tctx, &f)
			time.Sleep(5 * time.Millisecond)
			return n.State() == want
		})
	}

	var ok, failed int
	check := func(what string, err error) bool {
		t.Helper()
		switch {
		case err == nil:
			ok++
			return true
		case chaosClean(err):
			failed++
			return false
		default:
			t.Fatalf("%s: unclean error %v", what, err)
			return false
		}
	}
	modes := []sdo.BlockMode{sdo.BlockNever, sdo.BlockAlways}
	for i := range 30 {
		switch op := rng.IntN(10); {
		case op == 0:
			// Stopped: SDO is refused (an abort or no answer), never served.
			sendNMT(canopen.NMTStop)
			if _, err := c.Upload(tctx, nettest.IdxParam, 0); err == nil || !chaosClean(err) {
				t.Fatalf("upload from a stopped node: %v", err)
			}
			sendNMT(canopen.NMTEnterPreOperational)
		case op == 1:
			f, _ := canbus.NewStandard(canopen.FuncNMT, []byte{byte(canopen.NMTResetCommunication), byte(hVictim)})
			_ = nmtBus.Send(tctx, &f)
			time.Sleep(10 * time.Millisecond)
			sendNMT(canopen.NMTEnterPreOperational)
		case op < 5:
			v := rng.Uint32()
			err := c.WriteUint32(tctx, nettest.IdxParam, 0, v)
			if check(fmt.Sprintf("op %d: write %x", i, v), err) {
				if got, _ := d.GetUint(nettest.IdxParam, 0); uint32(got) != v {
					t.Fatalf("op %d: write %x succeeded, node has %x", i, v, got)
				}
			}
			// A failed write may still land late (a delayed request):
			// the read returns the value from before or after it.
			before, _ := d.GetUint(nettest.IdxParam, 0)
			got, err := c.ReadUint(tctx, nettest.IdxParam, 0)
			after, _ := d.GetUint(nettest.IdxParam, 0)
			if check(fmt.Sprintf("op %d: read", i), err) && got != before && got != after {
				t.Fatalf("op %d: read %x, node has %x/%x", i, got, before, after)
			}
		default:
			mode := modes[rng.IntN(len(modes))]
			want := make([]byte, rng.IntN(600))
			for j := range want {
				want[j] = byte(rng.Uint32())
			}
			err := c.Download(tctx, nettest.IdxDomain, 0, want, sdo.UseBlockMode(mode))
			if check(fmt.Sprintf("op %d: %v download of %d bytes", i, mode, len(want)), err) {
				if got, _ := d.Get(nettest.IdxDomain, 0); !bytes.Equal(got, want) {
					t.Fatalf("op %d: %v download succeeded with wrong data (%d bytes stored, %d sent)", i, mode, len(got), len(want))
				}
			} else if err := d.Set(nettest.IdxDomain, 0, want); err != nil {
				t.Fatal(err)
			}
			got, err := c.Upload(tctx, nettest.IdxDomain, 0, sdo.UseBlockMode(mode))
			if check(fmt.Sprintf("op %d: %v upload", i, mode), err) && !bytes.Equal(got, want) {
				t.Fatalf("op %d: %v upload succeeded with wrong data (%d bytes, want %d)", i, mode, len(got), len(want))
			}
		}
	}

	// The node's side, without faults: the node still serves correctly.
	clean, err := sdo.NewClient(open(vn), hVictim, sdo.WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clean.Close() })
	time.Sleep(100 * time.Millisecond) // let the faulty client's last frames and the server's timeout pass
	want := []byte("the node still serves after the monkey")
	if err := clean.Download(tctx, nettest.IdxDomain, 0, want, sdo.UseBlockMode(sdo.BlockAlways)); err != nil {
		t.Fatalf("clean download: %v", err)
	}
	if got, err := clean.Upload(tctx, nettest.IdxDomain, 0); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("clean upload: %q, %v", got, err)
	}
	t.Logf("%d ok, %d failed cleanly; bridge %+v; client %+v", ok, failed, br.Stats(), c.Stats())
}
