// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// cleanError reports whether err is one of the documented failures: an
// abort (either side) or a timeout.
func cleanError(err error) bool {
	var ae *canopen.AbortError
	return errors.As(err, &ae) || errors.Is(err, canopen.ErrTimeout)
}

type chaosResult struct{ ok, failed int }

// runChaos runs n downloads and uploads of random sizes through a chaos
// bridge and checks that each either delivers the right data or fails
// with a clean error.
func runChaos(t *testing.T, cfg chaos.Config, n, maxLen int, opts ...ClientOption) chaosResult {
	t.Helper()
	checkLeaks(t)
	a, b, br := testbus.Chaos(t, cfg)
	d := testDict(t)
	startServer(t, b, d, WithTimeout(60*time.Millisecond))
	opts = append([]ClientOption{WithTimeout(60 * time.Millisecond), WithRetries(3)}, opts...)
	c := newClient(t, a, opts...)
	ctx := ctxT(t)
	rng := rand.New(rand.NewSource(cfg.Seed))
	var res chaosResult
	for i := 0; i < n; i++ {
		want := make([]byte, rng.Intn(maxLen+1))
		rng.Read(want)
		err := c.Download(ctx, idxDomain, 0, want)
		switch {
		case err == nil:
			// The server stores before it confirms.
			if got, _ := d.Get(idxDomain, 0); !bytes.Equal(got, want) {
				t.Fatalf("download %d: success with wrong data (%d bytes stored, %d sent)", i, len(got), len(want))
			}
			res.ok++
		case cleanError(err):
			res.failed++
			t.Logf("download %d (%d bytes): %v", i, len(want), err)
			// Make the stored value known for the upload.
			if err := d.Set(idxDomain, 0, want); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("download %d: unclean error %v", i, err)
		}
		got, err := c.Upload(ctx, idxDomain, 0)
		switch {
		case err == nil:
			if !bytes.Equal(got, want) {
				t.Fatalf("upload %d: success with wrong data (%d bytes, want %d)", i, len(got), len(want))
			}
			res.ok++
		case cleanError(err):
			res.failed++
			t.Logf("upload %d (%d bytes): %v", i, len(want), err)
		default:
			t.Fatalf("upload %d: unclean error %v", i, err)
		}
	}
	t.Logf("%d ok, %d failed cleanly; bridge %+v; client %+v", res.ok, res.failed, br.Stats(), c.Stats())
	return res
}

// TestChaosBlock combines every fault that does not alter frames. Lost
// and repeated acknowledgements can make the sender resume at the wrong
// place; the size and CRC checks turn that into an abort.
func TestChaosBlock(t *testing.T) {
	res := runChaos(t, chaos.Config{Seed: 7, Drop: 0.01, Duplicate: 0.01, Delay: 0.05, DelayBy: 2 * time.Millisecond, Reorder: 0.005},
		12, 2000, WithBlockMode(BlockAlways), WithBlockSize(32))
	if res.ok < 10 {
		t.Errorf("only %d of 24 transfers succeeded", res.ok)
	}
}

func TestChaosBlockDrops(t *testing.T) {
	res := runChaos(t, chaos.Config{Seed: 3, Drop: 0.03}, 10, 1500, WithBlockMode(BlockAlways))
	if res.ok < 8 {
		t.Errorf("only %d of 20 transfers succeeded", res.ok)
	}
}

// TestChaosSegmented uses faults that do not alter frames: segmented
// transfers cannot detect corruption. A repeated frame is a toggle error.
func TestChaosSegmented(t *testing.T) {
	res := runChaos(t, chaos.Config{Seed: 11, Drop: 0.02, Duplicate: 0.005, Delay: 0.05, DelayBy: 2 * time.Millisecond},
		12, 150, WithBlockMode(BlockNever))
	if res.ok < 6 {
		t.Errorf("only %d of 24 transfers succeeded", res.ok)
	}
}

// TestChaosCorruptBlock flips bits: the block CRC and the size checks
// must catch every corruption of a block transfer.
func TestChaosCorruptBlock(t *testing.T) {
	res := runChaos(t, chaos.Config{Seed: 5, Corrupt: 0.003}, 15, 1000, WithBlockMode(BlockAlways))
	if res.ok < 8 {
		t.Errorf("only %d of 30 transfers succeeded", res.ok)
	}
}
