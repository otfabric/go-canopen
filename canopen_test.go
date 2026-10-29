// SPDX-License-Identifier: MIT

package canopen

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/otfabric/go-canbus"
)

func TestNodeID(t *testing.T) {
	for _, tc := range []struct {
		id    NodeID
		valid bool
	}{{0, false}, {1, true}, {127, true}, {128, false}, {255, false}} {
		if tc.id.Valid() != tc.valid {
			t.Errorf("%d valid=%v", tc.id, tc.id.Valid())
		}
		if err := tc.id.Check(); (err == nil) != tc.valid || (err != nil && !errors.Is(err, ErrInvalidNodeID)) {
			t.Errorf("%d check=%v", tc.id, err)
		}
	}
}

func TestDefaultPDOIDs(t *testing.T) {
	want := map[int][2]canbus.ID{1: {0x185, 0x205}, 2: {0x285, 0x305}, 3: {0x385, 0x405}, 4: {0x485, 0x505}}
	for n, w := range want {
		if got := DefaultTPDO(n, 5); got != w[0] {
			t.Errorf("TPDO%d = %x", n, got)
		}
		if got := DefaultRPDO(n, 5); got != w[1] {
			t.Errorf("RPDO%d = %x", n, got)
		}
	}
	if DefaultTPDO(0, 1) != 0 || DefaultRPDO(5, 1) != 0 {
		t.Error("out of range PDO numbers must give 0")
	}
}

func TestCOBID(t *testing.T) {
	c := NewCOBID(0x181)
	if c.ID() != 0x181 || c.Extended() || !c.Valid() {
		t.Fatalf("%v", c)
	}
	f, _ := canbus.NewStandard(0x181, nil)
	if !c.Matches(&f) || c.Filter() != canbus.ExactStandard(0x181) {
		t.Fatal("standard match")
	}
	x := NewCOBID(0x1234567)
	if !x.Extended() || x.ID() != 0x1234567 || x.Filter() != canbus.ExactExtended(0x1234567) {
		t.Fatalf("%v", x)
	}
	if x.Matches(&f) {
		t.Fatal("extended matched a standard frame")
	}
	inv := COBID(0x181) | COBInvalid | COBNoRTR
	if inv.Valid() || inv.ID() != 0x181 {
		t.Fatal(inv)
	}
	for c, s := range map[COBID]string{0x181: "0x181", inv: "0x181 invalid bit30", x: "0x1234567 ext"} {
		if c.String() != s {
			t.Errorf("%q != %q", c.String(), s)
		}
	}
	// Bits 11..28 of a standard COB-ID are ignored.
	if COBID(0x10000181).ID() != 0x181 {
		t.Fatal("standard ID not masked")
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		id   canbus.ID
		s    Service
		node NodeID
		pdo  int
	}{
		{0x000, ServiceNMT, 0, 0},
		{0x080, ServiceSYNC, 0, 0},
		{0x081, ServiceEMCY, 1, 0},
		{0x0FF, ServiceEMCY, 127, 0},
		{0x100, ServiceTIME, 0, 0},
		{0x185, ServiceTPDO, 5, 1},
		{0x205, ServiceRPDO, 5, 1},
		{0x285, ServiceTPDO, 5, 2},
		{0x305, ServiceRPDO, 5, 2},
		{0x385, ServiceTPDO, 5, 3},
		{0x405, ServiceRPDO, 5, 3},
		{0x485, ServiceTPDO, 5, 4},
		{0x505, ServiceRPDO, 5, 4},
		{0x585, ServiceSDOTx, 5, 0},
		{0x605, ServiceSDORx, 5, 0},
		{0x705, ServiceHeartbeat, 5, 0},
		{0x7E4, ServiceLSS, 0, 0},
		{0x7E5, ServiceLSS, 0, 0},
		{0x180, ServiceUnknown, 0, 0}, // node-ID 0
		{0x680, ServiceUnknown, 0, 0},
		{0x6E5, ServiceUnknown, 0, 0},
		{0x800, ServiceUnknown, 0, 0},
	} {
		s, n, p := Classify(tc.id)
		if s != tc.s || n != tc.node || p != tc.pdo {
			t.Errorf("%x: %v %d %d", tc.id, s, n, p)
		}
	}
	if ServiceHeartbeat.String() != "NMT error control" || Service(99).String() != "Service(99)" {
		t.Error("service names")
	}
}

func TestAbort(t *testing.T) {
	if !strings.Contains(AbortNoObject.Error(), "0x06020000") || AbortNoObject.Text() == "" {
		t.Error(AbortNoObject.Error())
	}
	if AbortCode(0x12345678).Text() != "" || AbortCode(0x12345678).Error() != "canopen: SDO abort 0x12345678" {
		t.Error(AbortCode(0x12345678).Error())
	}
	for c := range abortText {
		if c.Text() == "" {
			t.Errorf("%x has no text", uint32(c))
		}
	}
	var err error = &AbortError{Code: AbortReadOnly, Index: 0x1000, SubIndex: 0, Remote: true}
	if !errors.Is(err, AbortReadOnly) || !strings.Contains(err.Error(), "remote, object 0x1000:00") {
		t.Error(err)
	}
	var ac AbortCode
	if !errors.As(err, &ac) || ac != AbortReadOnly {
		t.Error("errors.As")
	}
	if e := (&AbortError{Code: AbortTimeout}); !strings.Contains(e.Error(), "local") {
		t.Error(e)
	}
}

func TestNMTNames(t *testing.T) {
	for _, s := range []NMTState{StateInitialising, StateStopped, StateOperational, StatePreOperational, StateUnknown} {
		got, err := ParseNMTState(s.String())
		if err != nil || got != s {
			t.Errorf("%v: %v %v", s, got, err)
		}
	}
	if _, err := ParseNMTState("x"); err == nil {
		t.Error("parsed x")
	}
	if NMTState(3).String() != "NMTState(0x03)" {
		t.Error(NMTState(3).String())
	}
	for c := range nmtCommandNames {
		got, err := ParseNMTCommand(c.String())
		if err != nil || got != c || !c.Valid() {
			t.Errorf("%v: %v %v", c, got, err)
		}
	}
	if _, err := ParseNMTCommand("x"); err == nil || NMTCommand(3).Valid() || NMTCommand(3).String() != "NMTCommand(0x03)" {
		t.Error("bad command accepted")
	}
}

func TestDataTypeInfo(t *testing.T) {
	for dt, info := range types {
		if dt.String() != info.name || !dt.Known() {
			t.Errorf("%v", dt)
		}
		got, err := ParseDataType(strings.ToLower(info.name))
		if err != nil || got != dt {
			t.Errorf("parse %s: %v %v", info.name, got, err)
		}
	}
	if dt, err := ParseDataType("0x0007"); err != nil || dt != Unsigned32 {
		t.Error(dt, err)
	}
	if _, err := ParseDataType("FOO"); err == nil {
		t.Error("parsed FOO")
	}
	if DataType(0x99).Known() || DataType(0x99).String() != "DataType(0x0099)" {
		t.Error("unknown type")
	}
	if Boolean.Bits() != 1 || Unsigned24.Bits() != 24 || VisibleString.Bits() != 0 {
		t.Error("bits")
	}
	if !Integer40.IsInteger() || !Integer40.IsSigned() || Boolean.IsInteger() || Real32.IsInteger() || TimeOfDay.IsInteger() {
		t.Error("integer classification")
	}
	if !Real64.IsFloat() || !Domain.IsVariable() || Unsigned8.IsVariable() {
		t.Error("float/variable")
	}
}

func TestEncodeDecode(t *testing.T) {
	for _, tc := range []struct {
		dt   DataType
		in   any
		want []byte
		out  any
	}{
		{Boolean, true, []byte{1}, true},
		{Boolean, false, []byte{0}, false},
		{Boolean, 1, []byte{1}, true},
		{Integer8, -1, []byte{0xFF}, int64(-1)},
		{Integer16, int16(-2), []byte{0xFE, 0xFF}, int64(-2)},
		{Integer24, -8388608, []byte{0, 0, 0x80}, int64(-8388608)},
		{Integer32, int32(math.MaxInt32), []byte{0xFF, 0xFF, 0xFF, 0x7F}, int64(math.MaxInt32)},
		{Integer40, int64(-1), []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, int64(-1)},
		{Integer64, int64(math.MinInt64), []byte{0, 0, 0, 0, 0, 0, 0, 0x80}, int64(math.MinInt64)},
		{Unsigned8, uint8(200), []byte{200}, uint64(200)},
		{Unsigned16, 0x1234, []byte{0x34, 0x12}, uint64(0x1234)},
		{Unsigned24, uint32(0xABCDEF), []byte{0xEF, 0xCD, 0xAB}, uint64(0xABCDEF)},
		{Unsigned32, uint(0xDEADBEEF), []byte{0xEF, 0xBE, 0xAD, 0xDE}, uint64(0xDEADBEEF)},
		{Unsigned48, uint64(1 << 40), []byte{0, 0, 0, 0, 0, 1}, uint64(1 << 40)},
		{Unsigned64, uint64(math.MaxUint64), bytes.Repeat([]byte{0xFF}, 8), uint64(math.MaxUint64)},
		{Real32, float32(1.5), []byte{0, 0, 0xC0, 0x3F}, float32(1.5)},
		{Real32, 1.5, []byte{0, 0, 0xC0, 0x3F}, float32(1.5)},
		{Real64, 2.0, []byte{0, 0, 0, 0, 0, 0, 0, 0x40}, 2.0},
		{Real64, float32(2), []byte{0, 0, 0, 0, 0, 0, 0, 0x40}, 2.0},
		{VisibleString, "abc", []byte("abc"), "abc"},
		{OctetString, "ab", []byte("ab"), []byte("ab")},
		{Domain, []byte{1, 2, 3}, []byte{1, 2, 3}, []byte{1, 2, 3}},
		{UnicodeString, "hé", []byte{'h', 0, 0xE9, 0}, "hé"},
		{TimeOfDay, []byte{1, 2, 3, 4, 5, 6}, []byte{1, 2, 3, 4, 5, 6}, []byte{1, 2, 3, 4, 5, 6}},
		{Unsigned16, []byte{1, 2}, []byte{1, 2}, uint64(0x0201)},
	} {
		b, err := Encode(tc.dt, tc.in)
		if err != nil || !bytes.Equal(b, tc.want) {
			t.Errorf("Encode(%v, %v) = %x, %v", tc.dt, tc.in, b, err)
			continue
		}
		v, err := Decode(tc.dt, b)
		if err != nil {
			t.Errorf("Decode(%v, %x): %v", tc.dt, b, err)
			continue
		}
		if bv, ok := v.([]byte); ok {
			if !bytes.Equal(bv, tc.out.([]byte)) {
				t.Errorf("Decode(%v) = %x", tc.dt, bv)
			}
		} else if v != tc.out {
			t.Errorf("Decode(%v, %x) = %#v, want %#v", tc.dt, b, v, tc.out)
		}
	}
	if s, _ := Decode(VisibleString, []byte("ab\x00\x00")); s != "ab" {
		t.Errorf("%q", s)
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, tc := range []struct {
		dt DataType
		in any
	}{
		{Unsigned8, 256},
		{Unsigned8, -1},
		{Integer8, 128},
		{Integer8, -129},
		{Integer16, uint64(math.MaxUint64)},
		{Boolean, 2},
		{Unsigned16, []byte{1}},
		{Unsigned16, "x"},
		{Real32, "x"},
		{Boolean, "x"},
		{VisibleString, 1},
		{PDOMapping, 1},
	} {
		if _, err := Encode(tc.dt, tc.in); !errors.Is(err, ErrDataType) {
			t.Errorf("Encode(%v, %#v) = %v", tc.dt, tc.in, err)
		}
	}
	if _, err := EncodeInt(VisibleString, 0, 0, false); err == nil {
		t.Error("EncodeInt of a string type")
	}
	if _, err := Decode(Unsigned32, []byte{1}); !errors.Is(err, ErrDataType) {
		t.Error(err)
	}
	if DecodeInt(nil) != 0 || DecodeInt([]byte{0x80}) != -128 || DecodeUint([]byte{1, 1}) != 0x101 {
		t.Error("decode helpers")
	}
}

func TestParseNumbers(t *testing.T) {
	for s, want := range map[string]uint64{"10": 10, "0x1A": 26, "0X1a": 26, "017": 15, "0": 0, " 7 ": 7} {
		got, err := ParseUint(s, 64)
		if err != nil || got != want {
			t.Errorf("ParseUint(%q) = %d, %v", s, got, err)
		}
	}
	for s, want := range map[string]int64{"-5": -5, "+5": 5, "0xFF": -1, "0x7F": 127, "-0x10": -16, "-128": -128} {
		got, err := ParseInt(s, 8)
		if err != nil || got != want {
			t.Errorf("ParseInt(%q) = %d, %v", s, got, err)
		}
	}
	for _, s := range []string{"128", "-129", "0x100", "x"} {
		if _, err := ParseInt(s, 8); err == nil {
			t.Errorf("ParseInt(%q) accepted", s)
		}
	}
}

func TestParseValue(t *testing.T) {
	for _, tc := range []struct {
		dt   DataType
		s    string
		node NodeID
		want []byte
	}{
		{Unsigned32, "$NODEID+0x180", 5, []byte{0x85, 1, 0, 0}},
		{Unsigned32, "0x180+$NODEID", 5, []byte{0x85, 1, 0, 0}},
		{Unsigned32, "$nodeid + 0x600", 1, []byte{0x01, 6, 0, 0}},
		{Unsigned8, "$NODEID", 7, []byte{7}},
		{Unsigned16, "", 0, []byte{0, 0}},
		{Integer16, "-2", 0, []byte{0xFE, 0xFF}},
		{Integer16, "0xFFFF", 0, []byte{0xFF, 0xFF}},
		{Boolean, "1", 0, []byte{1}},
		{Real32, "1.5", 0, []byte{0, 0, 0xC0, 0x3F}},
		{Real32, "", 0, []byte{0, 0, 0, 0}},
		{Real32, "0x3FC00000", 0, []byte{0, 0, 0xC0, 0x3F}},
		{Real64, "0x4000000000000000", 0, []byte{0, 0, 0, 0, 0, 0, 0, 0x40}},
		{VisibleString, "hello", 0, []byte("hello")},
		{OctetString, "01 02 0a", 0, []byte{1, 2, 10}},
		{Domain, "0xABCD", 0, []byte{0xAB, 0xCD}},
		{TimeOfDay, "", 0, make([]byte, 6)},
		{TimeOfDay, "010203040506", 0, []byte{1, 2, 3, 4, 5, 6}},
		{UnicodeString, "a", 0, []byte{'a', 0}},
	} {
		got, err := ParseValue(tc.dt, tc.s, tc.node)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("ParseValue(%v, %q) = %x, %v", tc.dt, tc.s, got, err)
		}
	}
	for _, tc := range []struct {
		dt DataType
		s  string
	}{
		{Unsigned8, "256"}, {Unsigned8, "x"}, {Integer8, "300"}, {Real32, "abc"}, {OctetString, "zz"},
		{TimeOfDay, "12"}, {PDOMapping, "1"}, {Unsigned8, "$NODEID+0xFF"},
	} {
		if _, err := ParseValue(tc.dt, tc.s, 5); !errors.Is(err, ErrDataType) {
			t.Errorf("ParseValue(%v, %q) = %v", tc.dt, tc.s, err)
		}
	}
}

func TestFormatValue(t *testing.T) {
	for _, tc := range []struct {
		dt   DataType
		b    []byte
		want string
	}{
		{Boolean, []byte{1}, "true"},
		{Integer16, []byte{0xFE, 0xFF}, "-2"},
		{Unsigned16, []byte{0x34, 0x12}, "0x1234"},
		{Real32, []byte{0, 0, 0xC0, 0x3F}, "1.5"},
		{Real64, []byte{0, 0, 0, 0, 0, 0, 0, 0x40}, "2"},
		{VisibleString, []byte("a\"b"), `"a\"b"`},
		{OctetString, []byte{1, 0xAB}, "01ab"},
		{Unsigned32, []byte{1}, "01"},
	} {
		if got := FormatValue(tc.dt, tc.b); got != tc.want {
			t.Errorf("FormatValue(%v, %x) = %q, want %q", tc.dt, tc.b, got, tc.want)
		}
	}
}

func FuzzParseValue(f *testing.F) {
	for _, s := range []string{"0x10", "$NODEID+0x180", "-1", "1.5", "01 02", "", "0777"} {
		f.Add(uint16(Unsigned32), s)
	}
	f.Fuzz(func(t *testing.T, dt uint16, s string) {
		b, err := ParseValue(DataType(dt), s, 1)
		if err != nil {
			return
		}
		if n := DataType(dt).Size(); n != 0 && len(b) != n {
			t.Fatalf("%v %q: %d bytes", DataType(dt), s, len(b))
		}
		_ = FormatValue(DataType(dt), b)
	})
}
