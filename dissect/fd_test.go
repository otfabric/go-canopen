// SPDX-License-Identifier: MIT

package dissect

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/timestamp"
	"github.com/otfabric/go-canopen/usdo"
)

func fdFrame(t testing.TB, id canbus.ID, brs bool, data ...byte) *canbus.Frame {
	t.Helper()
	f, err := canbus.NewFD(id, false, brs, canbus.PadFD(data, 0))
	if err != nil {
		t.Fatal(err)
	}
	return &f
}

func usdoFrame(t testing.TB, m usdo.Message, sender canopen.NodeID, response bool) *canbus.Frame {
	t.Helper()
	f, err := usdo.Frame(m, sender, response, true)
	if err != nil {
		t.Fatal(err)
	}
	return &f
}

func seq(n int, from byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = from + byte(i)
	}
	return b
}

func TestDecodeUSDO(t *testing.T) {
	tests := []struct {
		name  string
		f     *canbus.Frame
		kind  Kind
		node  canopen.NodeID
		s     string
		bad   bool
		check func(*USDO) bool
	}{
		{"upload request", usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdUpload, Session: 3, Index: 0x1018, SubIndex: 1}, 1, false),
			KindUSDORequest, 1, "USDO request node 1: upload to node 5 session 3 0x1018:01", false,
			func(u *USDO) bool { return u.Client && u.HasIndex && u.Dest == 5 && u.CS == 0x11 && u.Session == 3 }},
		{"expedited response", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdUploadExpedited, Session: 3, Index: 0x1018, SubIndex: 1, DataType: 7, Data: []byte{1, 2, 3, 4}}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: upload expedited response to node 1 session 3 0x1018:01 size 4: 01 02 03 04", false,
			func(u *USDO) bool {
				return !u.Client && u.DataType == 7 && u.Size == 4 && u.Data.String() == "01 02 03 04"
			}},
		{"expedited download", usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdDownloadExpedited, Session: 1, Index: 0x2000, Data: []byte{9}}, 1, false),
			KindUSDORequest, 1, "USDO request node 1: download expedited to node 5 session 1 0x2000:00 size 1: 09", false, nil},
		{"expedited ok", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdDownloadExpeditedOK, Session: 1, Index: 0x2000}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: download expedited response to node 1 session 1 0x2000:00", false, nil},
		{"abort", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdAbort, Session: 3, Index: 0x1018, SubIndex: 1, Abort: usdo.AbortTimeout}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: abort to node 1 session 3 0x1018:01 abort 0x12 (USDO time out error)", false,
			func(u *USDO) bool { return u.Abort == 0x12 && u.AbortText == "USDO time out error" }},
		{"abort unknown code", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdAbort, Session: 3, Index: 0x1018, Abort: 0xEE}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: abort to node 1 session 3 0x1018:00 abort 0xEE", false, nil},
		{"bulk initiate broadcast", usdoFrame(t, usdo.Message{Dest: usdo.Broadcast, Cmd: usdo.CmdBulkInit, Session: 2, Index: 0x2000, Size: 1000}, 1, false),
			KindUSDORequest, 1, "USDO request node 1: bulk initiate to broadcast session 2 0x2000:00 size 1000", false,
			func(u *USDO) bool { return u.Size == 1000 && u.Destination == "broadcast" }},
		{"segmented upload initiate", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdUploadInit, Session: 2, Index: 0x2000, Size: 70}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: upload initiate response to node 1 session 2 0x2000:00 size 70", false, nil},
		{"bulk initiate response group", usdoFrame(t, usdo.Message{Dest: 0x81, Cmd: usdo.CmdBulkInitOK, Session: 2, Index: 0x2000, TProc: 10}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: bulk initiate response to group 0x81 session 2 0x2000:00 t_proc 10", false,
			func(u *USDO) bool { return u.TProc == 10 }},
		{"bulk segment", usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdBulkSegment, Session: 2, Counter: 4, Data: seq(usdo.SegmentSize, 0)}, 1, false),
			KindUSDORequest, 1, "USDO request node 1: bulk segment to node 5 session 2 counter 4 size 60: 00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F ...", false,
			func(u *USDO) bool { return !u.HasIndex && u.Counter == 4 && len(u.Data) == 60 }},
		{"bulk last", usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdBulkLast, Session: 2, Data: []byte{9, 9}}, 1, false),
			KindUSDORequest, 1, "USDO request node 1: bulk last segment to node 5 session 2 size 2: 09 09", false, nil},
		{"bulk end response", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdBulkLastOK, Session: 2}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: bulk end response to node 1 session 2", false, nil},
		{"segment confirmation", usdoFrame(t, usdo.Message{Dest: 1, Cmd: usdo.CmdDownloadSegmentOK, Session: 2, Counter: 7}, 5, true),
			KindUSDOResponse, 5, "USDO response node 5: download segment response to node 1 session 2 counter 7", false, nil},
		{"remote service", fdFrame(t, 0x601, false, 5, 0x91, 1, 0xAA, 0xBB),
			KindUSDORequest, 1, "USDO request node 1: remote upload to node 5 session 1 size 2: AA BB", false,
			func(u *USDO) bool { return u.Remote && !u.HasIndex }},
		{"unknown command", fdFrame(t, 0x585, false, 1, 0x55, 1),
			KindUSDOResponse, 5, "USDO response node 5: Command(0x55) to node 1 session 1 [usdo: unknown command specifier: 0x55]", true,
			func(u *USDO) bool { return u.CS == 0x55 && !u.HasIndex }},
		{"short for command", fdFrame(t, 0x601, false, 5, 0x11, 1, 0),
			KindUSDORequest, 1, "USDO request node 1: upload to node 5 session 1 [canopen: protocol error: upload of 4 bytes, 6 needed]", true,
			func(u *USDO) bool { return !u.HasIndex }},
		{"too short", fdFrame(t, 0x601, false, 5, 0x11),
			KindUSDORequest, 1, "usdo-request 601: 05 11 [canopen: protocol error: USDO message of 2 bytes]", true, nil},
		{"no node", fdFrame(t, 0x600, false, 5, 0x11, 1, 0, 0, 0x10),
			KindUnknown, 0, "unknown 600: 05 11 01 00 00 10", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Decode(tt.f)
			if m.Kind != tt.kind || m.Node != tt.node || m.String() != tt.s || (m.Error != "") != tt.bad || !m.FD {
				t.Errorf("got %s node %d %q error %q fd %v", m.Kind, m.Node, m.String(), m.Error, m.FD)
			}
			if tt.check != nil && (m.USDO == nil || !tt.check(m.USDO)) {
				t.Errorf("fields %+v", m.USDO)
			}
		})
	}
}

func TestDecodeFDEMCY(t *testing.T) {
	x := emcy.FDMessage{LogicalDevice: 1, CiASpec: 402, Code: 0x8110, Register: 0x11, Specific: [5]byte{1, 2, 3, 4, 5}, Status: 2,
		Time: timestamp.TimeOfDay{Ms: 10000, Days: 4660}}
	b := x.Bytes()
	reset := emcy.FDMessage{LogicalDevice: 2, Register: 0}.Bytes()
	padded := append(append([]byte{}, b[:]...), 0, 0, 0, 0) // 24 bytes
	tests := []struct {
		name string
		f    *canbus.Frame
		s    string
		bad  bool
	}{
		{"fd emcy", fdFrame(t, 0x85, true, b[:]...),
			"EMCY FD node 5: error 0x8110 register 0x11 info 01 02 03 04 05, logical device 1, CiA 402, status 0x02, time 1996-10-04 00:00:10.000", false},
		{"fd emcy padded", fdFrame(t, 0x85, false, padded...),
			"EMCY FD node 5: error 0x8110 register 0x11 info 01 02 03 04 05, logical device 1, CiA 402, status 0x02, time 1996-10-04 00:00:10.000", false},
		{"fd emcy reset", fdFrame(t, 0x87, false, reset[:]...),
			"EMCY FD node 7: error reset, register 0x00, logical device 2, CiA 0, status 0x00, time 1984-01-01 00:00:00.000", false},
		{"fd emcy short", fdFrame(t, 0x85, false, b[:12]...),
			"emcy 085: 01 00 92 01 10 81 11 01 02 03 04 05 [canopen: protocol error: CANopen FD emergency message with 12 bytes]", true},
		{"classical emcy in fd frame", fdFrame(t, 0x85, false, 0x10, 0x81, 0x11, 1, 2, 3, 4, 5),
			"EMCY node 5: error 0x8110 register 0x11 msef 01 02 03 04 05", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Decode(tt.f)
			if m.Kind != KindEMCY || m.String() != tt.s || (m.Error != "") != tt.bad {
				t.Errorf("got %s %q error %q", m.Kind, m.String(), m.Error)
			}
		})
	}
	m := Decode(fdFrame(t, 0x85, true, b[:]...))
	want := TimeOfDayEpoch.AddDate(0, 0, 4660).Add(10 * time.Second)
	if e := m.EMCY; e == nil || e.FD == nil || e.Code != 0x8110 || e.Register != 0x11 || e.MSEF.String() != "01 02 03 04 05" ||
		e.FD.LogicalDevice != 1 || e.FD.CiASpec != 402 || e.FD.Status != 2 || e.FD.Milliseconds != 10000 || e.FD.Days != 4660 ||
		!e.FD.Time.Equal(want) || !m.BRS {
		t.Errorf("%+v %+v", m.EMCY, m.EMCY.FD)
	}
}

func TestDecodeFDJSON(t *testing.T) {
	b, err := json.Marshal(Decode(usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdUpload, Session: 3, Index: 0x1018, SubIndex: 1}, 1, false)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"kind":"usdo-request"`, `"fd":true`, `"brs":true`, `"usdo":{"client":true,"dest":5,"destination":"node 5","cs":17,"command":"upload","session":3,"index":4120,"subIndex":1,"hasIndex":true}`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("JSON %s lacks %s", b, s)
		}
	}
}

// TestTrackerFD checks that USDO frames of a node do not disturb the
// tracking of its classical SDO transfers.
func TestTrackerFD(t *testing.T) {
	tr := NewTracker()
	steps := []*canbus.Frame{
		frame(t, 0x605, 0x40, 0x08, 0x10, 0x00, 0, 0, 0, 0), // initiate upload 0x1008:00
		usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdUpload, Session: 1, Index: 0x2000}, 5, false),
		frame(t, 0x585, 0x41, 0x08, 0x10, 0x00, 10, 0, 0, 0), // segmented, 10 bytes
		usdoFrame(t, usdo.Message{Dest: 5, Cmd: usdo.CmdAbort, Session: 1, Index: 0x2000, Abort: usdo.AbortNoObject}, 5, true),
		frame(t, 0x605, 0x60, 0, 0, 0, 0, 0, 0, 0), // upload segment
	}
	var m Message
	for _, f := range steps {
		m = tr.Decode(f)
	}
	if m.SDO == nil || !m.SDO.HasIndex || m.SDO.Index != 0x1008 {
		t.Errorf("segment not attributed: %s", m)
	}
}

func TestLSSCommand(t *testing.T) {
	if s := LSSCommand(0x41); s != "switch state selective product code" {
		t.Error(s)
	}
	if s := LSSCommand(0x99); s != "LSS command 0x99" {
		t.Error(s)
	}
}

func FuzzDecodeFD(f *testing.F) {
	add := func(fr *canbus.Frame) { f.Add(uint16(fr.ID), fr.Payload()) }
	add(usdoFrame(f, usdo.Message{Dest: 5, Cmd: usdo.CmdUpload, Session: 3, Index: 0x1018, SubIndex: 1}, 1, false))
	add(usdoFrame(f, usdo.Message{Dest: 1, Cmd: usdo.CmdUploadExpedited, Session: 3, Index: 0x1018, Data: []byte{1, 2}}, 5, true))
	add(usdoFrame(f, usdo.Message{Dest: 5, Cmd: usdo.CmdBulkSegment, Session: 2, Counter: 4, Data: seq(usdo.SegmentSize, 0)}, 1, false))
	add(usdoFrame(f, usdo.Message{Dest: 1, Cmd: usdo.CmdAbort, Session: 3, Abort: usdo.AbortTimeout}, 5, true))
	b := emcy.FDMessage{Code: 0x8110, Register: 0x11}.Bytes()
	f.Add(uint16(0x85), b[:])
	f.Add(uint16(0x601), []byte{5, 0x91, 1})
	f.Add(uint16(0x185), []byte{1, 2, 3})
	tr := NewTracker()
	f.Fuzz(func(t *testing.T, id uint16, data []byte) {
		if len(data) > canbus.MaxFDLen {
			data = data[:canbus.MaxFDLen]
		}
		fr, err := canbus.NewFD(canbus.ID(id&0x7FF), false, id&0x8000 != 0, canbus.PadFD(data, 0))
		if err != nil {
			t.Fatal(err)
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
