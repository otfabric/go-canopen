// SPDX-License-Identifier: MIT

package sdo

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// TestDownloadNaNToLimitedFloat: an SDO download of NaN to a REAL32 or
// REAL64 object with limits is aborted with AbortInvalidValue (0609
// 0030) and leaves the value unchanged; values within the limits are
// stored.
func TestDownloadNaNToLimitedFloat(t *testing.T) {
	c, _, d := setup(t, nil)
	r32 := od.NewVar(0x2010, "real32 0..100", canopen.Real32, od.AccessRW, float32(50))
	r32.Subs[0].Low, _ = canopen.Encode(canopen.Real32, float32(0))
	r32.Subs[0].High, _ = canopen.Encode(canopen.Real32, float32(100))
	r64 := od.NewVar(0x2011, "real64 0..100", canopen.Real64, od.AccessRW, float64(50))
	r64.Subs[0].Low, _ = canopen.Encode(canopen.Real64, float64(0))
	r64.Subs[0].High, _ = canopen.Encode(canopen.Real64, float64(100))
	for _, o := range []*od.Object{r32, r64} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	nan32 := binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(math.NaN())))
	nan64 := binary.LittleEndian.AppendUint64(nil, math.Float64bits(math.NaN()))
	for _, tc := range []struct {
		name  string
		index uint16
		data  []byte
	}{
		{"REAL32", 0x2010, nan32},
		{"REAL64", 0x2011, nan64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := d.Get(tc.index, 0)
			err := c.Download(ctxT(t), tc.index, 0, tc.data)
			var ae *canopen.AbortError
			if !errors.As(err, &ae) || !ae.Remote || ae.Code != canopen.AbortInvalidValue {
				t.Fatalf("download of NaN = %v, want the server's abort %v", err, canopen.AbortInvalidValue)
			}
			if after, _ := d.Get(tc.index, 0); string(after) != string(before) {
				t.Errorf("value changed to %x", after)
			}
		})
	}
	ok, _ := canopen.Encode(canopen.Real32, float32(100))
	if err := c.Download(ctxT(t), 0x2010, 0, ok); err != nil {
		t.Errorf("download of the high limit: %v", err)
	}
}
