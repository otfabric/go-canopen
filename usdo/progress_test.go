// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// pacedLink is a scripted server 9 for one transfer. answer gives the
// responses to a request at once. It records when each request was sent.
// It runs in a synctest bubble: its waits advance the bubble's clock.
type pacedLink struct {
	answer  func(m Message) []resp
	pending []resp
	sent    []Message
	at      []time.Time
}

func (l *pacedLink) send(_ context.Context, m Message) error {
	l.sent = append(l.sent, m)
	l.at = append(l.at, time.Now())
	l.pending = append(l.pending, l.answer(m)...)
	return nil
}

func (l *pacedLink) recv(ctx context.Context, d time.Duration) (resp, error) {
	if len(l.pending) > 0 {
		r := l.pending[0]
		l.pending = l.pending[1:]
		return r, nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return resp{}, ctx.Err()
	case <-t.C:
	}
	return resp{}, errRecvTimeout
}

func from9(m Message) resp { return resp{from: 9, m: m} }

// linkXfer returns a transfer of idxDomain with server 9 over l.
func linkXfer(l link, timeout time.Duration) *xfer {
	return &xfer{st: &clientStats{}, l: l, ctx: context.Background(), server: 9, dest: 9, sid: 1,
		index: idxDomain, timeout: timeout, tunit: 100 * time.Microsecond}
}

// TestBulkPaceCapped: a server reporting the largest processing time
// t_proc paces a bulk download at most the response timeout per segment.
func TestBulkPaceCapped(t *testing.T) {
	const timeout = time.Second
	synctest.Test(t, func(t *testing.T) {
		l := &pacedLink{answer: func(m Message) []resp {
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
