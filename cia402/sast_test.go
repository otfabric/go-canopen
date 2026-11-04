// SPDX-License-Identifier: MIT

package cia402

import (
	"math"
	"testing"
)

// The wire conversions round-trip every boundary value bit for bit.
func TestWireConversionsBoundaries(t *testing.T) {
	for _, v := range []int8{math.MinInt8, -1, 0, 1, math.MaxInt8} {
		if got := int8Of(wire8(v)); got != v {
			t.Errorf("int8 %d -> %#x -> %d", v, wire8(v), got)
		}
	}
	if wire8(-1) != 0xFF || wire8(math.MinInt8) != 0x80 {
		t.Errorf("u8: %#x %#x", wire8(-1), wire8(math.MinInt8))
	}
	for _, v := range []int16{math.MinInt16, -1, 0, 1, math.MaxInt16} {
		if got := int16Of(wire16(v)); got != v {
			t.Errorf("int16 %d -> %#x -> %d", v, wire16(v), got)
		}
	}
	if wire16(-1) != 0xFFFF || wire16(math.MinInt16) != 0x8000 {
		t.Errorf("u16: %#x %#x", wire16(-1), wire16(math.MinInt16))
	}
	for _, v := range []int32{math.MinInt32, -1, 0, 1, math.MaxInt32} {
		if got := int32Of(wire32(v)); got != v {
			t.Errorf("int32 %d -> %#x -> %d", v, wire32(v), got)
		}
	}
	if wire32(-1) != 0xFFFFFFFF || wire32(math.MinInt32) != 0x80000000 {
		t.Errorf("u32: %#x %#x", wire32(-1), wire32(math.MinInt32))
	}
}

// A mode outside INTEGER8 cannot come from the type-checked 0x6060; it maps
// to ModeNone instead of wrapping to an unrelated mode.
func TestModeOfOutOfRange(t *testing.T) {
	for x, want := range map[int64]Mode{-129: ModeNone, -128: Mode(-128), 8: ModeCSP, 127: Mode(127), 264: ModeNone} {
		if got := modeOf(x); got != want {
			t.Errorf("modeOf(%d) = %d, want %d", x, got, want)
		}
	}
}
