package cia402

import "math"

// Two's-complement reinterpretations of CANopen INTEGERn wire values. The
// arithmetic form keeps every result provably in range; the guards never
// fire.

// int8Of returns the INTEGER8 encoded in b.
func int8Of(b byte) int8 {
	x := int16(b) - int16(b&0x80)<<1
	if x < math.MinInt8 {
		return math.MinInt8
	}
	if x > math.MaxInt8 {
		return math.MaxInt8
	}
	return int8(x)
}

// int16Of returns the INTEGER16 encoded in u.
func int16Of(u uint16) int16 {
	x := int32(u) - int32(u&0x8000)<<1
	if x < math.MinInt16 {
		return math.MinInt16
	}
	if x > math.MaxInt16 {
		return math.MaxInt16
	}
	return int16(x)
}

// int32Of returns the INTEGER32 encoded in u.
func int32Of(u uint32) int32 {
	x := int64(u) - int64(u&0x80000000)<<1
	if x < math.MinInt32 {
		return math.MinInt32
	}
	if x > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(x)
}

// modeOf returns the mode stored as INTEGER8 value x; values outside the
// INTEGER8 range (impossible for the type-checked 0x6060) map to ModeNone.
func modeOf(x int64) Mode {
	if x < math.MinInt8 {
		return ModeNone
	}
	if x > math.MaxInt8 {
		return ModeNone
	}
	return Mode(x)
}

// wire8 returns the INTEGER8 wire byte of v.
func wire8(v int8) byte { return byte(int16(v) & 0xFF) }

// wire16 returns the INTEGER16 wire value of v.
func wire16(v int16) uint16 { return uint16(int32(v) & 0xFFFF) }

// wire32 returns the INTEGER32 wire value of v.
func wire32(v int32) uint32 { return uint32(int64(v) & 0xFFFFFFFF) }
