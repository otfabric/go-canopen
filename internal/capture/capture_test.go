// SPDX-License-Identifier: MIT

package capture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCopleyLine(t *testing.T) {
	r, err := ParseCopleyLine("3382993761 CAN0: 0x00000601 - 0x40 0x18 0x10 0x00 0x00 0x00 0x00 0x00")
	if err != nil || r.Micros != 3382993761 || r.Port != 0 || r.Frame.ID != 0x601 || r.Frame.Len != 8 || r.Frame.Data[0] != 0x40 || r.Frame.Interface != "CAN0" {
		t.Fatalf("%+v %v", r, err)
	}
	if got := r.Frame.Timestamp.UnixMicro(); got != 3382993761 {
		t.Errorf("timestamp %d", got)
	}
	if r, err = ParseCopleyLine("3380258552 CAN0: 0x00000080 -"); err != nil || r.Frame.Len != 0 || r.Frame.IsRemote() {
		t.Errorf("SYNC: %+v %v", r, err)
	}
	if r, err = ParseCopleyLine("3383111385 RTR1: 0x00000181"); err != nil || !r.Frame.IsRemote() || r.Port != 1 || r.Frame.Len != 0 {
		t.Errorf("RTR: %+v %v", r, err)
	}
	for _, bad := range []string{
		"", "1 CAN0:", "x CAN0: 0x00000080 -", "1 CAN0 0x00000080 -", "1 ERR0: 0x00000080 -", "1 CANx: 0x00000080 -",
		"1 CAN-1: 0x00000080 -", "1 CAN0: 0x80 -", "1 CAN0: 0x0000008G -", "1 CAN0: 0x00000800 -",
		"1 RTR0: 0x00000181 - 0x01", "1 CAN0: 0x00000181 0x01", "1 CAN0: 0x00000181",
		"1 CAN0: 0x00000181 - 0x1", "1 CAN0: 0x00000181 - 0xZZ", "1 CAN0: 0x00000181 - 0x01 0x02 0x03 0x04 0x05 0x06 0x07 0x08 0x09",
		"1 CAN0: 1x00000181 -",
	} {
		if _, err := ParseCopleyLine(bad); !errors.Is(err, ErrFormat) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestReadCopley(t *testing.T) {
	recs, err := ReadCopley(strings.NewReader("1 CAN0: 0x00000080 - \r\n\r\n2 RTR0: 0x00000701\r\n3 CAN0: 0x00000701 - 0x85 \r\n"))
	if err != nil || len(recs) != 3 || recs[2].Line != 4 || recs[2].Frame.Data[0] != 0x85 {
		t.Fatalf("%+v %v", recs, err)
	}
	if _, err := ReadCopley(strings.NewReader("1 CAN0: 0x00000080 -\nbad\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("%v", err)
	}
}

// TestCopleyLogs reads the four committed logs and checks them against
// manifest.json: the sizes and the frame counts.
func TestCopleyLogs(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join(Dir(), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"unknown_copley-1100_absolute-move":         2274,
		"unknown_copley-1100_relative-move":         2464,
		"unknown_copley-11e0_linkage-home-and-move": 2928,
		"unknown_copley-1060_pvt-move":              8861,
	}
	for _, name := range CopleyLogs {
		recs, err := LoadCopley(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != want[name] || recs[len(recs)-1].Line != want[name] {
			t.Errorf("%s: %d frames", name, len(recs))
		}
		if !strings.Contains(string(manifest), `"copley/`+name+`.log"`) {
			t.Errorf("%s not in manifest.json", name)
		}
	}
	if _, err := LoadCopley("missing"); err == nil {
		t.Error("missing log")
	}
}

// TestSHA256SUMS checks the committed raw captures against SHA256SUMS.
func TestSHA256SUMS(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(Dir(), "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(Dir(), f[1]))
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != f[0] {
			t.Errorf("%s: checksum mismatch", f[1])
		}
		n++
	}
	if n == 0 {
		t.Error("no entries")
	}
}

func pcapFile(order binary.ByteOrder, recs ...[]byte) []byte {
	b := make([]byte, 24)
	order.PutUint32(b, 0xA1B2C3D4)
	order.PutUint32(b[20:], 227)
	for _, r := range recs {
		h := make([]byte, 16)
		order.PutUint32(h, 1)
		order.PutUint32(h[4:], 2)
		order.PutUint32(h[8:], uint32(len(r)))
		order.PutUint32(h[12:], uint32(len(r)))
		b = append(append(b, h...), r...)
	}
	return b
}

func TestReadSocketCANPCAP(t *testing.T) {
	std := []byte{0, 0, 0x06, 0x42, 2, 0x7F, 0, 0, 0x40, 0x08} // FDF set in byte 5: still classical
	ext := []byte{0x80, 0x01, 0x23, 0x45, 1, 0, 0, 0, 0xAA}    // 29-bit
	rtr := []byte{0x40, 0, 0x07, 0x01, 1, 0, 0, 0}             // remote request, DLC 1
	short := []byte{0, 0, 0x01, 0x81, 8, 0, 0, 0, 1, 2}        // DLC 8, 2 bytes recorded
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		fs, err := ReadSocketCANPCAP(pcapFile(order, std, ext, rtr, short))
		if err != nil || len(fs) != 4 {
			t.Fatalf("%v %v", fs, err)
		}
		if fs[0].ID != 0x642 || fs[0].IsFD() || fs[0].Len != 2 || fs[0].Timestamp.UnixMicro() != 1_000_002 {
			t.Errorf("std %v", fs[0])
		}
		if fs[1].ID != 0x1234567 && !fs[1].IsExtended() {
			t.Errorf("ext %v", fs[1])
		}
		if !fs[2].IsRemote() || fs[2].ID != 0x701 || fs[2].Len != 1 {
			t.Errorf("rtr %v", fs[2])
		}
		if fs[3].Len != 2 {
			t.Errorf("short %v", fs[3])
		}
	}
	for name, b := range map[string][]byte{
		"tiny":     make([]byte, 10),
		"magic":    make([]byte, 24),
		"linktype": func() []byte { b := pcapFile(binary.LittleEndian); b[20] = 1; return b }(),
		"header":   append(pcapFile(binary.LittleEndian), 1, 2, 3),
		"long":     pcapFile(binary.LittleEndian, make([]byte, 72)),
		"cut":      pcapFile(binary.LittleEndian, std)[:24+16+4],
		"error":    pcapFile(binary.LittleEndian, []byte{0x20, 0, 0, 1, 8, 0, 0, 0}),
		"dlc":      pcapFile(binary.LittleEndian, []byte{0, 0, 0, 1, 9, 0, 0, 0}),
		"id":       pcapFile(binary.LittleEndian, []byte{0x80, 0, 0, 0, 9, 0, 0, 0}),
	} {
		if _, err := ReadSocketCANPCAP(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
