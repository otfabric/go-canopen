// SPDX-License-Identifier: MIT

package master_test

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
)

// iterations is the number of seeds the monkey runs:
// CANOPEN_CHAOS_ITERATIONS overrides the default (make chaos runs many).
func iterations(def int) int {
	if n, err := strconv.Atoi(os.Getenv("CANOPEN_CHAOS_ITERATIONS")); err == nil && n > 0 {
		return n
	}
	return def
}

// physicalFaults returns a fault mix a CAN bus can produce for the
// receivers of a sender: lost, repeated (a retransmission after a
// missing acknowledgement) and late frames. Corruption is left out (a
// real bus rejects it by CRC, and segmented SDO has no checksum) and so
// is reordering (a bus keeps each sender's order).
func physicalFaults(r *rand.Rand) chaos.Config {
	return chaos.Config{
		Seed:      r.Int63(),
		Drop:      r.Float64() * 0.05,
		Duplicate: r.Float64() * 0.05,
		Delay:     r.Float64() * 0.05,
		DelayBy:   time.Duration(r.Intn(3)+1) * time.Millisecond,
	}
}

// cleanErr reports the documented failures of a transfer over a faulty
// bus: an abort (either side) or a timeout.
func cleanErr(err error) bool {
	var ae *canopen.AbortError
	return errors.As(err, &ae) || errors.Is(err, canopen.ErrTimeout)
}

// retry runs f until it succeeds, failing the test on an unclean error
// or after n attempts.
func retry(t *testing.T, what string, n int, f func() error) {
	t.Helper()
	var err error
	for i := 0; i < n; i++ {
		if err = f(); err == nil {
			return
		}
		if !cleanErr(err) {
			t.Fatalf("%s: unclean error %v", what, err)
		}
	}
	t.Fatalf("%s: %d attempts failed, last: %v", what, n, err)
}

// TestMonkeyMasterNode runs a master and a node through random physical
// faults: SDO traffic, PDO configuration, NMT state changes, heartbeat
// supervision, PDO traffic and a reset. It asserts that nothing succeeds with wrong
// data, that the node only takes commanded states, that no PDO crosses
// while the node is not operational, and that everything shuts down
// without leaking goroutines.
func TestMonkeyMasterNode(t *testing.T) {
	seeds := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < iterations(5); i++ {
		cfg := physicalFaults(seeds)
		t.Run(fmt.Sprintf("seed%d", cfg.Seed), func(t *testing.T) {
			nettest.CheckLeaks(t)
			monkeyRun(t, cfg)
		})
	}
}

func monkeyRun(t *testing.T, cfg chaos.Config) {
	a, b, br := testbus.Chaos(t, cfg)
	r := newRigOn(t, []*canbus.Bus{a}, master.WithSDOOptions(sdo.WithTimeout(60*time.Millisecond), sdo.WithRetries(3)))
	ctx := nettest.Ctx(t)
	rng := rand.New(rand.NewSource(cfg.Seed))
	d := nettest.Dict(t, 5, func(c *od.Config) { c.HeartbeatMs = 10 })

	var mu sync.Mutex
	var states []canopen.NMTState
	n := startNode(t, b, d, node.WithStateHandler(func(sc nmt.StateChange) {
		mu.Lock()
		states = append(states, sc.To)
		mu.Unlock()
	}))
	if err := r.m.Monitor(5, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	c, err := r.m.SDO(5)
	if err != nil {
		t.Fatal(err)
	}

	// SDO: every success is the truth.
	for i := 0; i < 20; i++ {
		v := rng.Uint32()
		err := c.WriteUint32(ctx, nettest.IdxParam, 0, v)
		switch {
		case err == nil:
			if got, _ := d.GetUint(nettest.IdxParam, 0); uint32(got) != v {
				t.Fatalf("write %x succeeded, node has %x", v, got)
			}
		case !cleanErr(err):
			t.Fatalf("write: unclean error %v", err)
		}
		// A failed write may still land: a delayed or duplicated request
		// can reach the node during the read. The read must return the
		// node's value from before or after it.
		want, _ := d.GetUint(nettest.IdxParam, 0)
		got, err := c.ReadUint(ctx, nettest.IdxParam, 0)
		after, _ := d.GetUint(nettest.IdxParam, 0)
		switch {
		case err == nil:
			if got != want && got != after {
				t.Fatalf("read %x, node has %x (before the read) and %x (after)", got, want, after)
			}
		case !cleanErr(err):
			t.Fatalf("read: unclean error %v", err)
		}
	}
	// A segmented transfer.
	blob := make([]byte, 40+rng.Intn(200))
	rng.Read(blob)
	if err := c.Download(ctx, nettest.IdxDomain, 0, blob); err == nil {
		if got, _ := d.Get(nettest.IdxDomain, 0); string(got) != string(blob) {
			t.Fatal("segmented download succeeded with wrong data")
		}
	} else if !cleanErr(err) {
		t.Fatalf("segmented download: %v", err)
	}

	// PDO configuration, retried as a whole.
	pc := pdo.Config{COBID: 0x185, TransmissionType: 255, EventTimer: 20, Mapping: pdo.Mapping{{Index: nettest.IdxIn32, Bits: 32}}}
	retry(t, "configure TPDO 1", 20, func() error { return r.m.ConfigurePDO(ctx, 5, true, 1, pc) })
	if got, _ := pdo.Load(d, true, 1); got.COBID != pc.COBID || got.EventTimer != 20 || len(got.Mapping) != 1 || got.Mapping[0] != pc.Mapping[0] {
		t.Fatalf("TPDO 1 configured as %+v", got)
	}

	// PDO traffic: only values that were set may arrive.
	var pmu sync.Mutex
	set := map[int64]bool{0: true}
	var last int64
	var received, afterStop int
	stopped := false
	l, err := r.m.Listen(pc.COBID, pc.Mapping, d, func(_ *canbus.Frame, vals []pdo.Value) {
		v := vals[0].Value.(int64)
		pmu.Lock()
		defer pmu.Unlock()
		if !set[v] {
			t.Errorf("TPDO carried %d, never set", v)
		}
		last = v
		received++
		if stopped {
			afterStop++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = l
	nmtUntil(t, r, n, canopen.NMTStart, canopen.StateOperational)
	var final int64
	for i := 0; i < 30; i++ {
		final = int64(rng.Int31())
		pmu.Lock()
		set[final] = true
		pmu.Unlock()
		_ = d.SetValue(nettest.IdxIn32, 0, int32(final))
		time.Sleep(time.Millisecond)
	}
	nettest.Eventually(t, 5*time.Second, "last value (event timer)", func() bool {
		pmu.Lock()
		defer pmu.Unlock()
		return last == final
	})

	// Stopped: no PDO after the frames in flight.
	nmtUntil(t, r, n, canopen.NMTStop, canopen.StateStopped)
	time.Sleep(30 * time.Millisecond)
	pmu.Lock()
	stopped = true
	pmu.Unlock()
	time.Sleep(80 * time.Millisecond)
	pmu.Lock()
	if afterStop != 0 {
		t.Fatalf("%d TPDOs while stopped", afterStop)
	}
	pmu.Unlock()

	// A reset communication (repeated until it happens) and SDO after it.
	for i := 0; n.Stats().Resets == 0; i++ {
		if i == 50 {
			t.Fatal("node never reset")
		}
		_ = r.m.ResetCommunication(ctx, 5)
		time.Sleep(20 * time.Millisecond)
	}
	waitState(t, n, canopen.StatePreOperational)
	retry(t, "read after reset", 20, func() error {
		v, err := c.ReadUint(ctx, od.IndexDeviceType, 0)
		if err == nil && uint32(v) != nettest.DeviceType {
			t.Fatalf("device type %x", v)
		}
		return err
	})

	mu.Lock()
	for _, s := range states {
		switch s {
		case canopen.StateInitialising, canopen.StatePreOperational, canopen.StateOperational, canopen.StateStopped:
		default:
			t.Errorf("node took state %s", s)
		}
	}
	mu.Unlock()
	if _, lost := r.rec.find(kind(master.EventHeartbeatLost, 5)); lost {
		t.Error("heartbeat lost with at most 5 % of the frames dropped")
	}
	t.Logf("bridge %+v; %d TPDOs; SDO %+v", br.Stats(), received, r.m.Stats().SDO[5])
}

// nmtUntil repeats an NMT command (unconfirmed, so possibly lost) until the
// node reports the state.
func nmtUntil(t *testing.T, r *rig, n *node.Node, cmd canopen.NMTCommand, st canopen.NMTState) {
	t.Helper()
	ctx := nettest.Ctx(t)
	for i := 0; i < 50; i++ {
		if err := r.m.NMT(ctx, 5, cmd); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(50 * time.Millisecond)
		for time.Now().Before(deadline) {
			if n.State() == st {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatalf("node never reached %s", st)
}
