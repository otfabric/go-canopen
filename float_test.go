// SPDX-License-Identifier: MIT

package canopen_test

import (
	"errors"
	"math"
	"testing"

	"github.com/otfabric/go-canopen"
)

// TestReal32Range: a REAL32 value from a float64 or a literal is encoded
// when it fits REAL32, NaN and ±Inf included, and refused with
// ErrDataType when it is finite and beyond the range of REAL32.
func TestReal32Range(t *testing.T) {
	maxF := float64(math.MaxFloat32)
	for _, tc := range []struct {
		name string
		x    float64
		ok   bool
	}{
		{"largest REAL32", maxF, true},
		{"smallest REAL32", -maxF, true},
		{"zero", 0, true},
		{"+Inf", math.Inf(1), true},
		{"-Inf", math.Inf(-1), true},
		{"NaN", math.NaN(), true},
		{"1e39", 1e39, false},
		{"-1e39", -1e39, false},
		{"largest float64", math.MaxFloat64, false},
		{"twice the largest REAL32", 2 * maxF, false},
	} {
		b, err := canopen.Encode(canopen.Real32, tc.x)
		switch {
		case tc.ok && err != nil:
			t.Errorf("Encode(REAL32, %s) = %v", tc.name, err)
		case !tc.ok && !errors.Is(err, canopen.ErrDataType):
			t.Errorf("Encode(REAL32, %s) = %x, %v, want ErrDataType", tc.name, b, err)
		}
	}
	for _, tc := range []struct {
		lit string
		ok  bool
	}{
		{"3.4028234663852886e38", true}, // math.MaxFloat32
		{"-3.4028234663852886e38", true},
		{"1e39", false},
		{"-1e39", false},
		{"1e300", false},
		{"NaN", true},
		{"+Inf", true},
		{"-Inf", true},
		{"0x7F800000", true}, // the raw pattern of +Inf
		{"1.5", true},
	} {
		b, err := canopen.ParseValue(canopen.Real32, tc.lit, 0)
		switch {
		case tc.ok && err != nil:
			t.Errorf("ParseValue(REAL32, %q) = %v", tc.lit, err)
		case !tc.ok && !errors.Is(err, canopen.ErrDataType):
			t.Errorf("ParseValue(REAL32, %q) = %x, %v, want ErrDataType", tc.lit, b, err)
		}
	}
	if b, err := canopen.ParseValue(canopen.Real64, "1e300", 0); err != nil || len(b) != 8 {
		t.Errorf("ParseValue(REAL64, 1e300) = %x, %v", b, err)
	}
}
