// SPDX-License-Identifier: MIT

package syncobj

import (
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func FuzzReceive(f *testing.F) {
	f.Add(uint32(0x80), uint8(0), []byte{1}, uint8(4), true)
	f.Add(uint32(0x80), uint8(0), []byte{}, uint8(0), true)
	f.Add(uint32(0x80), uint8(2), []byte{}, uint8(0), false)
	f.Add(uint32(0x80), uint8(0), []byte{1, 2, 3, 4, 5, 6, 7, 8}, uint8(240), true)
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte, overflow uint8, known bool) {
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		opts := []Option{WithWindow(1000)}
		if known {
			opts = append(opts, WithOverflow(overflow))
		}
		c, _ := NewConsumer(nil, opts...)
		for i := 0; i < 2; i++ {
			e, ok := c.receive(&fr, canopen.COBID(canopen.FuncSYNC))
			if ok && fr.Len > 0 && e.Counter != fr.Data[0] {
				t.Fatalf("counter %d from % X", e.Counter, fr.Data[:fr.Len])
			}
		}
	})
}
