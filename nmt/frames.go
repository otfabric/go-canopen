// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// CommandFrame returns the NMT module control frame (CAN-ID 0x000) of a
// command for one node, or for all nodes when node is 0.
func CommandFrame(node canopen.NodeID, cmd canopen.NMTCommand) canbus.Frame {
	f, _ := canbus.NewStandard(canopen.FuncNMT, []byte{byte(cmd), byte(node)})
	return f
}

// ParseCommand decodes an NMT module control frame: a classical data
// frame with the 11-bit identifier 0x000, two bytes, a known command and a
// node-ID 0..127. Anything else is an [canopen.ErrProtocol] error.
func ParseCommand(f *canbus.Frame) (canopen.NMTCommand, canopen.NodeID, error) {
	if f.ID != canopen.FuncNMT || f.Flags&(canbus.FlagExtended|canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) != 0 {
		return 0, 0, fmt.Errorf("%w: not an NMT command frame", canopen.ErrProtocol)
	}
	if f.Len != 2 {
		return 0, 0, fmt.Errorf("%w: NMT command with %d bytes", canopen.ErrProtocol, f.Len)
	}
	cmd, node := canopen.NMTCommand(f.Data[0]), canopen.NodeID(f.Data[1])
	if !cmd.Valid() {
		return 0, 0, fmt.Errorf("%w: unknown NMT command 0x%02X", canopen.ErrProtocol, f.Data[0])
	}
	if node > canopen.MaxNodeID {
		return 0, 0, fmt.Errorf("%w: NMT command for node %d", canopen.ErrProtocol, node)
	}
	return cmd, node, nil
}

// ErrorControlFrame returns the NMT error control frame (CAN-ID 0x700 +
// node) that reports state: the boot-up message for
// [canopen.StateInitialising], a heartbeat otherwise. toggle sets bit 7, as
// in node guarding responses.
func ErrorControlFrame(node canopen.NodeID, state canopen.NMTState, toggle bool) canbus.Frame {
	b := byte(state) & 0x7F
	if toggle {
		b |= 0x80
	}
	f, _ := canbus.NewStandard(canopen.FuncHeartbeat+canbus.ID(node), []byte{b})
	return f
}

// GuardRequest returns the node guarding remote frame for a node: an RTR
// on 0x700 + node with a data length of 1.
func GuardRequest(node canopen.NodeID) canbus.Frame {
	f, _ := canbus.NewRemote(canopen.FuncHeartbeat+canbus.ID(node), false, 1)
	return f
}

// ParseErrorControl decodes an NMT error control data frame (boot-up,
// heartbeat or node guarding response): an 11-bit identifier 0x701..0x77F
// and one byte holding a known state in bits 0..6 and the toggle bit 7.
// Anything else is an [canopen.ErrProtocol] error.
func ParseErrorControl(f *canbus.Frame) (node canopen.NodeID, state canopen.NMTState, toggle bool, err error) {
	if f.Flags&(canbus.FlagExtended|canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) != 0 ||
		f.ID&^0x7F != canopen.FuncHeartbeat {
		return 0, 0, false, fmt.Errorf("%w: not an NMT error control frame", canopen.ErrProtocol)
	}
	node = canopen.NodeID(f.ID & 0x7F)
	if !node.Valid() {
		return 0, 0, false, fmt.Errorf("%w: error control frame of node 0", canopen.ErrProtocol)
	}
	if f.Len != 1 {
		return 0, 0, false, fmt.Errorf("%w: error control frame with %d bytes", canopen.ErrProtocol, f.Len)
	}
	state = canopen.NMTState(f.Data[0] & 0x7F)
	switch state {
	case canopen.StateInitialising, canopen.StateStopped, canopen.StateOperational, canopen.StatePreOperational:
	default:
		return 0, 0, false, fmt.Errorf("%w: unknown NMT state 0x%02X", canopen.ErrProtocol, f.Data[0]&0x7F)
	}
	return node, state, f.Data[0]&0x80 != 0, nil
}

// errorControlFilter selects the data frames 0x700..0x77F.
var errorControlFilter = canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{{
	ID: canopen.FuncHeartbeat, Mask: 0x780, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly,
}}})

// Master sends NMT module control commands. It has no goroutine; it is
// safe for concurrent use.
type Master struct {
	bus  canopen.Bus
	cfg  config
	sent atomic.Uint64
}

// MasterStats counts what a [Master] did.
type MasterStats struct {
	Sent uint64 `json:"sent"` // commands sent
}

// NewMaster returns an NMT master on bus. Options: [WithLogger].
func NewMaster(bus canopen.Bus, opts ...Option) *Master {
	return &Master{bus: bus, cfg: newConfig(opts)}
}

// Send sends an NMT command to a node, or to all nodes when node is 0
// ([canopen.BroadcastNodeID]).
func (m *Master) Send(ctx context.Context, node canopen.NodeID, cmd canopen.NMTCommand) error {
	if node != canopen.BroadcastNodeID {
		if err := node.Check(); err != nil {
			return err
		}
	}
	if !cmd.Valid() {
		return fmt.Errorf("nmt: invalid command 0x%02X", uint8(cmd))
	}
	f := CommandFrame(node, cmd)
	if err := m.bus.Send(ctx, &f); err != nil {
		return fmt.Errorf("nmt: send %s to node %d: %w", cmd, node, err)
	}
	m.sent.Add(1)
	m.cfg.log.Debug("nmt command sent", "command", cmd.String(), "node", int(node))
	return nil
}

// Stats returns the counters.
func (m *Master) Stats() MasterStats { return MasterStats{Sent: m.sent.Load()} }
