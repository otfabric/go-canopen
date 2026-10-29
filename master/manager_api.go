// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/node"
)

// ErrNoDictionary reports a CiA 302 manager operation on a master without
// [WithDictionary].
var ErrNoDictionary = errors.New("master: no dictionary (WithDictionary)")

// Node returns the master's own node of [WithDictionary], or nil.
func (m *Master) Node() *node.Node {
	if m.mgr == nil {
		return nil
	}
	return m.mgr.n
}

// BootNetwork runs the CiA 302-2 network boot-up now (it runs on its own
// at every boot-up of the master's node when 0x1F80 bit 0 is set),
// replacing one in progress: the slaves of the network list (0x1F81) are
// reset (communication), the mandatory and keep-alive ones booted at once
// and the others when their boot-up message arrives; it returns when every
// mandatory slave booted (nil, after which the master enters Operational
// unless 0x1F80 bit 2 is set, and starts the slaves as 0x1F80 says) or one
// failed (its *BootError: the boot-up halts, the master stays
// pre-operational). Without bit 0 of 0x1F80 it does nothing.
//
// One network boot-up runs at a time: a newer one (another BootNetwork
// call, or the automatic one at a boot-up of the master's node) cancels
// the one in progress, and a call whose boot-up was replaced waits for the
// newer one and returns its result; it returns ctx's error only when ctx
// ends, and the cancellation when the master is closed.
func (m *Master) BootNetwork(ctx context.Context) error {
	if m.mgr == nil {
		return ErrNoDictionary
	}
	return m.mgr.runNetwork(ctx)
}

// BootSlave runs the CiA 302-2 boot slave process of a node now and waits
// for it: the checks of the node's identity (0x1F84..0x1F88), software
// (0x1F55, with an update from 0x1F58 when allowed) and configuration
// (0x1F26/0x1F27 against the node's 0x1020, the concise DCF 0x1F22), then
// the start of the error control and, as 0x1F80 allows, of the node. It
// fails with canopen.ErrBusy while one runs for the node, and with a
// *BootError when the process fails; it is not repeated when the node
// does not answer.
func (m *Master) BootSlave(ctx context.Context, id canopen.NodeID) error {
	g := m.mgr
	if g == nil {
		return ErrNoDictionary
	}
	if err := id.Check(); err != nil {
		return err
	}
	g.mu.Lock()
	s := g.slave(id)
	if s.booting {
		g.mu.Unlock()
		return fmt.Errorf("master: boot slave %d: %w", id, canopen.ErrBusy)
	}
	s.booting = true
	g.mu.Unlock()
	st, status, err := g.bootSlave(ctx, id)
	g.bootDone(id, st, status, err)
	if !status.OK() {
		return &BootError{Node: id, Status: status, Err: err}
	}
	return nil
}

// SlaveStatus returns the result of the last boot slave process of a node:
// done is false before the first one ended and while one runs.
func (m *Master) SlaveStatus(id canopen.NodeID) (status BootStatus, done bool) {
	g := m.mgr
	if g == nil {
		return 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.slaves[id]
	if s == nil || s.booting || !s.booted {
		return 0, false
	}
	return s.status, true
}
