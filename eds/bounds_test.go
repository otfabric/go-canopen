// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// A $NODEID default whose sum leaves the 64-bit range used to wrap around
// (INTEGER64 $NODEID+0x7FFFFFFFFFFFFFFF became negative) and be accepted.
func TestParseLiteralNodeIDOverflow(t *testing.T) {
	for _, tc := range []struct {
		t   canopen.DataType
		lit string
	}{
		{canopen.Integer64, "$NODEID+0x7FFFFFFFFFFFFFFF"},
		{canopen.Unsigned64, "$NODEID+0xFFFFFFFFFFFFFFFF"},
	} {
		if b, _, err := parseLiteral(tc.t, tc.lit, 1); err == nil {
			t.Errorf("%s %q = %x, want an error", tc.t, tc.lit, b)
		}
		// Without a node-ID the offset alone is valid.
		if _, rel, err := parseLiteral(tc.t, tc.lit, 0); err != nil || !rel {
			t.Errorf("%s %q node 0: rel %v, %v", tc.t, tc.lit, rel, err)
		}
	}
	for _, tc := range []struct {
		t    canopen.DataType
		lit  string
		want []byte
	}{
		{canopen.Integer8, "$NODEID+0xF0", []byte{0xF1}}, // -16 + 1
		{canopen.Integer8, "$NODEID+0x7E", []byte{0x7F}},
		{canopen.Integer64, "$NODEID+0x7FFFFFFFFFFFFFFE", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}},
		{canopen.Unsigned64, "$NODEID+0xFFFFFFFFFFFFFFFE", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	} {
		b, _, err := parseLiteral(tc.t, tc.lit, 1)
		if err != nil || !bytes.Equal(b, tc.want) {
			t.Errorf("%s %q = %x, %v; want %x", tc.t, tc.lit, b, err, tc.want)
		}
	}
	if b, _, err := parseLiteral(canopen.Integer8, "$NODEID+0x7F", 1); err == nil {
		t.Errorf("INTEGER8 $NODEID+0x7F node 1 = %x, want an error", b)
	}
}

// An object with all 256 sub-indices used to export SubNumber 0 (uint8
// wrap); it saturates at 255.
func TestFromDictionarySubNumberSaturates(t *testing.T) {
	subs := make([]*od.Variable, 0, 255)
	for i := 1; i <= 255; i++ {
		subs = append(subs, od.Sub(uint8(i), fmt.Sprint("s", i), canopen.Unsigned8, od.AccessRW, nil))
	}
	d := od.New(1)
	if err := d.Add(od.NewRecord(0x2000, "full", subs...)); err != nil {
		t.Fatal(err)
	}
	f := FromDictionary(d, ExportOptions{})
	for _, o := range f.Objects {
		if o.Index == 0x2000 {
			if o.SubNumber != 255 || len(o.Subs) != 256 {
				t.Fatalf("SubNumber %d, %d subs", o.SubNumber, len(o.Subs))
			}
			return
		}
	}
	t.Fatal("object 0x2000 not exported")
}
