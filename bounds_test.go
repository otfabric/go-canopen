// SPDX-License-Identifier: MIT

package canopen

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

// A $NODEID literal whose sum leaves the 64-bit range used to wrap around
// (UNSIGNED64 0xFFFFFFFFFFFFFFFF+$NODEID gave 0 for node 1) and be
// accepted; it must be rejected.
func TestParseValueNodeIDOverflow(t *testing.T) {
	for _, tc := range []struct {
		t    DataType
		lit  string
		node NodeID
	}{
		{Unsigned64, "0xFFFFFFFFFFFFFFFF+$NODEID", 1},
		{Unsigned64, "$NODEID+0xFFFFFFFFFFFFFF82", 127},
		{Integer64, "9223372036854775807+$NODEID", 1},
		{Integer64, "$NODEID+0x7FFFFFFFFFFFFFFF", 127},
	} {
		if b, err := ParseValue(tc.t, tc.lit, tc.node); !errors.Is(err, ErrDataType) {
			t.Errorf("%s %q node %d = %x, %v; want ErrDataType", tc.t, tc.lit, tc.node, b, err)
		}
	}
	for _, tc := range []struct {
		t    DataType
		lit  string
		node NodeID
		want []byte
	}{
		{Unsigned64, "0xFFFFFFFFFFFFFFFE+$NODEID", 1, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{Integer64, "9223372036854775806+$NODEID", 1, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}},
		{Integer64, "-9223372036854775808+$NODEID", 1, []byte{1, 0, 0, 0, 0, 0, 0, 0x80}},
		{Integer8, "-128+$NODEID", 5, []byte{0x85}},
		{Integer8, "$NODEID", 127, []byte{0x7F}},
	} {
		b, err := ParseValue(tc.t, tc.lit, tc.node)
		if err != nil || !bytes.Equal(b, tc.want) {
			t.Errorf("%s %q node %d = %x, %v; want %x", tc.t, tc.lit, tc.node, b, err, tc.want)
		}
	}
}

// A REAL32 bit pattern of more than 32 bits used to be truncated silently.
func TestParseValueReal32PatternRange(t *testing.T) {
	if b, err := ParseValue(Real32, "0x1FFFFFFFF", 0); !errors.Is(err, ErrDataType) {
		t.Errorf("0x1FFFFFFFF = %x, %v; want ErrDataType", b, err)
	}
	b, err := ParseValue(Real32, "0xFFFFFFFF", 0)
	if err != nil || !bytes.Equal(b, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("0xFFFFFFFF = %x, %v", b, err)
	}
}

func TestParseDataTypeRange(t *testing.T) {
	if dt, err := ParseDataType("0x10007"); err == nil {
		t.Errorf("0x10007 = %s, want an error", dt)
	}
	if dt, err := ParseDataType("0xFFFF"); err != nil || dt != DataType(0xFFFF) {
		t.Errorf("0xFFFF = %s, %v", dt, err)
	}
}

// The two's complement conversions at the 64-bit boundaries.
func TestIntegerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		b    []byte
		want int64
	}{
		{[]byte{0, 0, 0, 0, 0, 0, 0, 0x80}, math.MinInt64},
		{[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F}, math.MaxInt64},
		{[]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, -1},
		{[]byte{0x80}, -128},
		{[]byte{0x7F}, 127},
		{[]byte{0x00, 0x80, 0x00}, 0x8000},
		{[]byte{0x00, 0x00, 0x80}, -0x800000},
	} {
		if got := DecodeInt(tc.b); got != tc.want {
			t.Errorf("DecodeInt(%x) = %d, want %d", tc.b, got, tc.want)
		}
	}
	for _, tc := range []struct {
		s    string
		bits int
		want int64
	}{
		{"-9223372036854775808", 64, math.MinInt64},
		{"9223372036854775807", 64, math.MaxInt64},
		{"0xFFFFFFFFFFFFFFFF", 64, -1},
		{"0x8000000000000000", 64, math.MinInt64},
		{"0x80", 8, -128},
		{"-128", 8, -128},
	} {
		if got, err := ParseInt(tc.s, tc.bits); err != nil || got != tc.want {
			t.Errorf("ParseInt(%q, %d) = %d, %v; want %d", tc.s, tc.bits, got, err, tc.want)
		}
	}
	for _, s := range []string{"-9223372036854775809", "9223372036854775808"} {
		if got, err := ParseInt(s, 64); err == nil {
			t.Errorf("ParseInt(%q) = %d, want a range error", s, got)
		}
	}
	for _, tc := range []struct {
		t    DataType
		v    any
		want []byte
	}{
		{Integer64, int64(math.MinInt64), []byte{0, 0, 0, 0, 0, 0, 0, 0x80}},
		{Integer64, int(-1), []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{Integer8, int8(-128), []byte{0x80}},
		{Integer16, int16(-2), []byte{0xFE, 0xFF}},
		{Integer32, int32(-3), []byte{0xFD, 0xFF, 0xFF, 0xFF}},
		{Unsigned64, uint64(math.MaxUint64), []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{Unsigned32, uint(7), []byte{7, 0, 0, 0}},
	} {
		if b, err := Encode(tc.t, tc.v); err != nil || !bytes.Equal(b, tc.want) {
			t.Errorf("Encode(%s, %v) = %x, %v; want %x", tc.t, tc.v, b, err, tc.want)
		}
	}
	for _, tc := range []struct {
		t DataType
		v any
	}{
		{Integer64, uint64(1) << 63},
		{Integer8, uint(128)},
		{Unsigned8, int(-1)},
		{Unsigned64, int64(math.MinInt64)},
	} {
		if b, err := Encode(tc.t, tc.v); !errors.Is(err, ErrDataType) {
			t.Errorf("Encode(%s, %v) = %x, %v; want ErrDataType", tc.t, tc.v, b, err)
		}
	}
}
