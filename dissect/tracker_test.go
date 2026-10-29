// SPDX-License-Identifier: MIT

package dissect

import (
	"testing"
)

type step struct {
	client bool
	data   []byte
	want   string // summary of the decoded SDO
}

func runSteps(t *testing.T, tr *Tracker, steps []step) {
	t.Helper()
	for i, s := range steps {
		id := uint16(0x585)
		if s.client {
			id = 0x605
		}
		m := tr.Decode(frame(t, canbusID(id), s.data...))
		if m.SDO == nil || m.SDO.String() != s.want {
			t.Fatalf("step %d: got %q, want %q", i, m.SDO, s.want)
		}
	}
}

func TestTrackerSegmented(t *testing.T) {
	tr := NewTracker()
	runSteps(t, tr, []step{
		// Segmented download of 9 bytes to 0x1008:00.
		{true, []byte{0x21, 0x08, 0x10, 0x00, 9, 0, 0, 0}, "initiate download 0x1008:00 size=9"},
		{false, []byte{0x60, 0x08, 0x10, 0x00, 0, 0, 0, 0}, "initiate download 0x1008:00"},
		{true, []byte{0x00, 1, 2, 3, 4, 5, 6, 7}, "download segment 0x1008:00 toggle=0: 01 02 03 04 05 06 07"},
		{false, []byte{0x20, 0, 0, 0, 0, 0, 0, 0}, "download segment 0x1008:00 toggle=0"},
		{true, []byte{0x1B, 8, 9, 0, 0, 0, 0, 0}, "download segment 0x1008:00 toggle=1 last: 08 09"},
		{false, []byte{0x30, 0, 0, 0, 0, 0, 0, 0}, "download segment 0x1008:00 toggle=1"},
		// Transfer complete: a stray segment is not attributed.
		{false, []byte{0x30, 0, 0, 0, 0, 0, 0, 0}, "download segment toggle=1"},
		// Expedited download.
		{true, []byte{0x2F, 0x00, 0x20, 0x00, 7, 0, 0, 0}, "initiate download 0x2000:00 expedited: 07"},
		{false, []byte{0x60, 0x00, 0x20, 0x00, 0, 0, 0, 0}, "initiate download 0x2000:00"},
		{true, []byte{0x00, 1, 2, 3, 4, 5, 6, 7}, "download segment toggle=0: 01 02 03 04 05 06 07"},
		// Segmented upload of 0x1009:00.
		{true, []byte{0x40, 0x09, 0x10, 0x00, 0, 0, 0, 0}, "initiate upload 0x1009:00"},
		{false, []byte{0x41, 0x09, 0x10, 0x00, 3, 0, 0, 0}, "initiate upload 0x1009:00 size=3"},
		{true, []byte{0x60, 0, 0, 0, 0, 0, 0, 0}, "upload segment 0x1009:00 toggle=0"},
		{false, []byte{0x09, 'a', 'b', 'c', 0, 0, 0, 0}, "upload segment 0x1009:00 toggle=0 last: 61 62 63"},
		{true, []byte{0x60, 0, 0, 0, 0, 0, 0, 0}, "upload segment toggle=0"},
		// Expedited upload, then an abort mid-transfer.
		{true, []byte{0x40, 0x18, 0x10, 0x01, 0, 0, 0, 0}, "initiate upload 0x1018:01"},
		{false, []byte{0x43, 0x18, 0x10, 0x01, 1, 0, 0, 0}, "initiate upload 0x1018:01 expedited: 01 00 00 00"},
		{true, []byte{0x40, 0x0A, 0x10, 0x00, 0, 0, 0, 0}, "initiate upload 0x100A:00"},
		{false, []byte{0x41, 0x0A, 0x10, 0x00, 30, 0, 0, 0}, "initiate upload 0x100A:00 size=30"},
		{true, []byte{0x80, 0x0A, 0x10, 0x00, 0, 0, 0, 0x05}, "abort 0x100A:00 0x05000000"},
		{true, []byte{0x60, 0, 0, 0, 0, 0, 0, 0}, "upload segment toggle=0"},
	})
}

func TestTrackerBlockDownload(t *testing.T) {
	tr := NewTracker()
	runSteps(t, tr, []step{
		{true, []byte{0xC6, 0x00, 0x20, 0x00, 10, 0, 0, 0}, "block download initiate 0x2000:00 size=10"},
		{false, []byte{0xA4, 0x00, 0x20, 0x00, 2, 0, 0, 0}, "block download initiate 0x2000:00 blksize=2"},
		{true, []byte{0x01, 1, 2, 3, 4, 5, 6, 7}, "block segment 0x2000:00 seq=1: 01 02 03 04 05 06 07"},
		{true, []byte{0x02, 8, 9, 10, 11, 12, 13, 14}, "block segment 0x2000:00 seq=2: 08 09 0A 0B 0C 0D 0E"},
		{false, []byte{0xA2, 2, 2, 0, 0, 0, 0, 0}, "block download ack 0x2000:00 blksize=2 ackseq=2"},
		{true, []byte{0x81, 1, 2, 3, 0, 0, 0, 0}, "block segment 0x2000:00 seq=1 last: 01 02 03 00 00 00 00"},
		{false, []byte{0xA2, 1, 2, 0, 0, 0, 0, 0}, "block download ack 0x2000:00 blksize=2 ackseq=1"},
		{true, []byte{0xC1 | 4<<2, 0x34, 0x12, 0, 0, 0, 0, 0}, "block download end 0x2000:00 crc=0x1234"},
		{false, []byte{0xA1, 0, 0, 0, 0, 0, 0, 0}, "block download end 0x2000:00"},
		// Idle again: 0x01 is a download segment.
		{true, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}, "download segment toggle=0 last: 00 00 00 00 00 00 00"},
		// An abort during the data phase.
		{true, []byte{0xC0, 0x00, 0x20, 0x00, 0, 0, 0, 0}, "block download initiate 0x2000:00"},
		{false, []byte{0xA0, 0x00, 0x20, 0x00, 4, 0, 0, 0}, "block download initiate 0x2000:00 blksize=4"},
		{true, []byte{0x80, 0x00, 0x20, 0x00, 0, 0, 0x04, 0x05}, "abort 0x2000:00 0x05040000 (SDO protocol timed out)"},
		{true, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}, "download segment toggle=0 last: 00 00 00 00 00 00 00"},
	})
}

func TestTrackerBlockUpload(t *testing.T) {
	tr := NewTracker()
	runSteps(t, tr, []step{
		{true, []byte{0xA4, 0x08, 0x10, 0x00, 2, 0, 0, 0}, "block upload initiate 0x1008:00 blksize=2"},
		{false, []byte{0xC6, 0x08, 0x10, 0x00, 9, 0, 0, 0}, "block upload initiate 0x1008:00 size=9"},
		{true, []byte{0xA3, 0, 0, 0, 0, 0, 0, 0}, "block upload start 0x1008:00"},
		{false, []byte{0x01, 1, 2, 3, 4, 5, 6, 7}, "block segment 0x1008:00 seq=1: 01 02 03 04 05 06 07"},
		{false, []byte{0x82, 8, 9, 0, 0, 0, 0, 0}, "block segment 0x1008:00 seq=2 last: 08 09 00 00 00 00 00"},
		{true, []byte{0xA2, 2, 2, 0, 0, 0, 0, 0}, "block upload ack 0x1008:00 blksize=2 ackseq=2"},
		{false, []byte{0xC1 | 5<<2, 0xCD, 0xAB, 0, 0, 0, 0, 0}, "block upload end 0x1008:00 crc=0xABCD"},
		{true, []byte{0xA1, 0, 0, 0, 0, 0, 0, 0}, "block upload end 0x1008:00"},
		{false, []byte{0x01, 1, 2, 3, 4, 5, 6, 7}, "upload segment toggle=0 last: 01 02 03 04 05 06 07"},
	})
	tr.Reset()
	runSteps(t, tr, []step{
		{true, []byte{0xA4, 0x08, 0x10, 0x00, 2, 0, 0, 0}, "block upload initiate 0x1008:00 blksize=2"},
	})
	// Another node's channel is independent; non-SDO frames pass.
	m := tr.Decode(frame(t, 0x586, 0x01, 1, 2, 3, 4, 5, 6, 7))
	if m.SDO.Phase != PhaseSegment {
		t.Errorf("%s", m)
	}
	if m := tr.Decode(frame(t, 0x80)); m.Kind != KindSYNC {
		t.Errorf("%s", m)
	}
}
