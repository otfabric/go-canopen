// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// events collects callback values.
type events[T any] struct{ ch chan T }

func newEvents[T any]() *events[T] { return &events[T]{ch: make(chan T, 1024)} }

func (e *events[T]) add(v T) {
	select {
	case e.ch <- v:
	default:
	}
}

// wait returns the first value matching pred within d.
func (e *events[T]) wait(t *testing.T, d time.Duration, pred func(T) bool) T {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case v := <-e.ch:
			if pred(v) {
				return v
			}
		case <-timer.C:
			var zero T
			t.Fatalf("no matching event within %v", d)
			return zero
		}
	}
}

// none fails when a value matching pred arrives within d.
func (e *events[T]) none(t *testing.T, d time.Duration, pred func(T) bool) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case v := <-e.ch:
			if pred(v) {
				t.Fatalf("unexpected event %+v", v)
			}
		case <-timer.C:
			return
		}
	}
}

func subscribe(t *testing.T, b *canbus.Bus, opts ...canbus.SubscribeOption) *canbus.Subscription {
	t.Helper()
	s, err := b.Subscribe(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// recv returns the next frame of a subscription matching pred.
func recv(t *testing.T, s *canbus.Subscription, pred func(*canbus.Frame) bool) canbus.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		f, err := s.Receive(ctx)
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if pred == nil || pred(&f) {
			return f
		}
	}
}

func send(t *testing.T, b *canbus.Bus, f canbus.Frame) {
	t.Helper()
	if err := b.Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
