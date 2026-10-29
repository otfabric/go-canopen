// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-canopen"
)

func seg(b0 byte, data ...byte) frame {
	f := frame{b0}
	copy(f[1:], data)
	return f
}

func blkInitDL(idx uint16, size uint32, crc bool) frame {
	b0 := byte(0xC2)
	if crc {
		b0 |= 4
	}
	return muxFrame(b0, idx, 0, size)
}

func blkInitUL(idx uint16, blk, pst byte) frame {
	f := muxFrame(0xA4, idx, 0, 0)
	f[4], f[5] = blk, pst
	return f
}

func abortOf(idx uint16, code canopen.AbortCode) frame { return abortFrame(idx, 0, code) }

// TestMachine drives the server state machine message by message.
func TestMachine(t *testing.T) {
	crc7 := CRC16(pattern(7))
	type tc struct {
		name    string
		maxSize int
		blk     uint8
		noCRC   bool
		in      []frame
		want    frame // last message sent; zero: nothing sent by the last input
		state   serverState
		check   func(t *testing.T, m *machine)
	}
	cases := []tc{
		{name: "unknown command", in: []frame{{0xE0}}, want: abortOf(0, canopen.AbortCommand)},
		{name: "segment while idle", in: []frame{{0x00}}, want: abortOf(0, canopen.AbortCommand)},
		{name: "block ack while idle", in: []frame{{0xA2, 1, 1}}, want: abortOf(0, canopen.AbortCommand)},
		{name: "abort while idle", in: []frame{abortOf(idxDomain, canopen.AbortGeneral)}},
		{name: "expedited unsized u8", in: []frame{muxFrame(0x22, idxU8, 0, 0x99)},
			want: muxFrame(0x60, idxU8, 0, 0), check: func(t *testing.T, m *machine) {
				if v, _ := m.d.GetUint(idxU8, 0); v != 0x99 {
					t.Errorf("u8 = %x", v)
				}
			}},
		{name: "expedited unsized domain", in: []frame{muxFrame(0x22, idxDomain, 0, 0x04030201)},
			want: muxFrame(0x60, idxDomain, 0, 0), check: func(t *testing.T, m *machine) {
				if b, _ := m.d.Get(idxDomain, 0); !bytes.Equal(b, []byte{1, 2, 3, 4}) {
					t.Errorf("domain = % X", b)
				}
			}},
		{name: "segmented toggle", in: []frame{muxFrame(0x21, idxDomain, 0, 14), seg(0x10)},
			want: abortOf(idxDomain, canopen.AbortToggle)},
		{name: "segmented longer than indicated", in: []frame{muxFrame(0x21, idxDomain, 0, 3), seg(0x00, 1, 2, 3, 4, 5, 6, 7)},
			want: abortOf(idxDomain, canopen.AbortLengthHigh)},
		{name: "segmented shorter than indicated", in: []frame{muxFrame(0x21, idxDomain, 0, 10), seg(0x01, 1, 2, 3, 4, 5, 6, 7)},
			want: abortOf(idxDomain, canopen.AbortLengthLow)},
		{name: "segmented unsized", in: []frame{muxFrame(0x20, idxDomain, 0, 0), seg(0x00, 1, 2, 3, 4, 5, 6, 7), seg(0x10|5<<1|1, 8, 9)},
			want: seg(0x30), check: func(t *testing.T, m *machine) {
				if b, _ := m.d.Get(idxDomain, 0); len(b) != 9 || b[8] != 9 {
					t.Errorf("domain = % X", b)
				}
			}},
		{name: "segmented unsized over limit", maxSize: 10, in: []frame{muxFrame(0x20, idxDomain, 0, 0), seg(0x00), seg(0x10)},
			want: abortOf(idxDomain, canopen.AbortOutOfMemory)},
		{name: "segmented sized over limit", maxSize: 10, in: []frame{muxFrame(0x21, idxDomain, 0, 11)},
			want: abortOf(idxDomain, canopen.AbortOutOfMemory)},
		{name: "segmented write refused", in: []frame{muxFrame(0x21, idxLimited, 0, 5), seg(0x05, 1, 2, 3, 4, 5)},
			want: abortOf(idxLimited, canopen.AbortLengthHigh)},
		{name: "segmented wrong command", in: []frame{muxFrame(0x21, idxDomain, 0, 14), {0x60}},
			want: abortOf(idxDomain, canopen.AbortCommand)},
		{name: "segmented client abort", in: []frame{muxFrame(0x21, idxDomain, 0, 14), abortOf(idxDomain, canopen.AbortGeneral)},
			check: func(t *testing.T, m *machine) {
				if m.st.abortsReceived.Load() != 1 {
					t.Error("abort not counted")
				}
			}},
		{name: "upload toggle", in: []frame{muxFrame(0x40, idxString, 0, 0), {0x70}},
			want: abortOf(idxString, canopen.AbortToggle)},
		{name: "upload wrong command", in: []frame{muxFrame(0x40, idxString, 0, 0), {0x00}},
			want: abortOf(idxString, canopen.AbortCommand)},
		{name: "upload empty", in: []frame{muxFrame(0x40, idxDomain, 0, 0), {0x60}}, want: seg(0x0F)},
		{name: "restart", in: []frame{muxFrame(0x40, idxString, 0, 0), muxFrame(0x40, 0x1000, 0, 0)},
			want: muxFrame(0x43, 0x1000, 0, 0x00020192), check: func(t *testing.T, m *machine) {
				if m.st.restarts.Load() != 1 {
					t.Error("restart not counted")
				}
			}},
		{name: "block upload blksize 0", in: []frame{blkInitUL(idxString, 0, 0)}, want: abortOf(idxString, canopen.AbortBlockSize)},
		{name: "block upload blksize 128", in: []frame{blkInitUL(idxString, 128, 0)}, want: abortOf(idxString, canopen.AbortBlockSize)},
		{name: "block upload no object", in: []frame{blkInitUL(0x3000, 10, 0)}, want: abortOf(0x3000, canopen.AbortNoObject)},
		{name: "block upload wrong command", in: []frame{blkInitUL(idxString, 10, 0), {0x60}},
			want: abortOf(idxString, canopen.AbortCommand)},
		{name: "block upload ack beyond sent", in: []frame{blkInitUL(idxString, 2, 0), {0xA3}, {0xA2, 3, 2}},
			want: abortOf(idxString, canopen.AbortSequence)},
		{name: "block upload ack blksize 0", in: []frame{blkInitUL(idxString, 2, 0), {0xA3}, {0xA2, 2, 0}},
			want: abortOf(idxString, canopen.AbortBlockSize)},
		{name: "block upload repeated start", in: []frame{blkInitUL(idxString, 2, 0), {0xA3}, {0xA3}},
			state: stBlockUploadAck},
		{name: "block upload wrong command while acking", in: []frame{blkInitUL(idxString, 2, 0), {0xA3}, {0xA1}},
			want: abortOf(idxString, canopen.AbortCommand)},
		{name: "block upload partial ack", in: []frame{blkInitUL(idxString, 2, 0), {0xA3}, {0xA2, 1, 3}},
			want: seg(0x83, []byte(testString[21:])...), state: stBlockUploadAck},
		{name: "block upload end repeated ack", in: []frame{blkInitUL(idxDomain, 2, 0), {0xA3}, {0xA2, 1, 2}, {0xA2, 1, 2}},
			want: frame{0xC1 | 7<<2}, state: stBlockUploadEnd},
		{name: "block upload end other ack", in: []frame{blkInitUL(idxDomain, 2, 0), {0xA3}, {0xA2, 1, 2}, {0xA2, 0, 2}},
			state: stBlockUploadEnd},
		{name: "block upload end start", in: []frame{blkInitUL(idxDomain, 2, 0), {0xA3}, {0xA2, 1, 2}, {0xA3}},
			state: stBlockUploadEnd},
		{name: "block upload end garbage", in: []frame{blkInitUL(idxDomain, 2, 0), {0xA3}, {0xA2, 1, 2}, {0x60}},
			want: abortOf(idxDomain, canopen.AbortCommand)},
		{name: "block upload no CRC server", noCRC: true, in: []frame{blkInitUL(idxDomain, 2, 0)},
			want: muxFrame(0xC2, idxDomain, 0, 0), state: stBlockUploadInit},
		{name: "block download read-only", in: []frame{blkInitDL(idxString, 1, true)}, want: abortOf(idxString, canopen.AbortReadOnly)},
		{name: "block download no sub", in: []frame{muxFrame(0xC6, idxDomain, 1, 1)}, want: abortFrame(idxDomain, 1, canopen.AbortNoSubIndex)},
		{name: "block download too big", maxSize: 100, in: []frame{blkInitDL(idxDomain, 101, true)},
			want: abortOf(idxDomain, canopen.AbortOutOfMemory)},
		{name: "block download unsized over limit", maxSize: 10, in: []frame{muxFrame(0xC4, idxDomain, 0, 0), seg(1), seg(2)},
			want: abortOf(idxDomain, canopen.AbortOutOfMemory)},
		{name: "block download longer than indicated", in: []frame{blkInitDL(idxDomain, 3, true), seg(1)},
			want: abortOf(idxDomain, canopen.AbortLengthHigh)},
		{name: "block download out of sequence", in: []frame{blkInitDL(idxDomain, 30, true), seg(1), seg(3), seg(2)},
			state: stBlockDownload, check: func(t *testing.T, m *machine) {
				if m.seq != 2 || m.st.ignored.Load() != 1 {
					t.Errorf("seq %d ignored %d", m.seq, m.st.ignored.Load())
				}
			}},
		{name: "block download client abort", in: []frame{blkInitDL(idxDomain, 30, true), seg(1), abortOf(idxDomain, canopen.AbortGeneral)}},
		{name: "block download bad CRC", in: []frame{blkInitDL(idxDomain, 7, true), seg(0x81, pattern(7)...), {0xC1, byte(crc7 + 1), byte(crc7 >> 8)}},
			want: abortOf(idxDomain, canopen.AbortCRC)},
		{name: "block download good CRC", in: []frame{blkInitDL(idxDomain, 7, true), seg(0x81, pattern(7)...), {0xC1, byte(crc7), byte(crc7 >> 8)}},
			want: frame{0xA1}},
		{name: "block download CRC not used", in: []frame{blkInitDL(idxDomain, 7, false), seg(0x81, pattern(7)...), {0xC1, 1, 2}},
			want: frame{0xA1}},
		{name: "block download CRC unsupported", noCRC: true, in: []frame{blkInitDL(idxDomain, 7, true), seg(0x81, pattern(7)...), {0xC1, 1, 2}},
			want: frame{0xA1}},
		{name: "block download shorter than indicated", in: []frame{blkInitDL(idxDomain, 7, false), seg(0x81, pattern(7)...), {0xC1 | 1<<2}},
			want: abortOf(idxDomain, canopen.AbortLengthLow)},
		{name: "block download end ignores segments", in: []frame{blkInitDL(idxDomain, 7, false), seg(0x81), seg(0x81), seg(0x21)},
			state: stBlockDownloadEnd},
		{name: "block download end abort", in: []frame{blkInitDL(idxDomain, 7, false), seg(0x81), abortOf(idxDomain, canopen.AbortGeneral)}},
		{name: "block download write refused", in: []frame{blkInitDL(idxLimited, 4, false), seg(0x81, 1), {0xC1 | 3<<2}},
			want: abortOf(idxLimited, canopen.AbortValueLow)},
		{name: "block download blksize ack", blk: 2, in: []frame{blkInitDL(idxDomain, 30, false), seg(1), seg(2)},
			want: frame{0xA2, 2, 2}, state: stBlockDownload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.maxSize == 0 {
				c.maxSize = 1000
			}
			if c.blk == 0 {
				c.blk = 10
			}
			m, r := newMachine(testDict(t), c.maxSize, c.blk)
			m.crcOK = !c.noCRC
			for _, f := range c.in {
				r.take()
				m.handle(f)
			}
			out := r.take()
			var last frame
			if len(out) > 0 {
				last = out[len(out)-1]
			}
			if last != c.want {
				t.Errorf("last sent % X, want % X", last, c.want)
			}
			if m.state != c.state {
				t.Errorf("state %d, want %d", m.state, c.state)
			}
			if c.check != nil {
				c.check(t, m)
			}
		})
	}
}

func TestMachineTimers(t *testing.T) {
	m, r := newMachine(testDict(t), 1000, 10)
	m.timeout() // idle: nothing
	m.reack()
	if len(r.take()) != 0 {
		t.Fatal("idle machine sent something")
	}
	// Block download: no segment yet repeats the initiate response.
	m.handle(blkInitDL(idxDomain, 30, true))
	resp := r.take()[0]
	m.reack()
	if got := r.take(); len(got) != 1 || got[0] != resp {
		t.Fatalf("reack = % X, want the initiate response", got)
	}
	// Two segments: acknowledges them and starts a new block.
	m.handle(seg(1))
	m.handle(seg(2))
	m.reack()
	if got := r.take(); len(got) != 1 || got[0] != (frame{0xA2, 2, 10}) {
		t.Fatalf("reack = % X", got)
	}
	if m.seq != 0 {
		t.Fatalf("seq %d after reack", m.seq)
	}
	// Nothing new: repeats that acknowledgement.
	m.reack()
	if got := r.take(); len(got) != 1 || got[0] != (frame{0xA2, 2, 10}) {
		t.Fatalf("second reack = % X", got)
	}
	// The next block resumes at sequence number 1.
	m.handle(seg(0x81, 1, 2))
	if got := r.take(); len(got) != 1 || got[0] != (frame{0xA2, 1, 10}) {
		t.Fatalf("final ack = % X", got)
	}
	m.reack() // end state: repeats the final acknowledgement
	if got := r.take(); len(got) != 1 || got[0] != (frame{0xA2, 1, 10}) {
		t.Fatalf("end reack = % X", got)
	}
	m.timeout()
	if got := r.take(); len(got) != 1 || got[0] != abortOf(idxDomain, canopen.AbortTimeout) {
		t.Fatalf("timeout = % X", got)
	}
	if m.active() || m.st.timeouts.Load() != 1 {
		t.Fatal("timeout did not end the transfer")
	}
	// Not receiving: reack does nothing.
	m.handle(muxFrame(0x40, idxString, 0, 0))
	r.take()
	m.reack()
	if len(r.take()) != 0 {
		t.Fatal("reack while uploading")
	}
}

func TestIsInitiate(t *testing.T) {
	for b := 0; b < 256; b++ {
		b0 := byte(b)
		want := b0>>5 == 1 || b0>>5 == 2 || b0>>5 == 5 && b0&3 == 0 || b0>>5 == 6 && b0&1 == 0
		if isInitiate(b0) != want {
			t.Errorf("isInitiate(%02X) = %v", b0, !want)
		}
	}
}
