// SPDX-License-Identifier: MIT

package usdo

import (
	"fmt"
	"math"
)

// sizeField returns n as the 32-bit size of an init message, false when
// it does not fit (CiA 1301 sizes are UNSIGNED32).
func sizeField(n int) (uint32, bool) {
	v := int64(n)
	if v < 0 {
		return 0, false
	}
	if v > 0xFFFFFFFF {
		return 0, false
	}
	return uint32(v), true
}

// sizeInt returns a 32-bit size field as an int, false when it does not
// fit (sizes of 2 GiB and more on 32-bit platforms).
func sizeInt(u uint32) (int, bool) {
	v := int64(u)
	if v > int64(math.MaxInt) {
		return 0, false
	}
	return int(v), true
}

// errTooLarge reports data that does not fit a USDO transfer.
func errTooLarge(n int) error {
	return fmt.Errorf("usdo: %d bytes do not fit a USDO transfer", n)
}

// u8 clamps n to 0..255; callers pass lengths already checked.
func u8(n int) uint8 {
	if n < 0 {
		return 0
	}
	if n > 0xFF {
		return 0xFF
	}
	return uint8(n)
}

// u64 returns a byte count for the statistics.
func u64(n int) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}
