// SPDX-License-Identifier: MIT

package dissect

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func frame(t testing.TB, id canbus.ID, data ...byte) *canbus.Frame {
	t.Helper()
	f, err := canbus.NewStandard(id, data)
	if err != nil {
		t.Fatal(err)
	}
	return &f
}

func rtr(t testing.TB, id canbus.ID, n uint8) *canbus.Frame {
	t.Helper()
	f, err := canbus.NewRemote(id, false, n)
	if err != nil {
		t.Fatal(err)
	}
	return &f
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name string
		f    *canbus.Frame
		kind Kind
		node canopen.NodeID
		s    string
		bad  bool
	}{
		{"nmt start", frame(t, 0, 0x01, 0x05), KindNMT, 0, "NMT start node 5", false},
		{"nmt reset all", frame(t, 0, 0x81, 0x00), KindNMT, 0, "NMT reset-node all nodes", false},
		{"nmt unknown", frame(t, 0, 0x03, 0x00), KindNMT, 0, "NMT NMTCommand(0x03) all nodes [unknown NMT command 0x03]", true},
		{"nmt short", frame(t, 0, 0x01), KindNMT, 0, "NMT start all nodes [NMT needs 2 bytes, got 1]", true},
		{"nmt empty", frame(t, 0), KindNMT, 0, "nmt 000:  [NMT needs 2 bytes, got 0]", true},
		{"sync", frame(t, 0x80), KindSYNC, 0, "SYNC", false},
		{"sync counter", frame(t, 0x80, 7), KindSYNC, 0, "SYNC counter=7", false},
		{"sync long", frame(t, 0x80, 7, 8), KindSYNC, 0, "SYNC [SYNC with 2 bytes]", true},
		{"time", frame(t, 0x100, 0x10, 0x27, 0, 0xF0, 0x34, 0x12), KindTIME, 0, "TIME 1996-10-04 00:00:10.000", false},
		{"time short", frame(t, 0x100, 1, 2), KindTIME, 0, "time 100: 01 02 [canopen: protocol error: TIME_OF_DAY needs 6 bytes, got 2]", true},
		{"emcy", frame(t, 0x85, 0x10, 0x81, 0x11, 1, 2, 3, 4, 5), KindEMCY, 5, "EMCY node 5: error 0x8110 register 0x11 msef 01 02 03 04 05", false},
		{"emcy reset", frame(t, 0x85, 0, 0, 0, 0, 0, 0, 0, 0), KindEMCY, 5, "EMCY node 5: error reset, register 0x00", false},
		{"emcy short", frame(t, 0x85, 0, 0, 0), KindEMCY, 5, "EMCY node 5: error reset, register 0x00 [EMCY needs 8 bytes, got 3]", true},
		{"emcy tiny", frame(t, 0x85, 0), KindEMCY, 5, "emcy 085: 00 [EMCY needs 8 bytes, got 1]", true},
		{"tpdo", frame(t, 0x185, 1, 2, 3), KindTPDO, 5, "TPDO1 node 5: 01 02 03", false},
		{"rpdo4", frame(t, 0x57F, 9), KindRPDO, 127, "RPDO4 node 127: 09", false},
		{"tpdo rtr", rtr(t, 0x285, 2), KindTPDO, 5, "TPDO2 node 5: RTR", false},
		{"rpdo rtr", rtr(t, 0x305, 2), KindRPDO, 5, "RPDO2 node 5: RTR", false},
		{"guard request", rtr(t, 0x705, 1), KindGuardRequest, 5, "node guarding request node 5", false},
		{"guard reply", frame(t, 0x705, 0x85), KindGuardResponse, 5, "node guarding node 5: operational toggle=1", false},
		{"heartbeat", frame(t, 0x705, 0x7F), KindHeartbeat, 5, "heartbeat node 5: pre-operational", false},
		{"boot-up", frame(t, 0x705, 0x00), KindBootUp, 5, "boot-up node 5", false},
		{"heartbeat long", frame(t, 0x705, 0x05, 0), KindHeartbeat, 5, "heartbeat node 5: operational [NMT error control needs 1 byte, got 2]", true},
		{"heartbeat empty", frame(t, 0x705), KindHeartbeat, 5, "heartbeat 705:  [NMT error control needs 1 byte, got 0]", true},
		{"lss master", frame(t, 0x7E5, 0x04, 1, 0, 0, 0, 0, 0, 0), KindLSSMaster, 0, "LSS master: switch state global (01 00 00 00 00 00 00)", false},
		{"lss slave", frame(t, 0x7E4, 0x5E, 9, 0, 0, 0, 0, 0, 0), KindLSSSlave, 0, "LSS slave: inquire node-ID (09 00 00 00 00 00 00)", false},
		{"lss unknown", frame(t, 0x7E4, 0x99), KindLSSSlave, 0, "LSS slave: LSS command 0x99 () [LSS needs 8 bytes, got 1]", true},
		{"lss empty", frame(t, 0x7E4), KindLSSSlave, 0, "lss-slave 7E4:  [empty LSS message]", true},
		{"sdo upload", frame(t, 0x605, 0x40, 0x18, 0x10, 1, 0, 0, 0, 0), KindSDORequest, 5, "SDO request node 5: initiate upload 0x1018:01", false},
		{"sdo upload resp", frame(t, 0x585, 0x43, 0x18, 0x10, 1, 0x78, 0x56, 0x34, 0x12), KindSDOResponse, 5,
			"SDO response node 5: initiate upload 0x1018:01 expedited: 78 56 34 12", false},
		{"unknown id", frame(t, 0x7F0, 1), KindUnknown, 0, "unknown 7F0: 01", false},
		{"unknown rtr", rtr(t, 0x080, 0), KindUnknown, 0, "unknown 080 RTR [remote request on a COB that has none]", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Decode(tt.f)
			if m.Kind != tt.kind || m.Node != tt.node || m.String() != tt.s || (m.Error != "") != tt.bad {
				t.Errorf("got kind %s node %d %q error %q", m.Kind, m.Node, m.String(), m.Error)
			}
			if _, err := json.Marshal(m); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestDecodeExtendedAndFD(t *testing.T) {
	f, _ := canbus.NewExtended(0x185, []byte{1})
	if m := Decode(&f); m.Kind != KindUnknown || m.String() != "unknown 00000185: 01" {
		t.Errorf("%s", m)
	}
	f, _ = canbus.NewFD(0x185, false, false, []byte{1})
	if m := Decode(&f); m.Kind != KindUnknown {
		t.Errorf("%s", m)
	}
	r, _ := canbus.NewRemote(0x1234, true, 0)
	if m := Decode(&r); m.String() != "unknown 00001234 RTR" {
		t.Errorf("%s", m)
	}
}

func TestDecodeFields(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := frame(t, 0x100, 0xFF, 0xFF, 0xFF, 0xFF, 0x01, 0x00)
	f.Timestamp = ts
	m := Decode(f)
	// 0x0FFFFFFF ms (the top four bits are reserved) after 1984-01-02.
	want := TimeOfDayEpoch.AddDate(0, 0, 1).Add(0x0FFFFFFF * time.Millisecond)
	if m.TIME.Milliseconds != 0x0FFFFFFF || m.TIME.Days != 1 || !m.TIME.Time.Equal(want) || !m.Time.Equal(ts) {
		t.Errorf("%+v", m.TIME)
	}
	m = Decode(frame(t, 0, 0x82, 0x7F))
	if m.NMT.Command != canopen.NMTResetCommunication || m.NMT.Target != 127 || m.NMT.Name != "reset-communication" {
		t.Errorf("%+v", m.NMT)
	}
	m = Decode(frame(t, 0x7E5, 0x11, 0x22, 0x33, 0x44, 0x55, 0, 0, 0))
	if m.LSS.Value != 0x55443322 || m.LSS.CS != 0x11 || m.LSS.Command != "configure node-ID" {
		t.Errorf("%+v", m.LSS)
	}
	b, err := json.Marshal(Decode(frame(t, 0x185, 0xAB, 0xCD)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"kind":"tpdo"`, `"data":"AB CD"`, `"summary":"TPDO1 node 5: AB CD"`, `"pdo":{"number":1,"transmit":true}`, `"node":5`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("JSON %s lacks %s", b, s)
		}
	}
}

func TestSDO(t *testing.T) {
	tests := []struct {
		name   string
		client bool
		data   []byte
		s      string
		check  func(*SDO) bool
		bad    bool
	}{
		{"initiate download expedited 2", true, []byte{0x2B, 0x17, 0x10, 0x00, 0xE8, 0x03, 0, 0}, "initiate download 0x1017:00 expedited: E8 03",
			func(s *SDO) bool {
				return s.Expedited && s.SizeIndicated && s.Size == 2 && s.CS == 1 && s.Phase == PhaseInitiate
			}, false},
		{"initiate download expedited unsized", true, []byte{0x22, 0x00, 0x20, 0x01, 1, 2, 3, 4}, "initiate download 0x2000:01 expedited: 01 02 03 04",
			func(s *SDO) bool { return s.Expedited && !s.SizeIndicated && s.Size == 4 }, false},
		{"initiate download segmented", true, []byte{0x21, 0x08, 0x10, 0x00, 20, 0, 0, 0}, "initiate download 0x1008:00 size=20",
			func(s *SDO) bool { return !s.Expedited && s.SizeIndicated && s.Size == 20 }, false},
		{"initiate download response", false, []byte{0x60, 0x08, 0x10, 0x00, 0, 0, 0, 0}, "initiate download 0x1008:00",
			func(s *SDO) bool { return s.CS == 3 && s.HasIndex }, false},
		{"download segment", true, []byte{0x00, 'a', 'b', 'c', 'd', 'e', 'f', 'g'}, "download segment toggle=0: 61 62 63 64 65 66 67",
			func(s *SDO) bool { return !s.Last && s.Size == 7 && !s.HasIndex }, false},
		{"download segment last", true, []byte{0x1B, 'h', 'i', 0, 0, 0, 0, 0}, "download segment toggle=1 last: 68 69",
			func(s *SDO) bool { return s.Last && s.Toggle == 1 && s.Size == 2 }, false},
		{"download segment response", false, []byte{0x30, 0, 0, 0, 0, 0, 0, 0}, "download segment toggle=1",
			func(s *SDO) bool { return s.CS == 1 && s.Toggle == 1 }, false},
		{"initiate upload response segmented", false, []byte{0x41, 0x08, 0x10, 0x00, 9, 0, 0, 0}, "initiate upload 0x1008:00 size=9",
			func(s *SDO) bool { return s.Size == 9 }, false},
		{"upload segment request", true, []byte{0x70, 0, 0, 0, 0, 0, 0, 0}, "upload segment toggle=1",
			func(s *SDO) bool { return s.CS == 3 }, false},
		{"upload segment response", false, []byte{0x0B, 'x', 'y', 0, 0, 0, 0, 0}, "upload segment toggle=0 last: 78 79",
			func(s *SDO) bool { return s.CS == 0 && s.Last && s.Size == 2 }, false},
		{"abort", false, []byte{0x80, 0x00, 0x20, 0x01, 0x00, 0x00, 0x02, 0x06}, "abort 0x2000:01 0x06020000 (object does not exist in the object dictionary)",
			func(s *SDO) bool { return s.Abort == canopen.AbortNoObject && s.Phase == PhaseAbort }, false},
		{"abort unknown code", true, []byte{0x80, 0x00, 0x20, 0x01, 0x01, 0x00, 0x00, 0x00}, "abort 0x2000:01 0x00000001",
			func(s *SDO) bool { return s.AbortText == "" }, false},
		{"block download initiate", true, []byte{0xC6, 0x00, 0x20, 0x00, 0x00, 0x01, 0, 0}, "block download initiate 0x2000:00 size=256",
			func(s *SDO) bool { return s.CRCSupport && s.Size == 256 && s.Sub == 0 }, false},
		{"block download initiate response", false, []byte{0xA4, 0x00, 0x20, 0x00, 127, 0, 0, 0}, "block download initiate 0x2000:00 blksize=127",
			func(s *SDO) bool { return s.CRCSupport && s.BlockSize == 127 }, false},
		{"block download ack", false, []byte{0xA2, 5, 127, 0, 0, 0, 0, 0}, "block download ack blksize=127 ackseq=5",
			func(s *SDO) bool { return s.AckSeq == 5 && s.Phase == PhaseBlock }, false},
		{"block download end", true, []byte{0xC1 | 3<<2, 0x34, 0x12, 0, 0, 0, 0, 0}, "block download end crc=0x1234",
			func(s *SDO) bool { return s.CRC == 0x1234 && s.Size == 4 && s.Phase == PhaseEnd }, false},
		{"block download end response", false, []byte{0xA1, 0, 0, 0, 0, 0, 0, 0}, "block download end",
			func(s *SDO) bool { return s.Sub == 1 }, false},
		{"block upload initiate", true, []byte{0xA4, 0x00, 0x20, 0x00, 64, 10, 0, 0}, "block upload initiate 0x2000:00 blksize=64",
			func(s *SDO) bool { return s.CRCSupport && s.Threshold == 10 }, false},
		{"block upload initiate response", false, []byte{0xC6, 0x00, 0x20, 0x00, 100, 0, 0, 0}, "block upload initiate 0x2000:00 size=100",
			func(s *SDO) bool { return s.CRCSupport && s.Size == 100 }, false},
		{"block upload start", true, []byte{0xA3, 0, 0, 0, 0, 0, 0, 0}, "block upload start",
			func(s *SDO) bool { return s.Sub == 3 }, false},
		{"block upload ack", true, []byte{0xA2, 64, 64, 0, 0, 0, 0, 0}, "block upload ack blksize=64 ackseq=64",
			func(s *SDO) bool { return s.AckSeq == 64 }, false},
		{"block upload end", false, []byte{0xC9, 0xCD, 0xAB, 0, 0, 0, 0, 0}, "block upload end crc=0xABCD",
			func(s *SDO) bool { return s.Size == 5 && s.CRC == 0xABCD }, false},
		{"block upload end response", true, []byte{0xA1, 0, 0, 0, 0, 0, 0, 0}, "block upload end",
			func(s *SDO) bool { return s.Phase == PhaseEnd }, false},
		{"server ss 3 unknown", false, []byte{0xA3, 0, 0, 0, 0, 0, 0, 0}, "unknown command 0xA3",
			func(s *SDO) bool { return s.Phase == PhaseUnknown }, true},
		{"ccs 7", true, []byte{0xE0, 0, 0, 0, 0, 0, 0, 0}, "unknown command 0xE0",
			func(s *SDO) bool { return s.CS == 7 }, true},
		{"short", true, []byte{0x40, 0x18}, "initiate upload 0x0018:00",
			func(s *SDO) bool { return s.Index == 0x18 }, true},
		{"short expedited", false, []byte{0x43, 0x18, 0x10, 0x01, 0x78}, "initiate upload 0x1018:01 expedited: 78",
			func(s *SDO) bool { return len(s.Data) == 1 }, true},
		{"empty", true, nil, "", func(s *SDO) bool { return s.Phase == PhaseUnknown }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, problem := decodeSDO(tt.data, tt.client)
			if s.String() != tt.s || !tt.check(s) || (problem != "") != tt.bad {
				t.Errorf("got %q %+v problem %q", s.String(), s, problem)
			}
		})
	}
	s, p := decodeBlockSegment([]byte{0x85, 1, 2}, true)
	if s.Seq != 5 || !s.Last || s.String() != "block segment seq=5 last: 01 02" || p == "" {
		t.Errorf("%q %q", s, p)
	}
	if _, p := decodeBlockSegment(nil, true); p == "" {
		t.Error("empty segment")
	}
}

func FuzzDecode(f *testing.F) {
	seeds := []struct {
		id   uint16
		data []byte
	}{
		{0x000, []byte{1, 5}}, {0x080, nil}, {0x080, []byte{1}}, {0x100, []byte{1, 2, 3, 4, 5, 6}},
		{0x085, []byte{0x10, 0x81, 0x11, 1, 2, 3, 4, 5}}, {0x185, []byte{1, 2}},
		{0x605, []byte{0x40, 0x18, 0x10, 1, 0, 0, 0, 0}}, {0x585, []byte{0x43, 0x18, 0x10, 1, 0x78, 0x56, 0x34, 0x12}},
		{0x605, []byte{0xC6, 0, 0x20, 0, 0, 1, 0, 0}}, {0x585, []byte{0xA4, 0, 0x20, 0, 127, 0, 0, 0}},
		{0x705, []byte{0x85}}, {0x7E5, []byte{0x04, 1, 0, 0, 0, 0, 0, 0}},
	}
	for _, s := range seeds {
		f.Add(s.id, s.data, false)
	}
	f.Add(uint16(0x705), []byte{}, true)
	tr := NewTracker()
	f.Fuzz(func(t *testing.T, id uint16, data []byte, remote bool) {
		if len(data) > 8 {
			data = data[:8]
		}
		var fr canbus.Frame
		if remote {
			fr, _ = canbus.NewRemote(canbus.ID(id&0x7FF), false, uint8(len(data)))
		} else {
			fr, _ = canbus.NewStandard(canbus.ID(id&0x7FF), data)
		}
		m := Decode(&fr)
		_ = m.String()
		if _, err := json.Marshal(m); err != nil {
			t.Fatal(err)
		}
		m = tr.Decode(&fr)
		_ = m.String()
	})
}

func canbusID(x uint16) canbus.ID { return canbus.ID(x) }
