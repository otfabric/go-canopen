// SPDX-License-Identifier: MIT

package emcy

import (
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func FuzzParseFrame(f *testing.F) {
	f.Add(uint32(0x085), uint8(0), []byte{0x30, 0x81, 0x11, 1, 2, 3, 4, 5})
	f.Add(uint32(0x080), uint8(0), []byte{0x00, 0x10, 0x01})
	f.Add(uint32(0x0FF), uint8(2), []byte{})
	f.Add(uint32(0x1234567), uint8(1), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte) {
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		if m, err := ParseFrame(&fr); err == nil {
			b := m.Bytes()
			n := min(int(fr.Len), 8)
			for i := 0; i < n; i++ {
				if b[i] != fr.Data[i] {
					t.Fatalf("round trip % X -> % X", fr.Data[:n], b)
				}
			}
			_ = m.String()
		}
		for _, c := range []*Consumer{NewConsumer(nil), NewConsumer(nil, WithProducer(1, canopen.NewCOBID(canbus.ID(id)&canbus.MaxExtendedID)))} {
			c.receive(&fr)
		}
		_ = CodeText(uint16(id))
	})
}
