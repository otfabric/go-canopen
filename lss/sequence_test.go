// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// TestConfigureIsOneSequence: while one Configure has its slave selected
// and is inside its function, a Configure of another goroutine does not
// run; it runs once the first has returned. Each slave gets the node-ID
// its own Configure set.
func TestConfigureIsOneSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := testbus.Ports(t, 3)
		m := startMaster(t, ports[0])
		addrA := Address{Vendor: 0x29C, Product: 1, Revision: 1, Serial: 0xA}
		addrB := Address{Vendor: 0x29C, Product: 1, Revision: 1, Serial: 0xB}
		a := startSlave(t, ports[1], addrA, Unconfigured)
		b := startSlave(t, ports[2], addrB, Unconfigured)
		ctx := context.Background()

		inFirst, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release() // also when the test fails
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		secondRan := false
		go func() {
			firstDone <- m.Configure(ctx, addrA, func(ctx context.Context) error {
				close(inFirst)
				<-held
				return m.SetNodeID(ctx, 5)
			})
		}()
		<-inFirst
		go func() {
			secondDone <- m.Configure(ctx, addrB, func(ctx context.Context) error {
				secondRan = true
				return m.SetNodeID(ctx, 6)
			})
		}()
		// Everything is at rest: the first Configure in its function, the
		// second waiting for it.
		synctest.Wait()
		if secondRan {
			t.Fatal("the second Configure ran its function while the first was inside its own")
		}
		if st := a.State(); st != Configuration {
			t.Fatalf("slave A in state %v while its Configure runs, want Configuration", st)
		}
		if st := b.State(); st != Waiting {
			t.Fatalf("slave B in state %v while the Configure of A runs, want Waiting", st)
		}
		select {
		case err := <-secondDone:
			t.Fatalf("the second Configure returned (%v) before the first", err)
		default:
		}

		release()
		if err := <-firstDone; err != nil {
			t.Fatalf("first Configure: %v", err)
		}
		if err := <-secondDone; err != nil {
			t.Fatalf("second Configure: %v", err)
		}
		if got := a.Pending().NodeID; got != 5 {
			t.Errorf("slave A: pending node-ID %d, want 5", got)
		}
		if got := b.Pending().NodeID; got != 6 {
			t.Errorf("slave B: pending node-ID %d, want 6", got)
		}
	})
}

// TestSequenceRules: a single service waits for a sequence in progress
// until its context ends; the services a sequence's function calls belong
// to the sequence, whether they take the context of the function or run
// on its goroutine; a sequence inside a sequence is part of it.
func TestSequenceRules(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ports := testbus.Ports(t, 2)
		m := startMaster(t, ports[0])
		addr := Address{Vendor: 0x29C, Product: 1, Revision: 1, Serial: 0xA}
		s := startSlave(t, ports[1], addr, Unconfigured)
		outer := context.Background()

		var first context.Context // the context of the first sequence
		err := m.Configure(outer, addr, func(ctx context.Context) error {
			first = ctx
			// Another goroutine's service waits, and gives up with its
			// context.
			wctx, cancel := context.WithTimeout(outer, time.Second)
			defer cancel()
			waited := make(chan error, 1)
			go func() { _, err := m.InquireNodeID(wctx); waited <- err }()
			if err := <-waited; !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("service of another goroutine during a sequence = %v, want its context's error", err)
			}
			// With the function's context, on another goroutine.
			inq := make(chan error, 1)
			go func() { _, err := m.InquireNodeID(ctx); inq <- err }()
			if err := <-inq; err != nil {
				t.Errorf("service with the sequence's context: %v", err)
			}
			// On the function's goroutine, with a context of the caller's
			// own: a sequence (InquireAddress) and a single service.
			if got, err := m.InquireAddress(outer); err != nil || got != addr {
				t.Errorf("InquireAddress inside Configure = %v, %v", got, err)
			}
			return m.SetNodeID(outer, 7)
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Pending().NodeID; got != 7 {
			t.Errorf("pending node-ID %d, want 7", got)
		}
		// The sequence has ended: its context no longer belongs to one.
		if id, err := m.InquireNodeID(outer); !errors.Is(err, canopen.ErrTimeout) {
			t.Errorf("InquireNodeID with no slave selected = %d, %v, want ErrTimeout", id, err)
		}

		// A sequence waits like a single service does.
		busy, hold, ended := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			ended <- m.Configure(outer, addr, func(context.Context) error { close(busy); <-hold; return nil })
		}()
		<-busy
		wctx, cancel := context.WithTimeout(outer, time.Second)
		defer cancel()
		if _, err := m.InquireAddress(wctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("InquireAddress during another sequence = %v, want its context's error", err)
		}
		if _, err := m.AssignAll(wctx, 1); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("AssignAll during another sequence = %v, want its context's error", err)
		}
		if err := m.Configure(wctx, addr, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Configure during another sequence = %v, want its context's error", err)
		}
		// The context of a sequence that has ended belongs to no other.
		stale, cancelStale := context.WithTimeout(first, time.Second)
		defer cancelStale()
		staleErr := make(chan error, 1)
		go func() { _, err := m.InquireNodeID(stale); staleErr <- err }()
		if err := <-staleErr; !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("service with the context of an earlier sequence = %v, want it to wait for the running one", err)
		}
		close(hold)
		if err := <-ended; err != nil {
			t.Errorf("the sequence that was held: %v", err)
		}
	})
}
