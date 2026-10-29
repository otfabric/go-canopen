// SPDX-License-Identifier: MIT

package pdo

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

func le32(x uint32) []byte { return binary.LittleEndian.AppendUint32(nil, x) }
func le16(x uint16) []byte { return binary.LittleEndian.AppendUint16(nil, x) }

// TestProtect replays remote writes and checks the exact abort codes.
func TestProtect(t *testing.T) {
	type w struct {
		index uint16
		sub   uint8
		data  []byte
		want  error
	}
	const tc, tm, rc, rm = 0x1800, 0x1A00, 0x1400, 0x1600
	entry := func(e Entry) []byte { return le32(e.Uint32()) }
	tests := []struct {
		name   string
		writes []w
	}{
		{"default PDO is inactive: mapping writable", []w{
			{tm, 1, entry(Entry{idxU8, 0, 8}), nil},
			{tm, 0, []byte{1}, nil},
		}},
		{"active PDO: mapping refused", []w{
			{tm, 1, entry(Entry{idxU8, 0, 8}), nil},
			{tm, 0, []byte{1}, nil},
			{tm, 1, entry(Entry{idxI16, 0, 16}), canopen.AbortUnsupported},
			{tm, 0, []byte{0}, canopen.AbortUnsupported},
			{tc, 1, le32(0x185 | uint32(canopen.COBInvalid)), nil},
			{tm, 1, entry(Entry{idxI16, 0, 16}), canopen.AbortUnsupported}, // sub 0 is not 0
			{tm, 0, []byte{0}, nil},
			{tm, 1, entry(Entry{idxI16, 0, 16}), nil},
		}},
		{"entries", []w{
			{tm, 1, entry(Entry{idxNoMap, 0, 8}), canopen.AbortNotMappable},
			{tm, 1, entry(Entry{0x2FFF, 0, 8}), canopen.AbortNoObject},
			{tm, 1, entry(Entry{idxU8, 0, 7}), canopen.AbortNotMappable},
			{tm, 1, entry(Entry{0x0005, 0, 8}), canopen.AbortNotMappable}, // dummy in a TPDO
			{rm, 1, entry(Entry{0x0005, 0, 8}), nil},                      // dummy in an RPDO
			{rm, 2, entry(Entry{idxRO, 0, 16}), canopen.AbortNotMappable}, // read-only in an RPDO
			{tm, 1, entry(Entry{idxRO, 0, 16}), nil},
			{tm, 1, le32(0), nil},
		}},
		{"sub 0 validates", []w{
			{tm, 0, []byte{9}, canopen.AbortPDOLength}, // 8 entries only
			{tm, 0, []byte{1}, canopen.AbortNotMappable},
			{tm, 1, entry(Entry{idxU32, 0, 32}), nil},
			{tm, 2, entry(Entry{idxU32, 0, 32}), nil},
			{tm, 3, entry(Entry{idxBool, 0, 1}), nil},
			{tm, 0, []byte{3}, canopen.AbortPDOLength},
			{tm, 0, []byte{2}, nil},
		}},
		{"sub 0 sees entries changed locally", []w{
			{tm, 1, entry(Entry{idxU8, 0, 8}), nil},
		}},
		{"COB-ID", []w{
			{tc, 1, le32(0x190), canopen.AbortInvalidValue},                               // nothing mapped
			{tc, 1, le32(0x185 | uint32(canopen.COBInvalid)), nil},                        // invalid: fine
			{tc, 1, le32(0x195 | uint32(canopen.COBInvalid)), nil},                        // new ID while invalid
			{tm, 1, entry(Entry{idxU8, 0, 8}), nil},                                       //
			{tm, 0, []byte{1}, nil},                                                       //
			{tc, 1, le32(0x005), canopen.AbortInvalidValue},                               // restricted
			{tc, 1, le32(0x985), canopen.AbortInvalidValue},                               // bits 11..28
			{tc, 1, le32(0x195), nil},                                                     // validate
			{tc, 1, le32(0x196), canopen.AbortInvalidValue},                               // change ID while valid
			{tc, 1, le32(0x195 | uint32(canopen.COBExtended)), canopen.AbortInvalidValue}, // change format
			{tc, 1, le32(0x195 | uint32(canopen.COBNoRTR)), nil},                          // bit 30 may change
			{tc, 3, le16(10), canopen.AbortInvalidValue},                                  // inhibit while valid
			{tc, 6, []byte{2}, canopen.AbortInvalidValue},                                 // SYNC start while valid
			{tc, 5, le16(100), nil},                                                       // event timer any time
			{tc, 2, []byte{1}, nil},                                                       // type any time
			{tc, 1, le32(0x195 | uint32(canopen.COBInvalid)), nil},
			{tc, 3, le16(10), nil},
			{tc, 6, []byte{2}, nil},
			{tc, 6, []byte{241}, canopen.AbortInvalidValue},
			{tc, 1, le32(0x1234567 | uint32(canopen.COBExtended)), nil},
		}},
		{"transmission types", []w{
			{tc, 2, []byte{0}, nil},
			{tc, 2, []byte{240}, nil},
			{tc, 2, []byte{241}, canopen.AbortInvalidValue},
			{tc, 2, []byte{251}, canopen.AbortInvalidValue},
			{tc, 2, []byte{252}, nil},
			{tc, 2, []byte{253}, nil},
			{tc, 2, []byte{254}, nil},
			{rc, 2, []byte{252}, canopen.AbortInvalidValue},
			{rc, 2, []byte{253}, canopen.AbortInvalidValue},
			{rc, 2, []byte{241}, canopen.AbortInvalidValue},
			{rc, 2, []byte{254}, nil},
			{rc, 2, []byte{0}, nil},
			{rc, 3, le16(10), nil}, // RPDO inhibit time: not restricted
			{rc, 4, []byte{1}, nil},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := device(t, 5)
			for _, tr := range []bool{true, false} {
				if _, err := Protect(d, tr, 1); err != nil {
					t.Fatal(err)
				}
			}
			if tt.name == "sub 0 sees entries changed locally" {
				// A local write bypasses the hooks; sub 0 still validates.
				if err := d.Set(tm, 2, le32(Entry{idxNoMap, 0, 8}.Uint32())); err != nil {
					t.Fatal(err)
				}
				if err := d.Write(tm, 0, []byte{2}); err != canopen.AbortNotMappable {
					t.Fatalf("sub 0 = 2: %v", err)
				}
				if err := d.Set(tm, 2, le32(0)); err != nil {
					t.Fatal(err)
				}
				if err := d.Write(tm, 0, []byte{2}); err != canopen.AbortNotMappable {
					t.Fatalf("sub 0 = 2 with an empty entry: %v", err)
				}
			}
			for i, x := range tt.writes {
				if err := d.Write(x.index, x.sub, x.data); err != x.want {
					t.Fatalf("write %d %04X:%02X % X: %v, want %v", i, x.index, x.sub, x.data, err, x.want)
				}
			}
		})
	}
}

func TestProtectErrors(t *testing.T) {
	d := device(t, 5)
	if _, err := Protect(d, true, 0); !errors.Is(err, ErrNumber) {
		t.Errorf("TPDO 0: %v", err)
	}
	if _, err := Protect(d, true, 5); !errors.Is(err, canopen.AbortNoObject) {
		t.Errorf("TPDO 5: %v", err)
	}
	if _, err := Protect(od.New(1), false, 1); err == nil {
		t.Error("empty dictionary protected")
	}
}

func TestUnprotect(t *testing.T) {
	d := device(t, 5)
	before := d.HookCount()
	un, err := Protect(d, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.HookCount() != before+2 {
		t.Fatalf("hooks %d, want %d", d.HookCount(), before+2)
	}
	if err := d.Write(CommIndex(true, 1), 2, []byte{245}); err != canopen.AbortInvalidValue {
		t.Fatalf("protected write: %v", err)
	}
	un()
	un()
	if d.HookCount() != before {
		t.Fatalf("hooks %d after unprotect, want %d", d.HookCount(), before)
	}
	if err := d.Write(CommIndex(true, 1), 2, []byte{245}); err != nil {
		t.Fatalf("unprotected write: %v", err)
	}
}
