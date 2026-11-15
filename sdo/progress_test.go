// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
)

// errScriptEnd ends a scripted exchange that went on longer than any
// transfer of the test may.
var errScriptEnd = errors.New("scripted server: request limit reached")

// answerLink answers each request at once with the frames answer returns
// for it, without end, up to limit requests.
type answerLink struct {
	answer  func(req frame) []frame
	pending []frame
	sent    int
	limit   int
}

func (l *answerLink) send(_ context.Context, f frame) error {
	l.sent++
	if l.sent > l.limit {
		return errScriptEnd
	}
	l.pending = append(l.pending, l.answer(f)...)
	return nil
}

func (l *answerLink) recv(ctx context.Context, _ time.Duration) (frame, error) {
	if err := ctx.Err(); err != nil {
		return frame{}, err
	}
	if len(l.pending) == 0 {
		return frame{}, errNoResponse
	}
	f := l.pending[0]
	l.pending = l.pending[1:]
	return f, nil
}

func (l *answerLink) drain()               {}
func (l *answerLink) stale() (frame, bool) { return frame{}, false }

// linkXfer returns a transfer of idxDomain over l.
func linkXfer(t *testing.T, l link) *xfer {
	t.Helper()
	cfg := defaultConfig()
	if err := cfg.check(); err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: cfg, log: cfg.log}
	return &xfer{c: c, l: l, ctx: context.Background(), index: idxDomain}
}

// emptySegments answers an upload: the initiate with an unsized segmented
// response, then segment n (counted from 1) with data when hasData(n)
// says so, else with no data; segment last is the last one.
func emptySegments(hasData func(n int) bool, last int) func(req frame) []frame {
	n := 0
	return func(r frame) []frame {
		switch r[0] >> 5 {
		case ccsInitiateUpload:
			return []frame{muxFrame(scsInitiateUpload<<5, idxDomain, 0, 0)}
		case ccsUploadSegment:
			n++
			b0 := r[0] & 0x10 // the request's toggle
			if !hasData(n) {
				b0 |= 7 << 1 // no data byte
			}
			if n == last {
				b0 |= 1
			}
			return []frame{seg(b0, 1, 2, 3, 4, 5, 6, 7)}
		}
		return nil
	}
}

// TestUploadWithoutProgress: an upload whose segments carry no data, and
// which the server never ends, ends with ErrProtocol and the abort
// AbortGeneral after maxNoProgress+1 such segments, not when the context
// ends.
func TestUploadWithoutProgress(t *testing.T) {
	l := &answerLink{answer: emptySegments(func(int) bool { return false }, 0), limit: 1000}
	x := linkXfer(t, l)
	_, err := x.upload()
	err = x.finish(err)
	var ae *canopen.AbortError
	if !errors.Is(err, canopen.ErrProtocol) || !errors.As(err, &ae) || ae.Code != canopen.AbortGeneral {
		t.Fatalf("upload = %v, want ErrProtocol with AbortGeneral", err)
	}
	// initiate, maxNoProgress+1 segment requests, the abort
	if want := 1 + maxNoProgress + 1 + 1; l.sent != want {
		t.Errorf("%d requests sent, want %d", l.sent, want)
	}
}

// TestUploadEmptySegmentsBelowBound: up to maxNoProgress segments in a row
// without data are accepted; data resets the count.
func TestUploadEmptySegmentsBelowBound(t *testing.T) {
	// Segments 1..4 empty, 5 data, 6..9 empty, 10 data and last.
	hasData := func(n int) bool { return n == 5 || n == 10 }
	l := &answerLink{answer: emptySegments(hasData, 10), limit: 1000}
	x := linkXfer(t, l)
	data, err := x.upload()
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(data) != 14 {
		t.Errorf("%d bytes uploaded, want 14", len(data))
	}
}

// zeroAcks answers a block download: the initiate with block size blk,
// then each block with an acknowledgement of no segment.
func zeroAcks(blk uint8) func(req frame) []frame {
	return func(r frame) []frame {
		switch {
		case r[0]>>5 == ccsBlockDownload && r[0]&1 == 0 && r[0]&3 != blkEnd:
			return []frame{muxFrame(scsBlockDownload<<5, idxDomain, 0, uint32(blk))}
		case r[0]&0x7F == blk || r[0]&0x80 != 0:
			return []frame{{scsBlockDownload<<5 | blkAck, 0, blk}}
		}
		return nil
	}
}

// TestBlockDownloadWithoutProgress: a block download whose blocks are
// acknowledged with no segment, without end, ends with ErrProtocol and the
// abort AbortSequence after maxNoProgress+1 such acknowledgements.
func TestBlockDownloadWithoutProgress(t *testing.T) {
	const blk = 4
	l := &answerLink{answer: zeroAcks(blk), limit: 1000}
	x := linkXfer(t, l)
	err := x.finish(x.blockDownload(pattern(100)))
	var ae *canopen.AbortError
	if !errors.Is(err, canopen.ErrProtocol) || !errors.As(err, &ae) || ae.Code != canopen.AbortSequence {
		t.Fatalf("block download = %v, want ErrProtocol with AbortSequence", err)
	}
	// initiate, maxNoProgress+1 blocks of blk segments, the abort
	if want := 1 + (maxNoProgress+1)*blk + 1; l.sent != want {
		t.Errorf("%d requests sent, want %d", l.sent, want)
	}
}

// TestClientWithoutProgressOnBus runs both transfers against a scripted
// server on a bus, through Upload and Download: each returns ErrProtocol
// long before the context's deadline, which the client would otherwise
// run into.
func TestClientWithoutProgressOnBus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    BlockMode
		respond func(req frame) []frame
		run     func(ctx context.Context, c *Client) error
	}{
		{"segmented upload", BlockNever, emptySegments(func(int) bool { return false }, 0), func(ctx context.Context, c *Client) error {
			_, err := c.Upload(ctx, idxDomain, 0)
			return err
		}},
		{"block download", BlockAlways, zeroAcks(4), func(ctx context.Context, c *Client) error {
			return c.Download(ctx, idxDomain, 0, pattern(100))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			respond := func(r frame) []frame { mu.Lock(); defer mu.Unlock(); return tc.respond(r) }
			c, _ := scriptedClient(t, respond, WithBlockMode(tc.mode), WithTimeout(time.Second))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := tc.run(ctx, c)
			if !errors.Is(err, canopen.ErrProtocol) {
				t.Fatalf("transfer = %v, want ErrProtocol", err)
			}
		})
	}
}

// TestDownloadSizeAtIntLimit: a segmented or block download indicating
// the largest size an int holds on a 32-bit platform is accepted by a
// server whose limit is the largest int, without computing a capacity
// beyond it.
func TestDownloadSizeAtIntLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		init frame
		resp byte // specifier of the expected response
	}{
		{"segmented, largest int32 minus a segment", muxFrame(ccsInitiateDownload<<5|1, idxDomain, 0, math.MaxInt32-segData), scsInitiateDownload},
		{"segmented, one more", muxFrame(ccsInitiateDownload<<5|1, idxDomain, 0, math.MaxInt32-segData+1), scsInitiateDownload},
		{"segmented, largest int32", muxFrame(ccsInitiateDownload<<5|1, idxDomain, 0, math.MaxInt32), scsInitiateDownload},
		{"block, one more than largest int32 minus a segment", blkInitDL(idxDomain, math.MaxInt32-segData+1, false), scsBlockDownload},
		{"block, largest int32", blkInitDL(idxDomain, math.MaxInt32, false), scsBlockDownload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, r := newMachine(testDict(t), math.MaxInt, 8)
			m.handle(tc.init)
			out := r.take()
			if len(out) != 1 || out[0].cs() != tc.resp {
				t.Fatalf("response %x, want specifier %d", out, tc.resp)
			}
			if c := cap(m.buf); c > 64<<10 {
				t.Errorf("buffer capacity %d, want at most %d", c, 64<<10)
			}
		})
	}
}
