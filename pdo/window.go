// SPDX-License-Identifier: MIT

package pdo

import (
	"math"
	"time"

	"github.com/otfabric/go-canopen/od"
)

// syncWindow returns the synchronous window length of the device: object
// 0x1007 (µs) of its dictionary; 0 (no window) when the object is absent
// or its value does not fit the UNSIGNED32 of the object.
func (b *base) syncWindow() time.Duration {
	x, err := b.d.GetUint(od.IndexSyncWindow, 0)
	if err != nil {
		return 0
	}
	if x > math.MaxUint32 {
		return 0
	}
	return time.Duration(x) * time.Microsecond
}

// windowEnd returns the end of the synchronous window of a SYNC received
// now, or the zero time when no window is configured.
func (b *base) windowEnd() time.Time {
	w := b.syncWindow()
	if w <= 0 {
		return time.Time{}
	}
	return b.opt.clock().Add(w)
}

// closed reports whether the window that ends at end has closed; a zero
// end is no window, which never closes.
func (b *base) windowClosed(end time.Time) bool {
	return !end.IsZero() && !b.opt.clock().Before(end)
}
