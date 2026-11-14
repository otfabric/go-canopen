// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"fmt"
	"strings"
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

// compactSection is an ARRAY of n VISIBLE_STRING elements sharing one
// default of size bytes.
func compactSection(index uint16, n, size int) string {
	return fmt.Sprintf("[%04X]\nParameterName=s\nObjectType=0x8\nDataType=0x0009\nAccessType=rw\nCompactSubObj=%d\nDefaultValue=%s\n",
		index, n, bytes.Repeat([]byte{'A'}, size))
}

// The CompactSubObj expansion copies the object's value into every
// element; the copied bytes are bounded so that one short section cannot
// produce up to 255 copies of a long value.
func TestCompactExpansionBound(t *testing.T) {
	const base = "[1000]\nParameterName=a\nDataType=7\nAccessType=ro\n"
	parse := func(src string) *File {
		t.Helper()
		f, err := ParseBytes([]byte(base + src))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// 255 elements of a 128 KiB default (32 MiB expanded) are refused.
	big := parse(compactSection(0x2000, 255, 128<<10))
	if _, err := big.Dictionary(1); err == nil || !strings.Contains(err.Error(), "CompactSubObj") {
		t.Fatalf("Dictionary = %v, want a CompactSubObj error", err)
	}
	// Lenient: the elements beyond the bound are skipped with one warning.
	d, ds, err := big.BuildDictionary(1, BuildOptions{Lenient: true})
	if err != nil || len(ds) != 1 || ds[0].Severity != Warning || ds[0].Key != "CompactSubObj" {
		t.Fatalf("lenient: %v %v", ds, err)
	}
	if o := d.Object(0x2000); o == nil || len(o.Subs) != 1+DefaultMaxCompactBytes/(128<<10) {
		t.Errorf("lenient kept %d sub-objects", len(o.Subs))
	}
	// The bound is configurable and can be lifted.
	small := parse(compactSection(0x2000, 4, 100))
	if _, _, err := small.BuildDictionary(1, BuildOptions{MaxCompactBytes: 399}); err == nil {
		t.Error("400 bytes accepted with a 399-byte bound")
	}
	if _, _, err := small.BuildDictionary(1, BuildOptions{MaxCompactBytes: 400}); err != nil {
		t.Errorf("400-byte bound: %v", err)
	}
	if _, _, err := parse(compactSection(0x2000, 20, 100<<10)).BuildDictionary(1, BuildOptions{MaxCompactBytes: -1}); err != nil {
		t.Errorf("no bound: %v", err)
	}
	// The bound is summed over the build.
	two := parse(compactSection(0x2000, 2, 100) + compactSection(0x2001, 2, 100))
	if _, _, err := two.BuildDictionary(1, BuildOptions{MaxCompactBytes: 300}); err == nil {
		t.Error("two sections of 200 bytes accepted with a 300-byte bound")
	}
	// Normal compact arrays, elements valued from [XXXXValue] and
	// fixed-size elements do not count.
	ok := parse(compactSection(0x2000, 255, 64) +
		"[2001]\nParameterName=v\nObjectType=0x8\nDataType=0x0009\nAccessType=rw\nCompactSubObj=2\n[2001Value]\nNrOfEntries=2\n1=" +
		string(bytes.Repeat([]byte{'B'}, 2<<20)) + "\n2=x\n" +
		"[2002]\nParameterName=u\nObjectType=0x8\nDataType=0x0007\nAccessType=rw\nCompactSubObj=255\nDefaultValue=5\n")
	d, err = ok.Dictionary(1)
	if err != nil {
		t.Fatal(err)
	}
	if o := d.Object(0x2000); len(o.Subs) != 256 {
		t.Errorf("2000 has %d sub-objects", len(o.Subs))
	}
	if v, _ := d.Get(0x2001, 1); len(v) != 2<<20 {
		t.Errorf("2001:1 is %d bytes", len(v))
	}
	if o := d.Object(0x2002); len(o.Subs) != 256 {
		t.Errorf("2002 has %d sub-objects", len(o.Subs))
	}
}
