// SPDX-License-Identifier: MIT

package dissect

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/capture"
)

// wiresharkFields are the CANopen dissector fields the oracle compares.
var wiresharkFields = []string{
	"canopen.function_code", "canopen.node_id",
	"canopen.nmt_ctrl.cd", "canopen.nmt_ctrl.node_id",
	"canopen.sync.counter",
	"canopen.em.err_code", "canopen.em.err_reg",
	"canopen.sdo.ccs", "canopen.sdo.scs", "canopen.sdo.main_idx", "canopen.sdo.sub_idx", "canopen.sdo.abort_code",
	"canopen.time_stamp_ms", "canopen.time_stamp_days",
	"canopen.nmt_guard.state", "canopen.nmt_guard.toggle",
	"canopen.lss.cs",
	"canopen.pdo.data.bytes",
}

// tshark returns the path of tshark. Without it the test skips, unless
// CANOPEN_REQUIRE_TSHARK=1, where it fails.
func tshark(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("tshark")
	if err != nil {
		msg := "tshark not found: install Wireshark to compare with its CANopen dissector " +
			"(the test runs: tshark -r trace.pcap -d can.subdissector,canopen -T fields ...)"
		if os.Getenv("CANOPEN_REQUIRE_TSHARK") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg + " (CANOPEN_REQUIRE_TSHARK=1 makes this a failure)")
	}
	return p
}

// TestWireshark writes frames of every service to a pcap and compares the
// decoded fields with Wireshark's CANopen dissector, selected with the
// "Decode As" table can.subdissector.
func TestWireshark(t *testing.T) {
	bin := tshark(t)
	var frames []canbus.Frame
	add := func(id canbus.ID, data ...byte) {
		f, err := canbus.NewStandard(id, data)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	add(0x000, 0x01, 0x05)
	add(0x000, 0x82, 0x00)
	add(0x080)
	add(0x080, 0x2A)
	add(0x100, 0x10, 0x27, 0x00, 0x00, 0x34, 0x12)
	add(0x08A, 0x10, 0x81, 0x11, 1, 2, 3, 4, 5)
	add(0x08A, 0x00, 0x00, 0x00, 0, 0, 0, 0, 0)
	add(0x185, 1, 2, 3)
	add(0x2FF, 0xDE, 0xAD, 0xBE, 0xEF, 0, 1, 2, 3)
	add(0x405)
	add(0x605, 0x40, 0x18, 0x10, 0x01, 0, 0, 0, 0)
	add(0x585, 0x43, 0x18, 0x10, 0x01, 0x78, 0x56, 0x34, 0x12)
	add(0x605, 0x2B, 0x17, 0x10, 0x00, 0xE8, 0x03, 0, 0)
	add(0x585, 0x60, 0x17, 0x10, 0x00, 0, 0, 0, 0)
	add(0x605, 0x21, 0x08, 0x10, 0x00, 9, 0, 0, 0)
	add(0x605, 0x00, 1, 2, 3, 4, 5, 6, 7)
	add(0x585, 0x20, 0, 0, 0, 0, 0, 0, 0)
	add(0x605, 0x60, 0, 0, 0, 0, 0, 0, 0)
	add(0x585, 0x0B, 'a', 'b', 0, 0, 0, 0, 0)
	add(0x585, 0x80, 0x00, 0x20, 0x01, 0x00, 0x00, 0x02, 0x06)
	add(0x605, 0xC6, 0x00, 0x20, 0x00, 0x00, 0x01, 0, 0)
	add(0x585, 0xA4, 0x00, 0x20, 0x00, 127, 0, 0, 0)
	add(0x605, 0xA4, 0x08, 0x10, 0x00, 64, 10, 0, 0)
	add(0x585, 0xC6, 0x08, 0x10, 0x00, 100, 0, 0, 0)
	add(0x705, 0x00)
	add(0x705, 0x05)
	add(0x705, 0x7F)
	add(0x705, 0x84)
	add(0x7E5, 0x04, 0x01, 0, 0, 0, 0, 0, 0)
	add(0x7E5, 0x11, 0x0A, 0, 0, 0, 0, 0, 0)
	add(0x7E4, 0x11, 0x00, 0, 0, 0, 0, 0, 0)
	add(0x7E5, 0x5E, 0, 0, 0, 0, 0, 0, 0)

	path := filepath.Join(t.TempDir(), "canopen.pcap")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := capture.NewPCAPWriter(fh)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	for i := range frames {
		frames[i].Timestamp = base.Add(time.Duration(i) * time.Millisecond)
		if err := w.Write(&frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()

	args := []string{"-r", path, "-d", "can.subdissector,canopen", "-T", "fields", "-E", "separator=|", "-E", "occurrence=f"}
	for _, f := range wiresharkFields {
		args = append(args, "-e", f)
	}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		t.Fatalf("tshark %s: %v", strings.Join(args, " "), err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(frames) {
		t.Fatalf("tshark returned %d packets for %d frames:\n%s", len(lines), len(frames), out)
	}
	for i, line := range lines {
		cols := strings.Split(line, "|")
		ws := map[string]string{}
		for j, name := range wiresharkFields {
			if j < len(cols) && cols[j] != "" {
				ws[name] = cols[j]
			}
		}
		m := Decode(&frames[i])
		for name, want := range expected(m) {
			got, ok := ws[name]
			if !ok {
				t.Errorf("frame %d (%s): Wireshark has no %s, we have %s", i, m, name, want)
				continue
			}
			if normalize(got) != normalize(want) {
				t.Errorf("frame %d (%s): %s: Wireshark %s, we %s", i, m, name, got, want)
			}
		}
	}
}

// expected returns the Wireshark field values a message should have.
func expected(m Message) map[string]string {
	u := func(x any) string { return fmt.Sprint(x) }
	e := map[string]string{"canopen.function_code": u(uint32(m.ID) >> 7)}
	if m.Node != 0 {
		e["canopen.node_id"] = u(m.Node)
	}
	switch {
	case m.NMT != nil:
		e["canopen.nmt_ctrl.cd"] = u(uint8(m.NMT.Command))
		e["canopen.nmt_ctrl.node_id"] = u(m.NMT.Target)
	case m.SYNC != nil && m.SYNC.HasCounter:
		e["canopen.sync.counter"] = u(m.SYNC.Counter)
	case m.EMCY != nil:
		e["canopen.em.err_code"] = u(m.EMCY.Code)
		e["canopen.em.err_reg"] = u(m.EMCY.Register)
	case m.TIME != nil:
		e["canopen.time_stamp_ms"] = u(m.TIME.Milliseconds)
		e["canopen.time_stamp_days"] = u(m.TIME.Days)
	case m.ErrorControl != nil:
		e["canopen.nmt_guard.state"] = u(uint8(m.ErrorControl.State))
		t := 0
		if m.ErrorControl.Toggle {
			t = 1
		}
		e["canopen.nmt_guard.toggle"] = u(t)
	case m.LSS != nil:
		e["canopen.lss.cs"] = u(m.LSS.CS)
	case m.PDO != nil && len(m.Data) > 0:
		e["canopen.pdo.data.bytes"] = strings.ReplaceAll(m.Data.String(), " ", "")
	case m.SDO != nil && (m.SDO.Phase == PhaseBlock || m.SDO.Phase == PhaseEnd || m.SDO.Phase == PhaseSubBlock):
		// Wireshark 4.6 shows block transfer frames without SDO fields:
		// only the function code and node-ID are compared.
	case m.SDO != nil:
		if m.SDO.Client {
			e["canopen.sdo.ccs"] = u(m.SDO.CS)
		} else {
			e["canopen.sdo.scs"] = u(m.SDO.CS)
		}
		if m.SDO.HasIndex {
			e["canopen.sdo.main_idx"] = u(m.SDO.Index)
			e["canopen.sdo.sub_idx"] = u(m.SDO.SubIndex)
		}
		if m.SDO.Phase == PhaseAbort {
			e["canopen.sdo.abort_code"] = u(uint32(m.SDO.Abort))
		}
	}
	return e
}

// normalize turns decimal and 0x-hexadecimal numbers into one form and
// lowercases hex strings.
func normalize(s string) string {
	if x, err := strconv.ParseUint(s, 0, 64); err == nil {
		return strconv.FormatUint(x, 10)
	}
	return strings.ToLower(s)
}
