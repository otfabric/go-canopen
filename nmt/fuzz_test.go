// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// fuzzFrame builds a frame from fuzz input: identifier, flags, length and
// payload, without validation (decoders must cope with anything).
func fuzzFrame(id uint32, flags uint8, data []byte) canbus.Frame {
	var f canbus.Frame
	f.ID = canbus.ID(id)
	f.Flags = canbus.Flags(flags)
	n := copy(f.Data[:], data)
	f.Len = uint8(n)
	return f
}

func FuzzParseCommand(f *testing.F) {
	f.Add(uint32(0), uint8(0), []byte{0x01, 0x05})
	f.Add(uint32(0), uint8(0), []byte{0x81, 0x00})
	f.Add(uint32(0), uint8(2), []byte{0x01})
	f.Add(uint32(0x700), uint8(0), []byte{0x82, 0xFF, 0x00})
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte) {
		fr := fuzzFrame(id, flags, data)
		cmd, node, err := ParseCommand(&fr)
		if err == nil {
			g := CommandFrame(node, cmd)
			if g.Data[0] != fr.Data[0] || g.Data[1] != fr.Data[1] {
				t.Fatalf("round trip %v %v", fr, g)
			}
		}
		s := &Slave{cfg: newConfig(nil), node: 5, state: canopen.StatePreOperational, bus: nopBus{}}
		s.handle(context.Background(), &fr)
	})
}

func FuzzParseErrorControl(f *testing.F) {
	f.Add(uint32(0x705), uint8(0), []byte{0x05})
	f.Add(uint32(0x77F), uint8(0), []byte{0xFF})
	f.Add(uint32(0x700), uint8(0), []byte{0x00})
	f.Add(uint32(0x701), uint8(2), []byte{})
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte) {
		fr := fuzzFrame(id, flags, data)
		node, st, toggle, err := ParseErrorControl(&fr)
		if err == nil {
			g := ErrorControlFrame(node, st, toggle)
			if g.ID != fr.ID || g.Data[0] != fr.Data[0] {
				t.Fatalf("round trip %v %v", fr, g)
			}
		}
		c, _ := NewHeartbeatConsumer(nopBus{})
		_ = c.Monitor(5, time.Second)
		c.receive(&fr, time.Now())
		c.checkTimeouts(time.Now().Add(2 * time.Second))
		g := NewGuardMaster(nopBus{})
		_ = g.Guard(5, time.Second, 1)
		g.nodes[5].outstanding = true
		g.receive(&fr, time.Now())
	})
}

// nopBus discards sent frames and has no subscriptions.
type nopBus struct{}

func (nopBus) Send(context.Context, *canbus.Frame) error { return nil }
func (nopBus) Subscribe(...canbus.SubscribeOption) (*canbus.Subscription, error) {
	return nil, canopen.ErrClosed
}
