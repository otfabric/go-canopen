// SPDX-License-Identifier: MIT

package timestamp

import (
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func FuzzParseFrame(f *testing.F) {
	f.Add(uint32(0x100), uint8(0), []byte{0xDC, 0x05, 0, 0, 1, 0})
	f.Add(uint32(0x100), uint8(0), []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Add(uint32(0x100), uint8(2), []byte{})
	f.Add(uint32(0x101), uint8(0), []byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte) {
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		if td, err := ParseFrame(&fr); err == nil {
			if !td.Valid() {
				t.Fatalf("invalid %+v accepted", td)
			}
			back, err := FromTime(td.Time())
			if err != nil || back != td {
				t.Fatalf("round trip %+v -> %+v %v", td, back, err)
			}
		}
		if d, err := ParseTimeDifference(data); err == nil {
			if back, err := FromDuration(d.Duration()); err != nil || back != d {
				t.Fatalf("round trip %+v -> %+v %v", d, back, err)
			}
		}
		c, _ := NewConsumer(nil)
		c.receive(&fr)
		c, _ = NewConsumer(nil, WithCOBID(canopen.COBID(id)))
		c.receive(&fr)
	})
}
