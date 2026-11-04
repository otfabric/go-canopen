package nmt

import (
	"math"
	"time"
)

// maxMillis is the largest number of milliseconds a time.Duration holds.
const maxMillis = math.MaxInt64 / int64(time.Millisecond)

// millis converts a time in ms of the dictionary (0x100C, 0x1017:
// UNSIGNED16) to a duration. A dictionary that gives the object a wider
// type saturates instead of wrapping to a negative duration.
func millis(v uint64) time.Duration {
	if v > uint64(maxMillis) {
		return time.Duration(maxMillis) * time.Millisecond
	}
	return time.Duration(v) * time.Millisecond
}

// lifeFactor converts a life time factor of 0x100D (UNSIGNED8); a
// dictionary that gives it a wider type saturates at 255.
func lifeFactor(v uint64) uint8 {
	if v > math.MaxUint8 {
		return math.MaxUint8
	}
	return uint8(v)
}

// entryOf converts a value of 0x1016 (UNSIGNED32); ok is false for a
// value wider than 32 bits, which a dictionary that gives 0x1016 a wider
// type can hold.
func entryOf(v uint64) (e uint32, ok bool) {
	if v > math.MaxUint32 {
		return 0, false
	}
	return uint32(v), true
}

// mulSat returns d*n, saturated at the largest duration.
func mulSat(d time.Duration, n uint8) time.Duration {
	if d <= 0 || n == 0 {
		return 0
	}
	if d > math.MaxInt64/time.Duration(n) {
		return math.MaxInt64
	}
	return d * time.Duration(n)
}
