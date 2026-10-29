// SPDX-License-Identifier: MIT

package cdcf_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

func u32(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }

func TestEncodeDecode(t *testing.T) {
	entries := []cdcf.Entry{
		{Index: 0x1017, SubIndex: 0, Data: []byte{0xE8, 0x03}},
		{Index: 0x2000, SubIndex: 3, Data: []byte{}},
		{Index: 0x1F50, SubIndex: 1, Data: bytes.Repeat([]byte{0xAA}, 300)},
	}
	b, err := cdcf.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != cdcf.Size(entries) {
		t.Fatalf("size %d, Size %d", len(b), cdcf.Size(entries))
	}
	want := []byte{3, 0, 0, 0, 0x17, 0x10, 0, 2, 0, 0, 0, 0xE8, 0x03}
	if !bytes.Equal(b[:len(want)], want) {
		t.Fatalf("encoding % X", b[:len(want)])
	}
	got, err := cdcf.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].String() != "1017:00=E8 03" || got[1].Index != 0x2000 || len(got[1].Data) != 0 || len(got[2].Data) != 300 {
		t.Fatalf("decoded %v", got)
	}
	empty, err := cdcf.Encode(nil)
	if err != nil || !bytes.Equal(empty, []byte{0, 0, 0, 0}) {
		t.Fatalf("empty: % X %v", empty, err)
	}
	if es, err := cdcf.Decode(empty); err != nil || len(es) != 0 {
		t.Fatalf("decode empty: %v %v", es, err)
	}
	if es, err := cdcf.Decode(nil); err != nil || es != nil {
		t.Fatalf("decode nil: %v %v", es, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		b    []byte
	}{
		{"short header", []byte{1, 0}},
		{"count beyond data", []byte{2, 0, 0, 0, 0x17, 0x10, 0, 0, 0, 0, 0}},
		{"huge count", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x17, 0x10, 0, 0, 0, 0, 0}},
		{"truncated entry", []byte{1, 0, 0, 0, 0x17, 0x10, 0, 0, 0, 0}},
		{"size beyond data", []byte{1, 0, 0, 0, 0x17, 0x10, 0, 3, 0, 0, 0, 1, 2}},
		{"trailing bytes", []byte{1, 0, 0, 0, 0x17, 0x10, 0, 1, 0, 0, 0, 1, 9}},
		{"second entry truncated", []byte{2, 0, 0, 0, 0x17, 0x10, 0, 1, 0, 0, 0, 1, 0x17, 0x10, 0, 0, 0, 0, 0x00}[:18]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cdcf.Decode(tc.b); !errors.Is(err, cdcf.ErrFormat) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestPDOSequence(t *testing.T) {
	tests := []struct {
		name string
		in   []cdcf.Entry
		want []string
	}{
		{
			name: "non-PDO entries keep their order",
			in:   []cdcf.Entry{{Index: 0x2000, Data: []byte{1}}, {Index: 0x1017, Data: []byte{2, 0}}},
			want: []string{"2000:00=01", "1017:00=02 00"},
		},
		{
			name: "whole TPDO",
			in: []cdcf.Entry{
				{Index: 0x1800, SubIndex: 0, Data: []byte{6}},
				{Index: 0x1800, SubIndex: 1, Data: u32(0x185)},
				{Index: 0x1800, SubIndex: 2, Data: []byte{1}},
				{Index: 0x1800, SubIndex: 4, Data: []byte{0}},
				{Index: 0x1A00, SubIndex: 0, Data: []byte{1}},
				{Index: 0x1A00, SubIndex: 1, Data: u32(0x20000008)},
				{Index: 0x1A00, SubIndex: 2, Data: u32(0)},
				{Index: 0x2000, Data: []byte{7}},
			},
			want: []string{
				"2000:00=07",
				"1800:01=85 01 00 80", "1800:02=01",
				"1A00:00=00", "1A00:01=08 00 00 20", "1A00:00=01",
				"1800:01=85 01 00 00",
			},
		},
		{
			name: "RPDO mapping without count, last value wins",
			in: []cdcf.Entry{
				{Index: 0x1601, SubIndex: 2, Data: u32(2)},
				{Index: 0x1601, SubIndex: 1, Data: u32(1)},
				{Index: 0x1401, SubIndex: 3, Data: []byte{9, 0}},
				{Index: 0x1401, SubIndex: 3, Data: []byte{10, 0}},
			},
			want: []string{"1401:03=0A 00", "1601:01=01 00 00 00", "1601:02=02 00 00 00"},
		},
		{
			name: "two PDOs in first-appearance order",
			in: []cdcf.Entry{
				{Index: 0x1A01, SubIndex: 0, Data: []byte{0}},
				{Index: 0x1600, SubIndex: 0, Data: []byte{0}},
			},
			want: []string{"1A01:00=00", "1A01:00=00", "1600:00=00", "1600:00=00"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cdcf.PDOSequence(tc.in)
			var s []string
			for _, e := range got {
				s = append(s, e.String())
			}
			if strings.Join(s, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got\n%v\nwant\n%v", s, tc.want)
			}
		})
	}
}

func TestEntriesFromDCF(t *testing.T) {
	dcf, err := eds.ParseFile("../eds/testdata/node37.dcf")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cdcf.Entries(dcf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	seq, err := cdcf.FromFile(dcf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq) == 0 {
		t.Fatal("no sequence")
	}
	b, err := cdcf.Encode(seq)
	if err != nil {
		t.Fatal(err)
	}
	back, err := cdcf.Decode(b)
	if err != nil || len(back) != len(seq) {
		t.Fatalf("round trip %d/%d %v", len(back), len(seq), err)
	}
	if _, err := cdcf.Entries(dcf, 200); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Fatalf("bad node-ID: %v", err)
	}
	if _, err := cdcf.FromFile(dcf, 200); err == nil {
		t.Fatal("FromFile bad node-ID")
	}
	if _, err := cdcf.Entries(nil, 1); err == nil {
		t.Fatal("nil file")
	}
	bad := *dcf
	bad.Objects = append([]*eds.Object{{Index: 0x1000, Entry: eds.Entry{ObjectType: od.ObjectVar}}}, dcf.Objects[1:]...)
	if _, err := cdcf.Entries(&bad, 0x25); err == nil {
		t.Fatal("broken DCF")
	}
}

// TestEntriesFromDictionary builds a DCF from a dictionary and checks the
// PDO and plain entries it yields.
func TestEntriesFromDictionary(t *testing.T) {
	d, err := od.Standard(od.Config{NodeID: 5, TPDOs: 1, RPDOs: 1, MappingEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Add(od.NewVar(0x2000, "x", canopen.Unsigned8, od.AccessRW, uint8(3)))
	_ = d.Add(od.NewVar(0x2001, "ro", canopen.Unsigned8, od.AccessRO, uint8(3)))
	_ = d.Set(0x1A00, 1, u32(0x20000008))
	_ = d.Set(0x1A00, 0, []byte{1})
	f := eds.FromDictionary(d, eds.ExportOptions{DCF: true})
	seq, err := cdcf.FromFile(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, e := range seq {
		s = append(s, e.String())
	}
	joined := strings.Join(s, "|")
	for _, want := range []string{"2000:00=03", "1A00:00=00|1A00:01=08 00 00 20|1A00:00=01", "1800:01=85 01 00 80"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "2001:") {
		t.Errorf("read-only object included: %s", joined)
	}
}
