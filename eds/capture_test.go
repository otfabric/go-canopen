// SPDX-License-Identifier: MIT

package eds

import (
	"path/filepath"
	"testing"

	"github.com/otfabric/go-canopen"
	rec "github.com/otfabric/go-canopen/internal/capture"
)

// TestCopleyEDS reads the Copley Controls Accelnet EDS (copley/copley_accelnet.eds, CANopen.eds of the
// Copley message logs) and checks it against what
// the drives did in the four logs: it validates without errors, builds a
// dictionary, and every object the master read or wrote is in it,
// readable or writable as used, with the size the drive transferred.
func TestCopleyEDS(t *testing.T) {
	f, err := ParseFile(filepath.Join(rec.Dir(), "copley", "copley_accelnet.eds"))
	if err != nil {
		t.Fatal(err)
	}
	if f.DeviceInfo.VendorNumber != 0xAB || f.DeviceInfo.ProductName != "Accelnet" {
		t.Errorf("vendor 0x%X %q", f.DeviceInfo.VendorNumber, f.DeviceInfo.ProductName)
	}
	warnings := 0
	for _, d := range f.Validate() {
		if d.Severity == Error {
			t.Errorf("Validate: %s", d)
		} else {
			warnings++
		}
	}
	d, err := f.Dictionary(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d objects, %d warnings", len(d.Objects()), warnings)
	checked := 0
	for _, name := range rec.CopleyLogs {
		recs, err := rec.LoadCopley(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			fr := r.Frame
			if fr.IsRemote() || fr.Len != 8 || fr.ID <= 0x580 || fr.ID >= 0x680 || fr.ID == 0x600 {
				continue
			}
			b := fr.Payload()
			index, sub := uint16(b[1])|uint16(b[2])<<8, b[3]
			var op string
			switch {
			case fr.ID > 0x600 && b[0]&0xE0 == 0x20: // initiate download request
				op = "write"
			case fr.ID < 0x600 && b[0]&0xE0 == 0x40: // initiate upload answer
				op = "read"
			default:
				continue
			}
			checked++
			v, err := d.Variable(index, sub)
			if err != nil {
				t.Errorf("%s line %d: %s 0x%04X:%02X not in the EDS", name, r.Line, op, index, sub)
				continue
			}
			if op == "read" && !v.Access.Readable() || op == "write" && !v.Access.Writable() {
				t.Errorf("%s line %d: %s 0x%04X:%02X, EDS access %s", name, r.Line, op, index, sub, v.Access)
			}
			if b[0]&0x03 == 0x03 {
				n := 4 - int(b[0]>>2)&3
				if v.DataType.Size() != n && (v.DataType != canopen.Boolean || n != 1) {
					t.Errorf("%s line %d: %s 0x%04X:%02X of %d bytes, EDS %s", name, r.Line, op, index, sub, n, v.DataType)
				}
			}
		}
	}
	if checked < 1000 {
		t.Errorf("%d transfers checked", checked)
	}
}
