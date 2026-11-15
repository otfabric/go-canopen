// SPDX-License-Identifier: MIT

package emcy

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestFDTimeValidity: the time of a CANopen FD emergency message is
// decoded as received, without the check of timestamp.ParseTimeOfDay;
// TimeOfDay.Valid tells a millisecond count within the day from one of a
// day or more, at the boundaries.
func TestFDTimeValidity(t *testing.T) {
	const msPerDay = uint32(24 * time.Hour / time.Millisecond)
	for _, tc := range []struct {
		ms    uint32
		valid bool
	}{
		{0, true},
		{1, true},
		{msPerDay - 1, true},
		{msPerDay, false},
		{msPerDay + 1, false},
		{1<<28 - 1, false}, // the largest count the 28 bits carry
	} {
		var b [FDSize]byte
		binary.LittleEndian.PutUint32(b[14:], tc.ms|0xF0000000) // reserved bits set
		binary.LittleEndian.PutUint16(b[18:], 7)
		m, err := ParseFDMessage(b[:])
		if err != nil {
			t.Fatalf("ms %d: %v", tc.ms, err)
		}
		if m.Time.Ms != tc.ms || m.Time.Days != 7 {
			t.Errorf("ms %d: decoded %+v", tc.ms, m.Time)
		}
		if m.Time.Valid() != tc.valid {
			t.Errorf("ms %d: Valid() = %v, want %v", tc.ms, m.Time.Valid(), tc.valid)
		}
	}
}
