// SPDX-License-Identifier: MIT

package canopen

import (
	"context"

	"github.com/otfabric/go-canbus"
)

// Bus is what the CANopen services need from the CAN layer: sending frames
// and subscribing to received ones. *canbus.Bus implements it, on every
// go-canbus transport. A node does not receive its own transmissions (as
// on a real bus), so two services that talk to each other in one process
// need two buses (two ports of a virtual bus, or two SocketCAN sockets).
type Bus interface {
	Send(ctx context.Context, f *canbus.Frame) error
	Subscribe(opts ...canbus.SubscribeOption) (*canbus.Subscription, error)
}

var _ Bus = (*canbus.Bus)(nil)
