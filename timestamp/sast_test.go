// SPDX-License-Identifier: MIT

package timestamp

import (
	"testing"
	"time"
)

// TIME_OF_DAY boundaries: the first and last representable instants
// convert exactly; one step outside is an error rather than a wrapped day.
func TestFromTimeBoundaries(t *testing.T) {
	if got, err := FromTime(Epoch); err != nil || got != (TimeOfDay{}) {
		t.Fatalf("Epoch: %v %v", got, err)
	}
	last := Latest.Add(time.Millisecond - 1)
	if got, err := FromTime(last); err != nil || got != (TimeOfDay{Ms: msPerDay - 1, Days: 0xFFFF}) {
		t.Fatalf("Latest: %v %v", got, err)
	}
	for _, bad := range []time.Time{Epoch.Add(-time.Nanosecond), Latest.Add(time.Millisecond), {}} {
		if _, err := FromTime(bad); err == nil {
			t.Errorf("FromTime(%v): no error", bad)
		}
	}
}

func TestFromDurationBoundaries(t *testing.T) {
	if got, err := FromDuration(MaxTimeDifference); err != nil || got != (TimeDifference{Ms: msPerDay - 1, Days: 0xFFFF}) {
		t.Fatalf("max: %v %v", got, err)
	}
	for _, bad := range []time.Duration{-time.Nanosecond, MaxTimeDifference + time.Millisecond} {
		if _, err := FromDuration(bad); err == nil {
			t.Errorf("FromDuration(%v): no error", bad)
		}
	}
}

func TestEncodeMasksReservedBits(t *testing.T) {
	want := [Size]byte{0xFF, 0xFF, 0xFF, 0x0F, 0xFF, 0xFF}
	if got := encode(0xFFFFFFFF, 0xFFFF); got != want {
		t.Fatalf("encode = % x, want % x", got, want)
	}
}

func TestSplitClamps(t *testing.T) {
	if ms, days := split(-time.Hour); ms != 0 || days != 0 {
		t.Errorf("split(-1h) = %d %d", ms, days)
	}
	if ms, days := split(MaxTimeDifference + 2*day); ms != msPerDay-1 || days != 0xFFFF {
		t.Errorf("split(max+2d) = %d %d", ms, days)
	}
}

func TestCOBIDMask(t *testing.T) {
	if got := cobID(0x1_8000_0100); got != 0x8000_0100 {
		t.Fatalf("cobID = %#x", got)
	}
}
