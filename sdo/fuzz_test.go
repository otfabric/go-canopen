// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"testing"
	"time"
)

// Fuzz input of the server machine: records of an op byte (op%8 == 7:
// timeout, 6: repeat acknowledgement, else a message) followed, for
// messages, by its 8 bytes.
func serverSeed(frames ...frame) []byte {
	var b []byte
	for _, f := range frames {
		b = append(b, 0)
		b = append(b, f[:]...)
	}
	return b
}

func FuzzServer(f *testing.F) {
	crc := CRC16(pattern(10))
	f.Add(serverSeed(muxFrame(0x23, idxLimited, 0, 500)))
	f.Add(serverSeed(muxFrame(0x40, idxString, 0, 0), frame{0x60}, frame{0x70}, frame{0x60}, frame{0x70}))
	f.Add(serverSeed(muxFrame(0x21, idxDomain, 0, 10), seg(0x00, pattern(7)...), seg(0x19, 1, 2, 3)))
	f.Add(serverSeed(blkInitDL(idxDomain, 10, true), seg(1, pattern(10)[:7]...), seg(0x82, pattern(10)[7:]...),
		frame{0xC1 | 4<<2, byte(crc), byte(crc >> 8)}))
	f.Add(serverSeed(blkInitUL(idxString, 2, 0), frame{0xA3}, frame{0xA2, 2, 3}, frame{0xA2, 2, 3}, frame{0xA1}))
	f.Add(serverSeed(blkInitUL(0x1000, 10, 20)))
	f.Add(append(serverSeed(blkInitDL(idxDomain, 30, true), seg(1)), 6, 7))
	f.Add([]byte{0, 0xE0, 0, 0, 0, 0, 0, 0, 0, 7})
	f.Fuzz(func(t *testing.T, in []byte) {
		m, r := newMachine(testDict(t), 4096, 8)
		for len(in) > 0 {
			op := in[0]
			in = in[1:]
			switch op % 8 {
			case 7:
				m.timeout()
			case 6:
				m.reack()
			default:
				var fr frame
				n := copy(fr[:], in)
				in = in[n:]
				m.handle(fr)
			}
			if out := r.take(); len(out) > MaxBlockSize {
				t.Fatalf("%d messages for one input", len(out))
			}
			if m.state >= stateCount || m.seq > MaxBlockSize || len(m.buf) > 4096+segData {
				t.Fatalf("bad machine state %d seq %d buf %d", m.state, m.seq, len(m.buf))
			}
			if !m.active() && m.buf != nil {
				t.Fatal("idle machine holds data")
			}
		}
	})
}

// fakeLink answers the client from a list of messages; when they run out
// the server is silent.
type fakeLink struct {
	in   []frame
	sent int
}

func (l *fakeLink) send(context.Context, frame) error { l.sent++; return nil }

func (l *fakeLink) recv(ctx context.Context, _ time.Duration) (frame, error) {
	if err := ctx.Err(); err != nil {
		return frame{}, err
	}
	if len(l.in) == 0 {
		return frame{}, errNoResponse
	}
	f := l.in[0]
	l.in = l.in[1:]
	return f, nil
}

func (l *fakeLink) drain() {}

// fuzzClient runs one client transfer against the messages in data. The
// first byte selects the transfer, the second the download size.
func fuzzClient(t testing.TB, data []byte) ([]byte, error) {
	if len(data) < 2 {
		return nil, nil
	}
	sel, size := data[0], int(data[1])*20
	l := &fakeLink{}
	for rest := data[2:]; len(rest) > 0; {
		var f frame
		n := copy(f[:], rest)
		rest = rest[n:]
		l.in = append(l.in, f)
	}
	cfg := defaultConfig()
	cfg.maxSize, cfg.blkSize, cfg.crc = 2000, int(sel>>3&7)+1, sel&0x40 == 0
	if err := cfg.check(); err != nil {
		t.Fatal(err)
	}
	c := &Client{cfg: cfg, log: cfg.log}
	x := &xfer{c: c, l: l, ctx: context.Background(), index: idxDomain}
	var out []byte
	var err error
	switch sel % 4 {
	case 0:
		out, err = x.upload()
	case 1:
		out, err = x.blockUpload(sel & 0x80 >> 2)
	case 2:
		err = x.download(pattern(size))
	default:
		err = x.blockDownload(pattern(size))
	}
	err = x.finish(err)
	if err == nil && len(out) > cfg.maxSize {
		t.Fatalf("upload of %d bytes beyond the limit", len(out))
	}
	if err != nil && !cleanError(err) {
		t.Fatalf("unclean error %v", err)
	}
	if l.sent > 10*(len(data)+10) {
		t.Fatalf("%d messages sent for %d bytes of input", l.sent, len(data))
	}
	return out, err
}

func clientSeed(sel, size byte, frames ...frame) []byte {
	b := []byte{sel, size}
	for _, f := range frames {
		b = append(b, f[:]...)
	}
	return b
}

func FuzzClient(f *testing.F) {
	f.Add(clientSeed(0, 0, muxFrame(0x4B, idxDomain, 0, 0x1234)))
	f.Add(clientSeed(0, 0, muxFrame(0x41, idxDomain, 0, 9), seg(0x00, 1, 2, 3, 4, 5, 6, 7), seg(0x1B, 8, 9)))
	f.Add(clientSeed(9, 0, muxFrame(0xC6, idxDomain, 0, 9), seg(1), seg(0x82, 1, 2), frame{0xC1 | 5<<2}))
	f.Add(clientSeed(0x81, 0, muxFrame(0x43, idxDomain, 0, 7)))
	f.Add(clientSeed(2, 1, muxFrame(0x60, idxDomain, 0, 0), seg(0x20), seg(0x30), seg(0x20)))
	f.Add(clientSeed(3, 1, muxFrame(0xA4, idxDomain, 0, 2), frame{0xA2, 2, 2}, frame{0xA2, 1, 1}, frame{0xA1}))
	f.Add(clientSeed(2, 0, abortFrame(idxDomain, 0, 0x06020000)))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = fuzzClient(t, data)
	})
}

func TestFuzzClientSeeds(t *testing.T) {
	// The seeds describe correct transfers.
	got, err := fuzzClient(t, clientSeed(0, 0, muxFrame(0x41, idxDomain, 0, 9), seg(0x00, 1, 2, 3, 4, 5, 6, 7), seg(0x1B, 8, 9)))
	if err != nil || len(got) != 9 {
		t.Fatalf("segmented: % X, %v", got, err)
	}
	crc := CRC16([]byte{0, 0})
	got, err = fuzzClient(t, clientSeed(9, 0, muxFrame(0xC6, idxDomain, 0, 9), seg(1), seg(0x82, 0, 0), frame{0xC1 | 5<<2, byte(crc), byte(crc >> 8)}))
	if err != nil || len(got) != 9 {
		t.Fatalf("block: % X, %v", got, err)
	}
	if _, err := fuzzClient(t, clientSeed(3, 1, muxFrame(0xA4, idxDomain, 0, 2), frame{0xA2, 2, 2}, frame{0xA2, 1, 1}, frame{0xA1})); err != nil {
		t.Fatalf("block download: %v", err)
	}
	if _, err := fuzzClient(t, clientSeed(2, 0)); !cleanError(err) {
		t.Fatalf("silent server: %v", err)
	}
}
