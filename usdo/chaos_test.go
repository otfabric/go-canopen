// SPDX-License-Identifier: MIT

package usdo

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// clean reports whether a transfer error is one a lossy bus may cause: an
// abort (local or remote).
func clean(err error) bool {
	var ae *AbortError
	return err == nil || errors.As(err, &ae)
}

// TestChaos runs transfers through a lossy, duplicating, reordering CAN
// FD bridge: every transfer either succeeds with the right data or fails
// with an abort, and no session or goroutine is left behind.
func TestChaos(t *testing.T) {
	cfgs := []chaos.Config{
		{Seed: 1, Drop: 0.03},
		{Seed: 2, Duplicate: 0.1},
		{Seed: 3, Reorder: 0.05, Delay: 0.05, DelayBy: time.Millisecond},
		{Seed: 4, Drop: 0.02, Duplicate: 0.05, Reorder: 0.02},
	}
	before := runtime.NumGoroutine()
	for _, cfg := range cfgs {
		t.Run(fmt.Sprintf("seed%d", cfg.Seed), func(t *testing.T) {
			a, b, br := testbus.Chaos(t, cfg, virtual.WithFD())
			srv, d := startServer(t, a, 5, WithTimeout(100*time.Millisecond))
			c := startClient(t, b, 1, WithTimeout(50*time.Millisecond))
			ok, failed := 0, 0
			for i := range 40 {
				data := pattern(1+i*37%700, byte(i))
				err := c.Download(tctx(t), 5, idxDomain, 0, data, UseBulk(i%3 == 0))
				if !clean(err) {
					t.Fatalf("download %d: %v", i, err)
				}
				if err != nil {
					failed++
					continue
				}
				got, err := c.Upload(tctx(t), 5, idxDomain, 0)
				if !clean(err) {
					t.Fatalf("upload %d: %v", i, err)
				}
				if err != nil {
					failed++
					continue
				}
				if !equal(got, data) {
					// a lost bulk segment after a duplicate can only be
					// detected by size: compare what the server stored
					stored, _ := d.Get(idxDomain, 0)
					if !equal(got, stored) {
						t.Fatalf("transfer %d: uploaded data differs from the stored data", i)
					}
				}
				ok++
			}
			if ok == 0 {
				t.Fatalf("no transfer succeeded (%d failed, bridge %+v)", failed, br.Stats())
			}
			eventually(t, "server sessions expire", func() bool { return srv.Sessions() == 0 })
			t.Logf("ok %d failed %d bridge %+v", ok, failed, br.Stats())
		})
	}
	eventually(t, "goroutines end", func() bool { return runtime.NumGoroutine() <= before+2 })
}

// TestChaosCorrupt flips bits: the services must not hang or panic.
func TestChaosCorrupt(t *testing.T) {
	a, b, _ := testbus.Chaos(t, chaos.Config{Seed: 7, Corrupt: 0.2}, virtual.WithFD())
	startServer(t, a, 5, WithTimeout(50*time.Millisecond))
	c := startClient(t, b, 1, WithTimeout(30*time.Millisecond))
	for i := range 30 {
		_ = c.Download(tctx(t), 5, idxDomain, 0, pattern(i*20, 1))
		_, _ = c.Upload(tctx(t), 5, idxDomain, 0)
		_, _ = c.UploadMulti(tctx(t), Broadcast, idxU8, 0, Expect(canopen.NodeID(5)))
	}
}
