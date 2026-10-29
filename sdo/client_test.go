// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// scriptedClient returns a client talking to a scripted server.
func scriptedClient(t *testing.T, respond func(req frame) []frame, opts ...ClientOption) (*Client, *[]frame) {
	t.Helper()
	a, b := testbus.Pair(t)
	got := script(t, b, respond)
	opts = append([]ClientOption{WithTimeout(40 * time.Millisecond)}, opts...)
	return newClient(t, a, opts...), got
}

func TestClientProtocolErrors(t *testing.T) {
	checkLeaks(t)
	const idx = idxDomain
	initResp := func(b0 byte, v uint32) frame { return muxFrame(b0, idx, 0, v) }
	type tc struct {
		name    string
		mode    BlockMode
		upload  bool
		data    []byte
		respond func(req frame) []frame
		code    canopen.AbortCode
	}
	cases := []tc{
		{name: "upload wrong specifier", upload: true, respond: func(frame) []frame { return []frame{initResp(0x60, 0)} },
			code: canopen.AbortCommand},
		{name: "upload segment toggle", upload: true, respond: func(r frame) []frame {
			if r[0] == 0x40 {
				return []frame{initResp(0x41, 10)}
			}
			return []frame{seg(0x10)}
		}, code: canopen.AbortToggle},
		{name: "upload segment specifier", upload: true, respond: func(r frame) []frame {
			if r[0] == 0x40 {
				return []frame{initResp(0x41, 10)}
			}
			return []frame{seg(0x20)}
		}, code: canopen.AbortCommand},
		{name: "upload longer than indicated", upload: true, respond: func(r frame) []frame {
			if r[0] == 0x40 {
				return []frame{initResp(0x41, 3)}
			}
			return []frame{seg(0x00)}
		}, code: canopen.AbortLengthHigh},
		{name: "upload shorter than indicated", upload: true, respond: func(r frame) []frame {
			if r[0] == 0x40 {
				return []frame{initResp(0x41, 10)}
			}
			return []frame{seg(0x01)}
		}, code: canopen.AbortLengthLow},
		{name: "upload unsized over limit", upload: true, respond: func(r frame) []frame {
			if r[0] == 0x40 {
				return []frame{initResp(0x40, 0)}
			}
			return []frame{seg(r[0] & 0x10)}
		}, code: canopen.AbortOutOfMemory},
		{name: "download wrong specifier", data: []byte{1}, respond: func(frame) []frame { return []frame{initResp(0x41, 0)} },
			code: canopen.AbortCommand},
		{name: "download segment toggle", data: pattern(10), respond: func(r frame) []frame {
			if r[0] == 0x21 {
				return []frame{initResp(0x60, 0)}
			}
			return []frame{seg(0x30)}
		}, code: canopen.AbortToggle},
		{name: "download segment specifier", data: pattern(10), respond: func(r frame) []frame {
			if r[0] == 0x21 {
				return []frame{initResp(0x60, 0)}
			}
			return []frame{seg(0x00)}
		}, code: canopen.AbortCommand},
		{name: "block download blksize 0", mode: BlockAlways, data: pattern(10), respond: func(frame) []frame {
			return []frame{initResp(0xA4, 0)}
		}, code: canopen.AbortBlockSize},
		{name: "block download wrong sub-command", mode: BlockAlways, data: pattern(10), respond: func(frame) []frame {
			return []frame{initResp(0xA5, 10)}
		}, code: canopen.AbortCommand},
		{name: "block download ack beyond sent", mode: BlockAlways, data: pattern(10), respond: func(r frame) []frame {
			if r[0] == 0xC6 {
				return []frame{initResp(0xA4, 10)}
			}
			if r[0] == 0x82 {
				return []frame{{0xA2, 3, 10}}
			}
			return nil
		}, code: canopen.AbortSequence},
		{name: "block download ack blksize", mode: BlockAlways, data: pattern(10), respond: func(r frame) []frame {
			if r[0] == 0xC6 {
				return []frame{initResp(0xA4, 10)}
			}
			if r[0] == 0x82 {
				return []frame{{0xA2, 2, 200}}
			}
			return nil
		}, code: canopen.AbortBlockSize},
		{name: "block download ack specifier", mode: BlockAlways, data: pattern(10), respond: func(r frame) []frame {
			if r[0] == 0xC6 {
				return []frame{initResp(0xA4, 10)}
			}
			if r[0] == 0x82 {
				return []frame{{0x60}}
			}
			return nil
		}, code: canopen.AbortCommand},
		{name: "block download end specifier", mode: BlockAlways, data: pattern(10), respond: func(r frame) []frame {
			switch {
			case r[0] == 0xC6:
				return []frame{initResp(0xA4, 10)}
			case r[0] == 0x82:
				return []frame{{0xA2, 2, 10}}
			case r[0]&0xE3 == 0xC1:
				return []frame{{0xA2, 2, 10}, {0x60}}
			}
			return nil
		}, code: canopen.AbortCommand},
		{name: "block upload wrong specifier", mode: BlockAlways, upload: true, respond: func(frame) []frame {
			return []frame{initResp(0x60, 0)}
		}, code: canopen.AbortCommand},
		{name: "block upload wrong sub-command", mode: BlockAlways, upload: true, respond: func(frame) []frame {
			return []frame{initResp(0xC1, 0)}
		}, code: canopen.AbortCommand},
		{name: "block upload too big", mode: BlockAlways, upload: true, respond: func(frame) []frame {
			return []frame{initResp(0xC6, 2000)}
		}, code: canopen.AbortOutOfMemory},
		{name: "block upload longer than indicated", mode: BlockAlways, upload: true, respond: func(r frame) []frame {
			if r[0] == 0xA3 {
				return []frame{seg(1), seg(2)}
			}
			return []frame{initResp(0xC6, 8)}
		}, code: canopen.AbortLengthHigh},
		{name: "block upload end longer", mode: BlockAlways, upload: true, respond: func(r frame) []frame {
			switch r[0] {
			case 0xA3:
				return []frame{seg(1), seg(0x82, 1)}
			case 0xA2:
				return []frame{{0xC1}}
			}
			return []frame{initResp(0xC6, 8)}
		}, code: canopen.AbortLengthHigh},
		{name: "block upload end shorter", mode: BlockAlways, upload: true, respond: func(r frame) []frame {
			switch r[0] {
			case 0xA3:
				return []frame{seg(0x81)}
			case 0xA2:
				return []frame{{0xC1 | 6<<2}}
			}
			return []frame{initResp(0xC6, 2)}
		}, code: canopen.AbortLengthLow},
		{name: "block upload bad CRC", mode: BlockAlways, upload: true, respond: func(r frame) []frame {
			switch r[0] {
			case 0xA3:
				return []frame{seg(0x81, 1, 2)}
			case 0xA2:
				return []frame{{0xC1 | 5<<2, 0x12, 0x34}}
			}
			return []frame{initResp(0xC6, 2)}
		}, code: canopen.AbortCRC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, got := scriptedClient(t, c.respond, WithBlockMode(c.mode), WithMaxSize(1000))
			ctx := ctxT(t)
			var err error
			if c.upload {
				_, err = cl.Upload(ctx, idx, 0)
			} else {
				err = cl.Download(ctx, idx, 0, c.data)
			}
			wantAbort(t, err, c.code, false)
			if !errors.Is(err, canopen.ErrProtocol) {
				t.Errorf("%v is not ErrProtocol", err)
			}
			_ = got
			if st := cl.Stats(); st.AbortsSent != 1 || st.Failed != 1 {
				t.Errorf("stats %+v", st)
			}
		})
	}
}

func TestClientLenient(t *testing.T) {
	a, b := testbus.Pair(t)
	p := newPeer(t, b, canopen.NewCOBID(0x605), canopen.NewCOBID(0x585))
	c := newClient(t, a, WithTimeout(time.Second))
	ctx := ctxT(t)
	type res struct {
		data []byte
		err  error
	}
	run := func(fn func() ([]byte, error)) chan res {
		ch := make(chan res, 1)
		go func() { d, err := fn(); ch <- res{d, err} }()
		return ch
	}
	// A short expedited response (DLC 5), after a stale response for
	// another object.
	ch := run(func() ([]byte, error) { return c.Upload(ctx, idxU8, 0) })
	p.recv(time.Second)
	p.send(muxFrame(0x4F, 0x1000, 0, 7))
	p.sendLen(muxFrame(0x4F, idxU8, 0, 0x42), 5)
	if r := <-ch; r.err != nil || len(r.data) != 1 || r.data[0] != 0x42 {
		t.Fatalf("upload = % X, %v", r.data, r.err)
	}
	if c.Stats().Ignored != 1 {
		t.Errorf("ignored = %d", c.Stats().Ignored)
	}
	// Expedited without size: 4 bytes.
	ch = run(func() ([]byte, error) { return c.Upload(ctx, idxU8, 0) })
	p.recv(time.Second)
	p.send(muxFrame(0x42, idxU8, 0, 0x04030201))
	if r := <-ch; r.err != nil || len(r.data) != 4 {
		t.Fatalf("upload = % X, %v", r.data, r.err)
	}
	// Segmented without size, short segment frames.
	ch = run(func() ([]byte, error) { return c.Upload(ctx, idxDomain, 0) })
	p.recv(time.Second)
	p.send(muxFrame(0x40, idxDomain, 0, 0))
	p.recv(time.Second)
	p.send(seg(0x00, 1, 2, 3, 4, 5, 6, 7))
	p.recv(time.Second)
	p.sendLen(seg(0x10|5<<1|1, 8, 9), 3)
	if r := <-ch; r.err != nil || len(r.data) != 9 || r.data[8] != 9 {
		t.Fatalf("upload = % X, %v", r.data, r.err)
	}
	// Remote abort during a segmented upload.
	ch = run(func() ([]byte, error) { return c.Upload(ctx, idxDomain, 0) })
	p.recv(time.Second)
	p.send(muxFrame(0x41, idxDomain, 0, 20))
	p.recv(time.Second)
	p.send(abortFrame(0, 0, canopen.AbortDeviceState))
	r := <-ch
	wantAbort(t, r.err, canopen.AbortDeviceState, true)
	var ae *canopen.AbortError
	errors.As(r.err, &ae)
	if ae.Index != idxDomain || ae.SubIndex != 0 {
		t.Errorf("abort for %04X:%02X", ae.Index, ae.SubIndex)
	}
}

// TestClientBlockResume has a server acknowledge part of a block download
// and checks that the client resumes after the acknowledged segments.
func TestClientBlockResume(t *testing.T) {
	data := pattern(40) // 6 segments
	var mu sync.Mutex
	var got []byte
	var blockSegs [][]byte
	acks, blk := 0, 4
	respond := func(r frame) []frame {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r[0] == 0xC6:
			return []frame{muxFrame(0xA4, idxDomain, 0, 4)}
		case r[0]&0x7F >= 1 && int(r[0]&0x7F) <= blk && r[0]&0xE3 != 0xC1:
			blockSegs = append(blockSegs, append([]byte(nil), r[1:]...))
			if r[0]&0x80 == 0 && int(r[0]) != blk {
				return nil
			}
			acks++
			n := len(blockSegs)
			if acks == 1 {
				n = 2 // accept two of the four, ask for blocks of 3
			}
			for _, s := range blockSegs[:n] {
				got = append(got, s...)
			}
			blockSegs, blk = nil, 3
			return []frame{{0xA2, byte(n), 3}}
		case r[0]&0xE3 == 0xC1:
			n := int(r[0] >> 2 & 7)
			got = got[:len(got)-n]
			if CRC16(got) != binary.LittleEndian.Uint16(r[1:3]) {
				return []frame{abortFrame(idxDomain, 0, canopen.AbortCRC)}
			}
			return []frame{{0xA1}}
		}
		return nil
	}
	c, _ := scriptedClient(t, respond, WithBlockMode(BlockAlways))
	if err := c.Download(ctxT(t), idxDomain, 0, data); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	equal(t, got, data)
	if acks < 3 {
		t.Errorf("%d acknowledgements", acks)
	}
}

// TestClientBlockReack has a server lose a segment of a block upload and
// checks that the client repeats its acknowledgement and recovers.
func TestClientBlockReack(t *testing.T) {
	data := pattern(30)
	crc := CRC16(data)
	segs := func(from, n int) []frame {
		var out []frame
		for i := 0; i < n; i++ {
			p := from + 7*i
			end := min(p+7, len(data))
			f := seg(byte(i+1), data[p:end]...)
			if end == len(data) {
				f[0] |= 0x80
				out = append(out, f)
				break
			}
			out = append(out, f)
		}
		return out
	}
	respond := func(r frame) []frame {
		switch {
		case r[0] == 0xA4:
			return []frame{muxFrame(0xC6, idxDomain, 0, uint32(len(data)))}
		case r[0] == 0xA3: // first block: segment 2 lost
			s := segs(0, 5)
			return []frame{s[0], s[2], s[3]}
		case r[0] == 0xA2 && r[1] == 1: // the client acknowledged 1
			return segs(7, 5)
		case r[0] == 0xA2 && r[1] == 4:
			return []frame{{0xC1 | byte(7-2)<<2, byte(crc), byte(crc >> 8)}}
		}
		return nil
	}
	c, _ := scriptedClient(t, respond, WithBlockMode(BlockAlways))
	got, err := c.Upload(ctxT(t), idxDomain, 0)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got, data)
	if st := c.Stats(); st.Reacks == 0 || st.Ignored == 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestClientBlockUploadSilent(t *testing.T) {
	respond := func(r frame) []frame {
		switch r[0] {
		case 0xA4:
			return []frame{muxFrame(0xC6, idxDomain, 0, 30)}
		case 0xA3:
			return []frame{seg(1)} // then nothing
		}
		return nil
	}
	c, _ := scriptedClient(t, respond, WithBlockMode(BlockAlways))
	start := time.Now()
	_, err := c.Upload(ctxT(t), idxDomain, 0)
	if !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	wantAbort(t, err, canopen.AbortTimeout, false)
	if el := time.Since(start); el > time.Second {
		t.Errorf("took %v", el)
	}
	if c.Stats().Reacks < 2 {
		t.Errorf("reacks = %d", c.Stats().Reacks)
	}
}

// TestClientBlockUploadEmpty: Lely CANopen's server ends the block upload
// of an empty object without a segment; CANopenNode sends one empty one.
func TestClientBlockUploadEmpty(t *testing.T) {
	cases := []struct {
		name  string
		init  byte    // initiate response: 0xC6 sized, 0xC4 unsized (CRC)
		start []frame // the server's answer to the start
		err   canopen.AbortCode
		size  uint32
	}{
		{"sized, no segment", 0xC6, []frame{seg(0xC1 | 7<<2)}, 0, 0},
		{"unsized, no segment", 0xC4, []frame{seg(0xC1 | 7<<2)}, 0, 0},
		{"sized, empty segment", 0xC6, []frame{seg(0x81)}, 0, 0},
		{"size 3, no segment", 0xC6, []frame{seg(0xC1 | 7<<2)}, canopen.AbortLengthLow, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			respond := func(r frame) []frame {
				switch r[0] {
				case 0xA4:
					return []frame{muxFrame(tc.init, idxDomain, 0, tc.size)}
				case 0xA3:
					return tc.start
				case 0xA2: // acknowledgement of the empty segment
					return []frame{seg(0xC1 | 7<<2)}
				}
				return nil
			}
			c, _ := scriptedClient(t, respond, WithBlockMode(BlockAlways))
			got, err := c.Upload(ctxT(t), idxDomain, 0)
			if tc.err != 0 {
				wantAbort(t, err, tc.err, false)
				return
			}
			if err != nil || len(got) != 0 {
				t.Fatalf("upload = % X, %v", got, err)
			}
		})
	}
}

func TestClientBlockUploadAbort(t *testing.T) {
	for _, phase := range []string{"segments", "end"} {
		t.Run(phase, func(t *testing.T) {
			respond := func(r frame) []frame {
				switch r[0] {
				case 0xA4:
					return []frame{muxFrame(0xC6, idxDomain, 0, 10)}
				case 0xA3:
					if phase == "segments" {
						return []frame{seg(1), abortFrame(idxDomain, 0, canopen.AbortHardware)}
					}
					return []frame{seg(1), seg(0x82, 1, 2, 3)}
				case 0xA2:
					return []frame{seg(0x82), seg(5), abortFrame(idxDomain, 0, canopen.AbortHardware)}
				}
				return nil
			}
			c, _ := scriptedClient(t, respond, WithBlockMode(BlockAlways))
			_, err := c.Upload(ctxT(t), idxDomain, 0)
			wantAbort(t, err, canopen.AbortHardware, true)
		})
	}
}

func TestClientContext(t *testing.T) {
	checkLeaks(t)
	c, got := scriptedClient(t, func(frame) []frame { return nil }, WithTimeout(5*time.Second))
	_ = got
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Upload(ctx, idxDomain, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	eventually(t, func() bool { return c.Stats().AbortsSent == 1 })

	// A second transfer waits for the first; its context ends first.
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		close(started)
		_, err := c.Upload(ctx, idxDomain, 0)
		done <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := c.Download(ctx2, idxDomain, 0, []byte{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued transfer: %v", err)
	}
	<-done
}

func TestClientClose(t *testing.T) {
	checkLeaks(t)
	c, _ := scriptedClient(t, func(frame) []frame { return nil }, WithTimeout(5*time.Second))
	done := make(chan error, 1)
	go func() { _, err := c.Upload(context.Background(), idxDomain, 0); done <- err }()
	time.Sleep(20 * time.Millisecond)
	// A transfer queued behind the running one ends with ErrClosed too.
	queued := make(chan error, 1)
	go func() { queued <- c.Download(context.Background(), idxDomain, 0, []byte{1}) }()
	time.Sleep(10 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("running transfer: %v", err)
	}
	if err := <-queued; !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("queued transfer: %v", err)
	}
	if _, err := c.Upload(context.Background(), idxDomain, 0); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("after close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestNewClient(t *testing.T) {
	a, _ := testbus.Pair(t)
	if _, err := NewClient(nil, 1); err == nil {
		t.Error("nil bus accepted")
	}
	if _, err := NewClient(a, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Errorf("node 0: %v", err)
	}
	for _, o := range []ClientOption{WithTimeout(0), WithBlockSize(0), WithBlockSize(128), WithMaxSize(-1),
		WithProtocolSwitch(256), WithRetries(-1), WithBlockMode(9)} {
		if _, err := NewClient(a, 1, o); err == nil {
			t.Errorf("option accepted")
		}
	}
	c, err := NewClient(a, 0, WithCOBIDs(0x123, 0x124))
	if err != nil {
		t.Fatal(err)
	}
	if tx, rx := c.COBIDs(); tx != 0x123 || rx != 0x124 {
		t.Errorf("COB-IDs %v %v", tx, rx)
	}
	c, _ = NewClient(a, 127)
	if tx, rx := c.COBIDs(); tx != 0x67F || rx != 0x5FF {
		t.Errorf("COB-IDs %v %v", tx, rx)
	}
	h := discardHandler{}
	if h.Enabled(context.Background(), slog.LevelError) || h.Handle(context.Background(), slog.Record{}) != nil ||
		h.WithAttrs(nil) != h || h.WithGroup("g") != h {
		t.Error("discard handler")
	}
	if (&localAbort{canopen.AbortCRC}).Error() != canopen.AbortCRC.Error() {
		t.Error("localAbort text")
	}
	for m, s := range map[BlockMode]string{BlockNever: "never", BlockAlways: "always", BlockAuto: "auto", 7: "BlockMode(7)"} {
		if m.String() != s {
			t.Errorf("%d: %q", m, m.String())
		}
	}
}

// TestClientBusClosed checks the errors when the bus is gone.
func TestClientBusClosed(t *testing.T) {
	a, _ := testbus.Pair(t)
	c := newClient(t, a)
	_ = a.Close()
	if _, err := c.Upload(ctxT(t), idxDomain, 0); err == nil || errors.Is(err, canopen.ErrTimeout) {
		t.Errorf("err = %v", err)
	}
}
