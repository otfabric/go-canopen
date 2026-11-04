// SPDX-License-Identifier: MIT

package sdo

import "fmt"

// sizeField returns n as the 32-bit size of an initiate frame, false when
// it does not fit (CiA 301 sizes are UNSIGNED32).
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

// errTooLarge reports data that does not fit an SDO transfer.
func errTooLarge(n int) error {
	return fmt.Errorf("sdo: %d bytes do not fit an SDO transfer", n)
}

// u8 clamps n to 0..255; callers pass values already in range (counts of
// unused segment bytes, validated block sizes and thresholds).
func u8(n int) uint8 {
	if n < 0 {
		return 0
	}
	if n > 0xFF {
		return 0xFF
	}
	return uint8(n)
}
