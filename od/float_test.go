// SPDX-License-Identifier: MIT

package od

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
)

// limitedFloat returns a VAR of type t with limits lo and hi (nil: none).
func limitedFloat(t canopen.DataType, index uint16, lo, hi any) *Object {
	o := NewVar(index, "float", t, AccessRW, nil)
	if lo != nil {
		o.Subs[0].Low, _ = canopen.Encode(t, lo)
	}
	if hi != nil {
		o.Subs[0].High, _ = canopen.Encode(t, hi)
	}
	return o
}

// TestValidateFloatLimits: a limited REAL32 or REAL64 refuses NaN with
// AbortInvalidValue and ±Inf outside its limits; its limits themselves
// and ±Inf within them are accepted; without limits NaN is a value like
// any other.
func TestValidateFloatLimits(t *testing.T) {
	inf, nan := math.Inf(1), math.NaN()
	for _, dt := range []canopen.DataType{canopen.Real32, canopen.Real64} {
		val := func(f float64) any {
			if dt == canopen.Real32 {
				return float32(f)
			}
			return f
		}
		limited := limitedFloat(dt, 0x2100, val(0), val(100)).Subs[0]
		lowOnly := limitedFloat(dt, 0x2101, val(0), nil).Subs[0]
		highOnly := limitedFloat(dt, 0x2102, nil, val(100)).Subs[0]
		open := limitedFloat(dt, 0x2103, nil, nil).Subs[0]
		for _, tc := range []struct {
			name string
			v    *Variable
			x    float64
			want error
		}{
			{"NaN, limits", limited, nan, canopen.AbortInvalidValue},
			{"NaN, low limit", lowOnly, nan, canopen.AbortInvalidValue},
			{"NaN, high limit", highOnly, nan, canopen.AbortInvalidValue},
			{"NaN, no limit", open, nan, nil},
			{"+Inf, limits", limited, inf, canopen.AbortValueHigh},
			{"-Inf, limits", limited, -inf, canopen.AbortValueLow},
			{"+Inf, low limit", lowOnly, inf, nil},
			{"-Inf, high limit", highOnly, -inf, nil},
			{"low limit", limited, 0, nil},
			{"high limit", limited, 100, nil},
			{"below", limited, -1, canopen.AbortValueLow},
			{"above", limited, 200, canopen.AbortValueHigh},
		} {
			b, err := canopen.Encode(dt, val(tc.x))
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(tc.v, b); !errors.Is(err, tc.want) && err != tc.want {
				t.Errorf("%s %s: Validate = %v, want %v", dt, tc.name, err, tc.want)
			}
		}
	}
}

// TestRemoteWriteNaN: a remote write of NaN to a limited float is refused
// and leaves the value unchanged.
func TestRemoteWriteNaN(t *testing.T) {
	d := New(5)
	if err := d.Add(limitedFloat(canopen.Real32, 0x2100, float32(0), float32(100))); err != nil {
		t.Fatal(err)
	}
	nan, _ := canopen.Encode(canopen.Real32, float32(math.NaN()))
	if err := d.Write(0x2100, 0, nan); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Fatalf("Write of NaN = %v, want %v", err, canopen.AbortInvalidValue)
	}
	if v, _ := d.GetValue(0x2100, 0); v != float32(0) {
		t.Errorf("value %v after the refused write, want 0", v)
	}
}

// TestAddRefusesNaNLimit: Add refuses a float variable with a NaN limit,
// and accepts ±Inf limits.
func TestAddRefusesNaNLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		o      *Object
		refuse string
	}{
		{"REAL32 low", limitedFloat(canopen.Real32, 0x2100, float32(math.NaN()), float32(1)), "low limit"},
		{"REAL32 high", limitedFloat(canopen.Real32, 0x2100, float32(0), float32(math.NaN())), "high limit"},
		{"REAL64 low", limitedFloat(canopen.Real64, 0x2100, math.NaN(), nil), "low limit"},
		{"REAL64 high", limitedFloat(canopen.Real64, 0x2100, nil, math.NaN()), "high limit"},
		{"REAL32 infinite limits", limitedFloat(canopen.Real32, 0x2100, float32(math.Inf(-1)), float32(math.Inf(1))), ""},
		{"REAL64 infinite limits", limitedFloat(canopen.Real64, 0x2100, math.Inf(-1), math.Inf(1)), ""},
	} {
		err := New(5).Add(tc.o)
		switch {
		case tc.refuse == "" && err != nil:
			t.Errorf("%s: Add = %v", tc.name, err)
		case tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)):
			t.Errorf("%s: Add = %v, want an error naming the %s", tc.name, err, tc.refuse)
		}
	}
}
