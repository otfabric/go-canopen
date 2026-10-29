// SPDX-License-Identifier: MIT

package timestamp

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
