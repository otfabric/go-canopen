// SPDX-License-Identifier: MIT

package node

import (
	"context"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// gatedBus is the bus of a producer that may only transmit in some NMT
// states (EMCY, SYNC, TIME are not sent in Stopped, CiA 301 table 79):
// frames sent while allow reports false are dropped silently and
// counted. sent, when set, sees every frame that went out, so that the
// node can process its own SYNC (a node does not receive its own
// transmissions).
type gatedBus struct {
	canopen.Bus
	allow   func() bool
	dropped func()
	sent    func(*canbus.Frame)
}

// Send transmits f when the gate is open and drops it otherwise.
func (g *gatedBus) Send(ctx context.Context, f *canbus.Frame) error {
	if !g.allow() {
		g.dropped()
		return nil
	}
	if err := g.Bus.Send(ctx, f); err != nil {
		return err
	}
	if g.sent != nil {
		g.sent(f)
	}
	return nil
}
