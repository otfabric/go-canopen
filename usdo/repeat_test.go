// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
)

// repeatLink is a link to a peer that answers every request with
// next(n), n counting the requests sent; it records the waits of recv.
type repeatLink struct {
	mu    sync.Mutex
	sent  []Message
	waits []time.Duration
	next  func(n int) Message
}

func (l *repeatLink) send(_ context.Context, m Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, m)
	return nil
}

func (l *repeatLink) recv(ctx context.Context, d time.Duration) (resp, error) {
	l.mu.Lock()
	l.waits = append(l.waits, d)
	n := len(l.sent)
	l.mu.Unlock()
	t := time.NewTimer(min(d, time.Millisecond))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return resp{}, ctx.Err()
	case <-t.C:
	}
	return resp{from: 5, m: l.next(n)}, nil
}

// runRepeat runs f with a fake peer and fails when it does not end.
func runRepeat(t *testing.T, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("transfer did not time out while the peer repeated")
	}
	return nil
}

// checkRepeatTimeout checks that a transfer to a peer repeating an earlier
// response ended with a timeout abort, and that the waits of the repeating
// step shrank: one deadline for the step, not one per repeat.
func checkRepeatTimeout(t *testing.T, l *repeatLink, err error, step int) {
	t.Helper()
	var ae *AbortError
	if !errors.As(err, &ae) || ae.Code != AbortTimeout || ae.Remote {
		t.Fatalf("err = %v, want local AbortTimeout", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if last := l.sent[len(l.sent)-1]; last.Cmd != CmdAbort || last.Abort != AbortTimeout {
		t.Fatalf("last sent %+v, want timeout abort", last)
	}
	// waits[0..step-1] are the steps that made progress; the rest belong to
	// the repeating step.
	w := l.waits[step:]
	if len(w) < 2 {
		t.Fatalf("waits %v: the peer was not repeated", l.waits)
	}
	for i := 1; i < len(w); i++ {
		if w[i] >= w[i-1] {
			t.Fatalf("wait %d = %v after %v: a repeat extended the deadline", i, w[i], w[i-1])
		}
	}
}

func TestUploadRepeatedSegmentTimesOut(t *testing.T) {
	seg1 := Message{Dest: 1, Cmd: CmdUploadSegmentData, Session: 1, Counter: 1, Data: pattern(SegmentSize, 1)}
	l := &repeatLink{next: func(int) Message { return seg1 }}
	x := &xfer{st: &clientStats{}, l: l, ctx: tctx(t), server: 5, dest: 5, sid: 1, index: idxDomain,
		timeout: 20 * time.Millisecond}
	err := runRepeat(t, func() error {
		_, err := x.uploadFrom(Message{Dest: 1, Cmd: CmdUploadInit, Session: 1, Index: idxDomain, Size: 3 * SegmentSize})
		return err
	})
	checkRepeatTimeout(t, l, err, 1)
	if x.st.ignored.Load() == 0 {
		t.Error("repeats not counted as ignored")
	}
}

func TestDownloadRepeatedConfirmationTimesOut(t *testing.T) {
	l := &repeatLink{next: func(n int) Message {
		if n == 1 {
			return Message{Dest: 1, Cmd: CmdDownloadInitOK, Session: 1, Index: idxDomain}
		}
		return Message{Dest: 1, Cmd: CmdDownloadSegmentOK, Session: 1, Counter: 1}
	}}
	x := &xfer{st: &clientStats{}, l: l, ctx: tctx(t), server: 5, dest: 5, sid: 1, index: idxDomain,
		timeout: 20 * time.Millisecond}
	err := runRepeat(t, func() error { return x.downloadSegmented(pattern(4*SegmentSize, 2), call{}) })
	checkRepeatTimeout(t, l, err, 2)
}

// blockBus is a bus whose Send blocks until its context ends.
type blockBus struct {
	mu   sync.Mutex
	errs []error // ctx.Err() at the start of each Send
}

func (b *blockBus) Send(ctx context.Context, _ *canbus.Frame) error {
	b.mu.Lock()
	b.errs = append(b.errs, ctx.Err())
	b.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (b *blockBus) Subscribe(...canbus.SubscribeOption) (*canbus.Subscription, error) {
	return nil, errors.New("not supported")
}

// TestServerExpireUsesServiceContext checks that the timeout aborts of
// expired sessions are sent with the service's context, so a stopped
// server does not wait for a blocked bus.
func TestServerExpireUsesServiceContext(t *testing.T) {
	b := &blockBus{}
	s, err := NewServer(b, device(t, 5), WithNodeID(5))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Second)
	s.sessions[sessKey{client: 1, id: 1}] = &session{kind: kindDownload, index: idxDomain, deadline: past}
	s.sessions[sessKey{client: 2, id: 1}] = &session{kind: kindUpload, index: idxDomain, deadline: past}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if next := s.expire(ctx); !next.IsZero() {
		t.Errorf("next = %v, want none", next)
	}
	if len(s.sessions) != 0 {
		t.Errorf("%d sessions left", len(s.sessions))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) != 2 {
		t.Fatalf("%d sends, want 2", len(b.errs))
	}
	for _, e := range b.errs {
		if !errors.Is(e, context.Canceled) {
			t.Errorf("abort sent with a live context (%v)", e)
		}
	}
	if st := s.Stats(); st.Timeouts != 2 || st.SendErrors != 2 {
		t.Errorf("%+v", st)
	}
}
