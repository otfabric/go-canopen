// SPDX-License-Identifier: MIT

package timestamp

import (
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
)

// Epoch is day 0 of TIME_OF_DAY: 1984-01-01 00:00 UTC.
var Epoch = time.Date(1984, time.January, 1, 0, 0, 0, 0, time.UTC)

const (
	day      = 24 * time.Hour
	msPerDay = uint32(day / time.Millisecond)
	msMask   = 1<<28 - 1
)

// Size is the encoded size of TIME_OF_DAY and TIME_DIFFERENCE.
const Size = 6

// TimeOfDay is the TIME_OF_DAY data type: milliseconds after midnight
// (28 bits, 0..86 399 999) and days since [Epoch].
type TimeOfDay struct {
	Ms   uint32
	Days uint16
}

// Latest is the last instant TIME_OF_DAY can represent (day 65535,
// 2163-06-05, at 23:59:59.999 UTC).
var Latest = TimeOfDay{Ms: msPerDay - 1, Days: 0xFFFF}.Time()

// FromTime converts a time to TIME_OF_DAY in UTC, truncated to the
// millisecond. Times before [Epoch] or after [Latest] are an error.
func FromTime(t time.Time) (TimeOfDay, error) {
	u := t.UTC()
	if u.Before(Epoch) || u.After(Latest.Add(time.Millisecond-1)) {
		return TimeOfDay{}, fmt.Errorf("timestamp: %v outside the TIME_OF_DAY range", t)
	}
	d := u.Sub(Epoch)
	return TimeOfDay{Ms: uint32(d % day / time.Millisecond), Days: uint16(d / day)}, nil
}

// Time returns the instant in UTC.
func (t TimeOfDay) Time() time.Time {
	return Epoch.AddDate(0, 0, int(t.Days)).Add(time.Duration(t.Ms) * time.Millisecond)
}

// Valid reports whether Ms is less than a day.
func (t TimeOfDay) Valid() bool { return t.Ms < msPerDay }

// Bytes returns the 6-byte encoding: milliseconds (UNSIGNED28 in 4 bytes,
// little-endian, upper 4 bits 0) and days (UNSIGNED16, little-endian).
func (t TimeOfDay) Bytes() [Size]byte { return encode(t.Ms, t.Days) }

// String formats the time as RFC 3339 with milliseconds.
func (t TimeOfDay) String() string { return t.Time().Format("2006-01-02T15:04:05.000Z07:00") }

// ParseTimeOfDay decodes a 6-byte TIME_OF_DAY. The 4 reserved bits are
// ignored; a millisecond count of a day or more is an
// [canopen.ErrDataType] error.
func ParseTimeOfDay(b []byte) (TimeOfDay, error) {
	ms, days, err := decode(b)
	if err != nil {
		return TimeOfDay{}, err
	}
	if ms >= msPerDay {
		return TimeOfDay{}, fmt.Errorf("%w: TIME_OF_DAY with %d ms after midnight", canopen.ErrDataType, ms)
	}
	return TimeOfDay{Ms: ms, Days: days}, nil
}

// TimeDifference is the TIME_DIFFERENCE data type: a duration of days and
// milliseconds (28 bits, less than a day), with the TIME_OF_DAY layout.
type TimeDifference struct {
	Ms   uint32
	Days uint16
}

// MaxTimeDifference is the longest TIME_DIFFERENCE: 65535 days and
// 86 399 999 ms.
const MaxTimeDifference = 0xFFFF*day + day - time.Millisecond

// FromDuration converts a duration (0..[MaxTimeDifference]) to
// TIME_DIFFERENCE, truncated to the millisecond.
func FromDuration(d time.Duration) (TimeDifference, error) {
	if d < 0 || d > MaxTimeDifference {
		return TimeDifference{}, fmt.Errorf("timestamp: duration %v outside the TIME_DIFFERENCE range", d)
	}
	return TimeDifference{Ms: uint32(d % day / time.Millisecond), Days: uint16(d / day)}, nil
}

// Duration returns the duration.
func (t TimeDifference) Duration() time.Duration {
	return time.Duration(t.Days)*day + time.Duration(t.Ms)*time.Millisecond
}

// Bytes returns the 6-byte encoding, as [TimeOfDay.Bytes].
func (t TimeDifference) Bytes() [Size]byte { return encode(t.Ms, t.Days) }

// String formats the duration.
func (t TimeDifference) String() string { return t.Duration().String() }

// ParseTimeDifference decodes a 6-byte TIME_DIFFERENCE, as
// [ParseTimeOfDay].
func ParseTimeDifference(b []byte) (TimeDifference, error) {
	ms, days, err := decode(b)
	if err != nil {
		return TimeDifference{}, err
	}
	if ms >= msPerDay {
		return TimeDifference{}, fmt.Errorf("%w: TIME_DIFFERENCE with %d ms", canopen.ErrDataType, ms)
	}
	return TimeDifference{Ms: ms, Days: days}, nil
}

func encode(ms uint32, days uint16) [Size]byte {
	ms &= msMask
	return [Size]byte{byte(ms), byte(ms >> 8), byte(ms >> 16), byte(ms >> 24), byte(days), byte(days >> 8)}
}

func decode(b []byte) (uint32, uint16, error) {
	if len(b) != Size {
		return 0, 0, fmt.Errorf("%w: TIME_OF_DAY needs %d bytes, got %d", canopen.ErrDataType, Size, len(b))
	}
	ms := (uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24) & msMask
	return ms, uint16(b[4]) | uint16(b[5])<<8, nil
}
