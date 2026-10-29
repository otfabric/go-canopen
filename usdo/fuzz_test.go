// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func FuzzDecode(f *testing.F) {
	for _, m := range []Message{
		{Dest: 1, Cmd: CmdUpload, Session: 1, Index: 0x1000, SubIndex: 1},
		{Dest: 1, Cmd: CmdDownloadExpedited, Session: 1, Index: 0x2000, Data: []byte{1, 2}},
		{Dest: 1, Cmd: CmdBulkInit, Session: 1, Size: 1000},
		{Dest: 1, Cmd: CmdBulkSegment, Session: 1, Counter: 1, Data: make([]byte, SegmentSize)},
		{Dest: 1, Cmd: CmdBulkLast, Session: 1, Data: []byte{1}},
		{Dest: 1, Cmd: CmdAbort, Session: 1, Abort: AbortTimeout},
		{Dest: 1, Cmd: CmdBulkInitOK, Session: 1, TProc: 10},
	} {
		b, _ := m.Encode()
		f.Add(b)
	}
	f.Add([]byte{1, 0x91, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Decode(data)
		if err != nil || m.Cmd.Remote() {
			return
		}
		b, err := m.Encode()
		if err != nil {
			t.Fatalf("decoded %+v does not encode: %v", m, err)
		}
		m2, err := Decode(b)
		if err != nil || m2.String() != m.String() || !equal(m2.Data, m.Data) || m2.Size != m.Size {
			t.Fatalf("re-decode %+v != %+v (%v)", m2, m, err)
		}
	})
}

// framesOf splits fuzz data into request frames: a length byte, a client
// node-ID byte and the payload.
func framesOf(data []byte) []canbus.Frame {
	var out []canbus.Frame
	for len(data) >= 2 {
		n := min(int(data[0])%65, len(data)-2)
		f, err := canbus.NewFD(0x600+canbus.ID(data[1]&0x7F), false, false, canbus.PadFD(data[2:2+n], 0))
		if err == nil {
			out = append(out, f)
		}
		data = data[2+n:]
	}
	return out
}

func serverSeed(msgs ...Message) []byte {
	var out []byte
	for _, m := range msgs {
		b, _ := m.Encode()
		out = append(out, byte(len(b)), 1)
		out = append(out, b...)
	}
	return out
}

func FuzzServer(f *testing.F) {
	seg := make([]byte, SegmentSize)
	f.Add(serverSeed(Message{Dest: 5, Cmd: CmdUpload, Session: 1, Index: idxStr}))
	f.Add(serverSeed(
		Message{Dest: 5, Cmd: CmdDownloadInit, Session: 1, Index: idxDomain, Size: 70},
		Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 1, Counter: 1, Data: seg},
		Message{Dest: 5, Cmd: CmdDownloadLast, Session: 1, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}))
	f.Add(serverSeed(
		Message{Dest: 0, Cmd: CmdBulkInit, Session: 1, Index: idxDomain, Size: 61},
		Message{Dest: 0, Cmd: CmdBulkSegment, Session: 1, Counter: 1, Data: seg},
		Message{Dest: 0, Cmd: CmdBulkLast, Session: 1, Data: []byte{1}},
		Message{Dest: 5, Cmd: CmdUpload, Session: 2, Index: idxDomain},
		Message{Dest: 5, Cmd: CmdUploadSegment, Session: 2, Counter: 1},
		Message{Dest: 5, Cmd: CmdUploadSegment, Session: 2, Counter: 2}))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := NewServer(nopBus{}, device(t, 5), WithMaxSessions(4), WithMaxSize(4096))
		if err != nil {
			t.Fatal(err)
		}
		_ = s.SetNodeID(5)
		for _, fr := range framesOf(data) {
			for _, r := range s.handle(&fr) {
				if _, err := Frame(r.m, 5, true, false); err != nil {
					t.Fatalf("response %+v does not encode: %v", r.m, err)
				}
				if r.m.Dest != Address(r.to) {
					t.Fatalf("response to %d addressed to %v", r.to, r.m.Dest)
				}
			}
		}
		if s.Sessions() != len(s.sessions) || s.Sessions() > 4 {
			t.Fatalf("session count %d / %d", s.Sessions(), len(s.sessions))
		}
	})
}

// nopBus is a bus that accepts every frame and receives nothing.
type nopBus struct{}

func (nopBus) Send(context.Context, *canbus.Frame) error { return nil }
func (nopBus) Subscribe(...canbus.SubscribeOption) (*canbus.Subscription, error) {
	return nil, errors.New("nop bus")
}

// scriptLink plays scripted responses of server 9.
type scriptLink struct {
	resps []resp
	sent  int
	fail  int // fail the nth send (1-based); 0: never
}

func (l *scriptLink) send(context.Context, Message) error {
	l.sent++
	if l.sent == l.fail {
		return errors.New("send failed")
	}
	return nil
}

func (l *scriptLink) recv(ctx context.Context, _ time.Duration) (resp, error) {
	if ctx.Err() != nil {
		return resp{}, ctx.Err()
	}
	if len(l.resps) == 0 {
		return resp{}, errRecvTimeout
	}
	r := l.resps[0]
	l.resps = l.resps[1:]
	return r, nil
}

// fuzzClient runs a transfer chosen by sel against responses decoded from
// data (a length byte, a sender byte, the payload, repeated).
func fuzzClient(sel byte, data []byte) error {
	var rs []resp
	for _, fr := range framesOf(data) {
		m, err := Decode(fr.Payload())
		if err != nil || m.Cmd.Remote() {
			continue
		}
		rs = append(rs, resp{from: canopen.NodeID(fr.ID & 0x7F), m: m})
	}
	l := &scriptLink{resps: rs}
	x := &xfer{st: &clientStats{}, l: l, ctx: context.Background(), server: 9, dest: 9, sid: 1,
		index: idxDomain, timeout: time.Second, tunit: time.Nanosecond}
	switch sel % 4 {
	case 0:
		if err := x.send(x.msg(CmdUpload)); err != nil {
			return err
		}
		m, err := x.expect(CmdUploadExpedited, CmdUploadInit)
		if err != nil {
			return x.fail(err)
		}
		_, err = x.uploadFrom(m)
		return err
	case 1:
		return x.downloadExpedited([]byte{1, 2}, call{})
	case 2:
		return x.downloadSegmented(make([]byte, 200), call{})
	}
	return x.downloadBulk(make([]byte, 200), call{})
}

func clientSeed(msgs ...Message) []byte {
	var out []byte
	for _, m := range msgs {
		m.Dest, m.Session = 1, 1
		b, _ := m.Encode()
		out = append(out, byte(len(b)), 9)
		out = append(out, b...)
	}
	return out
}

func clientSeeds() []struct {
	sel  byte
	data []byte
} {
	seg := make([]byte, SegmentSize)
	return []struct {
		sel  byte
		data []byte
	}{
		{0, clientSeed(Message{Cmd: CmdUploadExpedited, Index: idxDomain, Data: []byte{1}})},
		{0, clientSeed(Message{Cmd: CmdUploadInit, Index: idxDomain, Size: 61},
			Message{Cmd: CmdUploadSegmentData, Counter: 1, Data: seg},
			Message{Cmd: CmdUploadSegmentData, Counter: 1, Data: seg},
			Message{Cmd: CmdUploadLast, Data: []byte{1}})},
		{0, clientSeed(Message{Cmd: CmdAbort, Abort: AbortNoObject})},
		{1, clientSeed(Message{Cmd: CmdDownloadExpeditedOK, Index: idxDomain})},
		{1, clientSeed(Message{Cmd: CmdBulkLastOK}, Message{Cmd: CmdDownloadExpeditedOK, Index: 1})},
		{2, clientSeed(Message{Cmd: CmdDownloadInitOK, Index: idxDomain},
			Message{Cmd: CmdDownloadSegmentOK, Counter: 1},
			Message{Cmd: CmdDownloadSegmentOK, Counter: 2},
			Message{Cmd: CmdDownloadSegmentOK, Counter: 2},
			Message{Cmd: CmdDownloadSegmentOK, Counter: 3},
			Message{Cmd: CmdDownloadLastOK})},
		{2, clientSeed(Message{Cmd: CmdDownloadInitOK, Index: 1})},
		{3, clientSeed(Message{Cmd: CmdBulkInitOK, Index: idxDomain, TProc: 1}, Message{Cmd: CmdBulkLastOK})},
		{3, clientSeed(Message{Cmd: CmdBulkInitOK, Index: 1})},
		{3, clientSeed(Message{Cmd: CmdBulkInitOK, Index: idxDomain})},
	}
}

func FuzzClient(f *testing.F) {
	for _, s := range clientSeeds() {
		f.Add(s.sel, s.data)
	}
	f.Fuzz(func(t *testing.T, sel byte, data []byte) {
		err := fuzzClient(sel, data)
		var ae *AbortError
		if err != nil && !errors.As(err, &ae) && !errors.Is(err, canopen.ErrProtocol) {
			t.Fatalf("unexpected error %v", err)
		}
	})
}

// TestFuzzClientSeeds runs the client seeds and checks their outcomes.
func TestFuzzClientSeeds(t *testing.T) {
	want := []bool{true, true, false, true, false, true, false, true, false, false}
	for i, s := range clientSeeds() {
		err := fuzzClient(s.sel, s.data)
		if (err == nil) != want[i] {
			t.Errorf("seed %d: %v", i, err)
		}
	}
}

// TestSendFailures checks that send errors end transfers cleanly.
func TestSendFailures(t *testing.T) {
	for fail := 1; fail <= 3; fail++ {
		for sel := range 4 {
			l := &scriptLink{fail: fail, resps: []resp{
				{9, Message{Dest: 1, Cmd: CmdDownloadInitOK, Session: 1, Index: idxDomain}},
				{9, Message{Dest: 1, Cmd: CmdDownloadSegmentOK, Session: 1, Counter: 1}},
				{9, Message{Dest: 1, Cmd: CmdDownloadSegmentOK, Session: 1, Counter: 2}},
			}}
			if sel == 3 {
				l.resps = []resp{{9, Message{Dest: 1, Cmd: CmdBulkInitOK, Session: 1, Index: idxDomain}}}
			}
			if sel == 0 {
				l.resps = []resp{{9, Message{Dest: 1, Cmd: CmdUploadInit, Session: 1, Index: idxDomain, Size: 200}}}
			}
			x := &xfer{st: &clientStats{}, l: l, ctx: context.Background(), server: 9, dest: 9, sid: 1, index: idxDomain, timeout: time.Second}
			var err error
			switch sel {
			case 0:
				_, err = x.uploadFrom(l.resps[0].m)
			case 1:
				err = x.downloadExpedited([]byte{1}, call{})
			case 2:
				err = x.downloadSegmented(make([]byte, 200), call{})
			case 3:
				err = x.downloadBulk(make([]byte, 200), call{})
			}
			if fail <= 1 && err == nil {
				t.Errorf("fail %d sel %d: no error", fail, sel)
			}
		}
	}
	// a bulk cancelled while pacing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sendBulk(ctx, &scriptLink{}, 1, 1, make([]byte, 200), time.Hour); !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	// a failing bus
	c, _ := NewClient(failBus{fdPorts(t, 1)[0]}, 1)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Download(tctx(t), 5, idxU8, 0, []byte{1}); err == nil {
		t.Error("send failure ignored")
	}
	if _, err := c.DownloadMulti(tctx(t), Broadcast, idxU8, 0, []byte{1}); err == nil {
		t.Error("send failure ignored")
	}
	if _, err := c.DownloadMulti(tctx(t), Broadcast, idxU8, 0, make([]byte, 100)); err == nil {
		t.Error("send failure ignored")
	}
	if _, err := c.UploadMulti(tctx(t), Broadcast, idxU8, 0); err == nil {
		t.Error("send failure ignored")
	}
	s, _ := NewServer(failBus{fdPorts(t, 1)[0]}, device(t, 5))
	_ = s.SetNodeID(5)
	s.send(context.Background(), reply{1, Message{Cmd: CmdBulkLastOK}})
	s.send(context.Background(), reply{1, Message{Cmd: 0x05}})
	if s.Stats().SendErrors != 2 {
		t.Error(s.Stats())
	}
}

// failBus fails every send.
type failBus struct{ *canbus.Bus }

func (failBus) Send(context.Context, *canbus.Frame) error { return errors.New("bus down") }
