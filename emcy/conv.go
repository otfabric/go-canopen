package emcy

import (
	"math"
	"time"

	"github.com/otfabric/go-canopen"
)

// maxInhibitUnits is the largest inhibit time, in 100 µs units, a
// time.Duration holds.
const maxInhibitUnits = math.MaxInt64 / int64(100*time.Microsecond)

// inhibitDuration converts an inhibit time of 0x1015 (UNSIGNED16, 100 µs
// units) to a duration. A dictionary that gives 0x1015 a wider type
// saturates instead of wrapping to a negative duration.
func inhibitDuration(v uint64) time.Duration {
	if v > uint64(maxInhibitUnits) {
		return time.Duration(maxInhibitUnits) * 100 * time.Microsecond
	}
	return time.Duration(v) * 100 * time.Microsecond
}

// cobidOf converts a value of 0x1014 (UNSIGNED32) to a COB-ID; ok is
// false for a value wider than 32 bits, which a dictionary that gives
// 0x1014 a wider type can hold.
func cobidOf(v uint64) (cob canopen.COBID, ok bool) {
	if v > math.MaxUint32 {
		return 0, false
	}
	return canopen.COBID(v), true
}
