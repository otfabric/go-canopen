// SPDX-License-Identifier: MIT

package dissect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	rec "github.com/otfabric/go-canopen/internal/capture"
)

// copleyExcerpts are the committed excerpts of the Copley Controls logs
// (testdata/captures/README.md).
var copleyExcerpts = []string{
	"copley/absolute-move.excerpt.json",
	"copley/linkage-home-and-move.excerpt.json",
	"copley/pvt-move.excerpt.json",
}

func loadExcerpt(t *testing.T, rel string) []rec.Record {
	t.Helper()
	_, recs, err := rec.LoadExcerpt(rel)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// checkLog runs a log through a Tracker and checks what holds for every
// recorded Copley exchange: no frame is malformed, every SDO response
// answers the request before it on the same node (same object, the
// matching command), every node guarding request is answered by a guard
// response, and every frame on an identifier the drives were configured
// to use decodes as that PDO. It returns the decoded messages.
func checkLog(t *testing.T, name string, recs []rec.Record) []Message {
	t.Helper()
	tr := NewTracker()
	out := make([]Message, len(recs))
	pending := map[canopen.NodeID]*SDO{}
	guards := map[canopen.NodeID]int{}
	for i := range recs {
		r := &recs[i]
		m := tr.Decode(&r.Frame)
		out[i] = m
		where := fmt.Sprintf("%s line %d (%s)", name, r.Line, m)
		if m.Error != "" {
			t.Errorf("%s: %s", where, m.Error)
		}
		switch m.Kind {
		case KindSDORequest:
			if p := pending[m.Node]; p != nil {
				t.Errorf("%s: request while 0x%04X:%02X is unanswered", where, p.Index, p.SubIndex)
			}
			pending[m.Node] = m.SDO
		case KindSDOResponse:
			req := pending[m.Node]
			delete(pending, m.Node)
			switch {
			case req == nil:
				t.Errorf("%s: response without a request", where)
			case !m.SDO.HasIndex || m.SDO.Index != req.Index || m.SDO.SubIndex != req.SubIndex:
				t.Errorf("%s: answers 0x%04X:%02X", where, req.Index, req.SubIndex)
			case req.CS == CCSInitiateUpload && m.SDO.CS != SCSInitiateUpload,
				req.CS == CCSInitiateDownload && m.SDO.CS != SCSInitiateDownload:
				t.Errorf("%s: answers %s", where, req.Command)
			}
		case KindGuardRequest:
			guards[m.Node]++
		case KindGuardResponse:
			guards[m.Node]--
			if guards[m.Node] != 0 {
				t.Errorf("%s: guard response without a request", where)
			}
		case KindHeartbeat:
			// The drives' heartbeat producer is off (0x1017 = 0): every
			// error control frame answers a node guarding request.
			t.Errorf("%s: heartbeat from a guarded Copley drive", where)
		default: // no special case
		}
	}
	return out
}

// TestCopleyExcerpts decodes the excerpts of the Copley Controls logs.
func TestCopleyExcerpts(t *testing.T) {
	for _, rel := range copleyExcerpts {
		recs := loadExcerpt(t, rel)
		if len(recs) < 100 {
			t.Fatalf("%s: %d records", rel, len(recs))
		}
		checkLog(t, rel, recs)
	}
}

// byLine returns the message decoded from a line of the log.
func byLine(t *testing.T, recs []rec.Record, msgs []Message, line int) Message {
	t.Helper()
	for i, r := range recs {
		if r.Line == line {
			return msgs[i]
		}
	}
	t.Fatalf("line %d not in the excerpt", line)
	return Message{}
}

// TestCopleyGuarding: the drive answers every node guarding request (RTR
// on 0x701) with its state and an alternating toggle bit; a Tracker calls
// both toggle values guard responses (Decode alone took the ones with the
// toggle bit clear for heartbeats).
func TestCopleyGuarding(t *testing.T) {
	recs := loadExcerpt(t, "copley/absolute-move.excerpt.json")
	msgs := checkLog(t, "absolute-move", recs)
	for _, c := range []struct {
		line   int
		kind   Kind
		toggle bool
	}{
		{426, KindGuardRequest, false},
		{427, KindGuardResponse, true}, // 0x85: the first answer has the toggle bit set
		{1868, KindGuardRequest, false},
		{1869, KindGuardResponse, false}, // 0x05
	} {
		m := byLine(t, recs, msgs, c.line)
		if m.Kind != c.kind || c.kind == KindGuardResponse && (m.ErrorControl.State != canopen.StateOperational || m.ErrorControl.Toggle != c.toggle) {
			t.Errorf("line %d: %s, want %s toggle=%v", c.line, m, c.kind, c.toggle)
		}
	}
	// Decode alone cannot tell a guard response with the toggle bit clear
	// from a heartbeat.
	f := recs[0].Frame
	for _, r := range recs {
		if r.Line == 1869 {
			f = r.Frame
		}
	}
	if m := Decode(&f); m.Kind != KindHeartbeat {
		t.Errorf("Decode: %s", m)
	}
}

// TestCopleyConfiguredPDOs: the drive's TPDO5 is configured to COB-ID
// 0x180 (0x1804:01, lines 449..460) and maps the high-resolution time
// stamp 0x1013 (0x1A04:01 = 0x10130020); the predefined connection set
// has no meaning for 0x180 (node-ID 0). Before the master reads the
// configuration, the frame is unknown; after it, the drive's TPDO5. In
// the two-drive log, drive 2 produces it (TPDO5) and drive 3 consumes it
// (RPDO5 on 0x180, lines 345..356): the frame is drive 2's TPDO5.
func TestCopleyConfiguredPDOs(t *testing.T) {
	recs := loadExcerpt(t, "copley/absolute-move.excerpt.json")
	msgs := checkLog(t, "absolute-move", recs)
	first, last := -1, -1
	for i, r := range recs {
		if r.Frame.ID != 0x180 || r.Frame.IsRemote() {
			continue
		}
		m := msgs[i]
		switch {
		case r.Line < 449 && m.Kind != KindUnknown:
			t.Errorf("line %d before the configuration: %s", r.Line, m)
		case r.Line > 460 && (m.Kind != KindTPDO || m.Node != 1 || m.PDO.Number != 5 || !m.PDO.Configured):
			t.Errorf("line %d: %s, want TPDO5 node 1", r.Line, m)
		case r.Line > 460:
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		t.Fatal("no TPDO5 frame after the configuration")
	}
	// The time stamp counts microseconds: it advances by the time between
	// the frames (0x1006 = 10 ms SYNC, transmission type 10: every 100 ms),
	// within 1 ms.
	ts := func(i int) uint32 {
		d := recs[i].Frame.Payload()
		return uint32(d[0]) | uint32(d[1])<<8 | uint32(d[2])<<16 | uint32(d[3])<<24
	}
	dStamp := int64(ts(last) - ts(first))
	dLog := int64(recs[last].Micros - recs[first].Micros)
	if dStamp-dLog > 1000 || dLog-dStamp > 1000 {
		t.Errorf("0x1013 advanced %d µs, the log %d µs", dStamp, dLog)
	}

	recs = loadExcerpt(t, "copley/linkage-home-and-move.excerpt.json")
	msgs = checkLog(t, "linkage", recs)
	n := 0
	for i, r := range recs {
		if r.Frame.ID == 0x180 && r.Line > 356 {
			n++
			if m := msgs[i]; m.Kind != KindTPDO || m.Node != 2 || m.PDO.Number != 5 {
				t.Errorf("linkage line %d: %s, want TPDO5 node 2", r.Line, m)
			}
		}
	}
	if n == 0 {
		t.Error("linkage: no 0x180 frame after the configuration")
	}
}

// TestCopleyIdentity: the drives identify themselves in 0x1018 (Copley
// Controls is CiA vendor 0x000000AB).
func TestCopleyIdentity(t *testing.T) {
	for _, c := range []struct {
		rel     string
		node    canopen.NodeID
		product uint32
		rev     uint32
		serial  uint32
	}{
		{"copley/absolute-move.excerpt.json", 1, 0x1100, 0x00010004, 0x00E735B9},
		{"copley/pvt-move.excerpt.json", 2, 0x1060, 0x00010004, 0x002FBB62},
	} {
		recs := loadExcerpt(t, c.rel)
		msgs := checkLog(t, c.rel, recs)
		got := map[uint8]uint32{}
		for _, m := range msgs {
			if m.Kind == KindSDOResponse && m.Node == c.node && m.SDO.Index == 0x1018 && len(m.SDO.Data) == 4 {
				d := m.SDO.Data
				got[m.SDO.SubIndex] = uint32(d[0]) | uint32(d[1])<<8 | uint32(d[2])<<16 | uint32(d[3])<<24
			}
		}
		want := map[uint8]uint32{1: 0xAB, 2: c.product, 3: c.rev, 4: c.serial}
		for sub, v := range want {
			if got[sub] != v {
				t.Errorf("%s node %d 0x1018:%02X = 0x%08X, want 0x%08X", c.rel, c.node, sub, got[sub], v)
			}
		}
	}
}

// TestCopleyEmergencies: the drive confirms its error-free state after NMT
// start (error reset, line 429) and, after the master stops guarding,
// reports the life guarding error 0x8130 and the manufacturer-specific
// 0x61FF with error register 0x81 (generic and manufacturer-specific bits).
func TestCopleyEmergencies(t *testing.T) {
	recs := loadExcerpt(t, "copley/absolute-move.excerpt.json")
	msgs := checkLog(t, "absolute-move", recs)
	for _, c := range []struct {
		line int
		code uint16
		reg  uint8
	}{{429, 0x0000, 0x00}, {1938, 0x8130, 0x81}, {1940, 0x61FF, 0x81}} {
		m := byLine(t, recs, msgs, c.line)
		if m.EMCY == nil || m.EMCY.Code != c.code || m.EMCY.Register != c.reg || m.EMCY.Reset != (c.code == 0) {
			t.Errorf("line %d: %s", c.line, m)
		}
	}
}

// TestRecordedFramesMatchWireshark compares the decode of every recorded
// frame of the committed corpus (the Copley excerpts and the Wireshark
// sample capture) with Wireshark's CANopen dissector.
func TestRecordedFramesMatchWireshark(t *testing.T) {
	bin := tshark(t)
	var frames []canbus.Frame
	for _, rel := range copleyExcerpts {
		for _, r := range loadExcerpt(t, rel) {
			frames = append(frames, r.Frame)
		}
	}
	frames = append(frames, readPCAP(t, filepath.Join(rec.Dir(), "wireshark", "canopen-sample.pcap"))...)
	compareWithWireshark(t, bin, frames)
}

func readPCAP(t *testing.T, path string) []canbus.Frame {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := rec.ReadSocketCANPCAP(b)
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

// TestWiresharkSampleCapture decodes the Wireshark wiki's CANopen sample
// capture (SampleCaptures/CANopen.pcap, 34 frames, origin not stated):
// boot-ups, NMT, SDO uploads and downloads answered with aborts, an
// upload segment exchange cut by aborts, LSS identify non-configured
// slave, and a block upload aborted at once.
func TestWiresharkSampleCapture(t *testing.T) {
	frames := readPCAP(t, filepath.Join(rec.Dir(), "wireshark", "canopen-sample.pcap"))
	if len(frames) != 34 {
		t.Fatalf("%d frames, want 34", len(frames))
	}
	tr := NewTracker()
	kinds := map[Kind]int{}
	aborts := 0
	for i := range frames {
		m := tr.Decode(&frames[i])
		kinds[m.Kind]++
		if m.SDO != nil && m.SDO.Phase == PhaseAbort {
			aborts++
			if m.SDO.AbortText == "" {
				t.Errorf("frame %d: abort 0x%08X has no text", i+1, uint32(m.SDO.Abort))
			}
		}
	}
	want := map[Kind]int{KindBootUp: 4, KindNMT: 4, KindSDORequest: 13, KindSDOResponse: 8, KindLSSMaster: 1, KindLSSSlave: 4}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%s: %d, want %d (all: %v)", k, kinds[k], n, kinds)
		}
	}
	if aborts != 10 {
		t.Errorf("%d aborts, want 10", aborts)
	}
}

// TestCopleyExtended runs the whole Copley logs (make captures-extended)
// through the same checks, compares all 16,527 frames with Wireshark's
// dissector (when tshark is installed) and requires the excerpts to be
// verbatim lines of the logs.
func TestCopleyExtended(t *testing.T) {
	dir, ok := rec.ExtendedDir()
	if !ok {
		t.Skip("extended corpus not fetched: make captures-extended")
	}
	logs := map[string]string{
		"absolute-move":         "copley/Absolute Move",
		"relative-move":         "copley/Relative Move",
		"linkage-home-and-move": "copley/Linkage Home and Move",
		"pvt-move":              "copley/PVT Move",
	}
	var all []canbus.Frame
	defer func() {
		if bin, err := exec.LookPath("tshark"); err == nil && !t.Failed() {
			compareWithWireshark(t, bin, all)
		}
	}()
	for name, file := range logs {
		recs, err := rec.ReadCopleyFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			all = append(all, r.Frame)
		}
		msgs := checkLog(t, name, recs)
		kinds := map[Kind]int{}
		for _, m := range msgs {
			kinds[m.Kind]++
		}
		t.Logf("%s: %d frames %v", name, len(recs), kinds)
		if name == "relative-move" {
			continue
		}
		ex, exRecs, err := rec.LoadExcerpt("copley/" + name + ".excerpt.json")
		if err != nil {
			t.Fatal(err)
		}
		at := map[int]rec.Record{}
		for _, r := range recs {
			at[r.Line] = r
		}
		for i, r := range exRecs {
			if at[r.Line].Frame.String() != r.Frame.String() || at[r.Line].Micros != r.Micros {
				t.Errorf("%s: excerpt line %d %q differs from the log", name, r.Line, ex.Raw[i].Text)
			}
		}
	}
}
