// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// errScriptEnd ends a scripted exchange that went on longer than any
// transfer of the test may.
var errScriptEnd = errors.New("scripted server: response limit reached")

// pacedLink is a scripted server 9 for one transfer. answer gives the
// responses to a request at once; it may set repeat, a response the
// server then sends again every every, without end (up to limit times).
// It records when each request was sent. It runs in a synctest bubble:
// its waits advance the bubble's clock.
type pacedLink struct {
	answer  func(l *pacedLink, m Message) []resp
	pending []resp
	repeat  *resp
	every   time.Duration
	repeats int
	limit   int
	sent    []Message
	at      []time.Time
}

func (l *pacedLink) send(_ context.Context, m Message) error {
	l.sent = append(l.sent, m)
	l.at = append(l.at, time.Now())
	l.pending = append(l.pending, l.answer(l, m)...)
	return nil
}

func (l *pacedLink) recv(ctx context.Context, d time.Duration) (resp, error) {
	if len(l.pending) > 0 {
		r := l.pending[0]
		l.pending = l.pending[1:]
		return r, nil
	}
	wait, repeat := d, l.repeat != nil && l.every <= d
	if repeat {
		wait = l.every
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return resp{}, ctx.Err()
	case <-t.C:
	}
	if !repeat {
		return resp{}, errRecvTimeout
	}
	if l.repeats++; l.repeats > l.limit {
		return resp{}, errScriptEnd
	}
	return *l.repeat, nil
}

func from9(m Message) resp { return resp{from: 9, m: m} }

// linkXfer returns a transfer of idxDomain with server 9 over l.
func linkXfer(l link, timeout time.Duration) *xfer {
	return &xfer{st: &clientStats{}, l: l, ctx: context.Background(), server: 9, dest: 9, sid: 1,
		index: idxDomain, timeout: timeout, tunit: 100 * time.Microsecond}
}

// TestRepeatsDoNotExtendTimeout: a server that, instead of the next
// segment or confirmation, keeps repeating the previous one within each
// response timeout makes the transfer time out one response timeout after
// the request, as a silent server does.
func TestRepeatsDoNotExtendTimeout(t *testing.T) {
	const timeout = time.Second
	seg := make([]byte, SegmentSize)
	for _, tc := range []struct {
		name   string
		answer func(l *pacedLink, m Message) []resp
		run    func(x *xfer) error
	}{
		{"segmented upload", func(l *pacedLink, m Message) []resp {
			switch {
			case m.Cmd == CmdUpload:
				return []resp{from9(Message{Cmd: CmdUploadInit, Index: idxDomain, Size: 200})}
			case m.Cmd == CmdUploadSegment && m.Counter == 1:
				r := from9(Message{Cmd: CmdUploadSegmentData, Counter: 1, Data: seg})
				l.repeat = &r
				return []resp{r}
			}
			return nil
		}, func(x *xfer) error {
			if err := x.send(x.msg(CmdUpload)); err != nil {
				return err
			}
			m, err := x.expect(CmdUploadExpedited, CmdUploadInit)
			if err != nil {
				return x.fail(err)
			}
			_, err = x.uploadFrom(m)
			return err
		}},
		{"segmented download", func(l *pacedLink, m Message) []resp {
			switch {
			case m.Cmd == CmdDownloadInit:
				return []resp{from9(Message{Cmd: CmdDownloadInitOK, Index: idxDomain})}
			case m.Cmd == CmdDownloadSegment && m.Counter == 1:
				r := from9(Message{Cmd: CmdDownloadSegmentOK, Counter: 1})
				l.repeat = &r
				return []resp{r}
			}
			return nil
		}, func(x *xfer) error { return x.downloadSegmented(make([]byte, 200), call{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := &pacedLink{answer: tc.answer, every: timeout * 2 / 5, limit: 50}
				x := linkXfer(l, timeout)
				start := time.Now()
				err := tc.run(x)
				var ae *AbortError
				if !errors.As(err, &ae) || ae.Code != AbortTimeout || !errors.Is(err, canopen.ErrTimeout) {
					t.Fatalf("transfer = %v after %d repeats, want a timeout", err, l.repeats)
				}
				if l.repeats > 2 {
					t.Errorf("%d repeats taken, want at most 2 within one response timeout", l.repeats)
				}
				if d := time.Since(start); d > 2*timeout {
					t.Errorf("transfer ended after %v, want within %v", d, 2*timeout)
				}
			})
		})
	}
}

// TestBulkPaceCapped: a server reporting the largest processing time
// t_proc paces a bulk download at most the response timeout per segment.
func TestBulkPaceCapped(t *testing.T) {
	const timeout = time.Second
	synctest.Test(t, func(t *testing.T) {
		l := &pacedLink{answer: func(_ *pacedLink, m Message) []resp {
			switch m.Cmd {
			case CmdBulkInit:
				return []resp{from9(Message{Cmd: CmdBulkInitOK, Index: idxDomain, TProc: 0xFFFF})}
			case CmdBulkLast:
				return []resp{from9(Message{Cmd: CmdBulkLastOK})}
			default: // the segments are not confirmed
				return nil
			}
		}}
		x := linkXfer(l, timeout)
		if err := x.downloadBulk(make([]byte, 4*SegmentSize), call{}); err != nil {
			t.Fatal(err)
		}
		checkPace(t, l.sent, l.at, timeout)
	})
}

// checkPace checks the time between the bulk segments sent.
func checkPace(t *testing.T, sent []Message, at []time.Time, want time.Duration) {
	t.Helper()
	var segs []time.Time
	for i, m := range sent {
		if m.Cmd == CmdBulkSegment || m.Cmd == CmdBulkLast {
			segs = append(segs, at[i])
		}
	}
	if len(segs) < 2 {
		t.Fatalf("%d bulk segments sent", len(segs))
	}
	for i := 1; i < len(segs); i++ {
		if d := segs[i].Sub(segs[i-1]); d != want {
			t.Errorf("segment %d sent %v after the previous one, want %v", i+1, d, want)
		}
	}
}

// TestBulkPaceCappedMulti: in DownloadMulti, a participant reporting the
// largest t_proc paces the segments at most the response timeout apart.
func TestBulkPaceCappedMulti(t *testing.T) {
	const timeout = time.Second
	synctest.Test(t, func(t *testing.T) {
		ports := fdPorts(t, 2)
		c := startClient(t, ports[0], 1, WithTimeout(timeout))
		sub, err := ports[1].Subscribe(canbus.WithFilters(filter(canopen.FuncSDORx)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sub.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var sent []Message
		var at []time.Time
		served := make(chan struct{})
		go func() { // server 9
			defer close(served)
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				m, err := Decode(f.Payload())
				if err != nil {
					continue
				}
				sent, at = append(sent, m), append(at, time.Now())
				var r Message
				switch m.Cmd {
				case CmdBulkInit:
					r = Message{Cmd: CmdBulkInitOK, Index: idxDomain, TProc: 0xFFFF}
				case CmdBulkLast:
					r = Message{Cmd: CmdBulkLastOK}
				default:
					continue
				}
				r.Dest, r.Session = 1, m.Session
				b, _ := r.Encode()
				out, _ := canbus.NewFD(canopen.FuncSDOTx+9, false, false, canbus.PadFD(b, 0))
				_ = ports[1].Send(ctx, &out)
			}
		}()
		res, err := c.DownloadMulti(context.Background(), 9, idxDomain, 0, make([]byte, 4*SegmentSize), Expect(9))
		if err != nil || res[9] != nil {
			t.Fatalf("DownloadMulti = %v, %v", res, err)
		}
		cancel()
		<-served
		checkPace(t, sent, at, timeout)
	})
}
