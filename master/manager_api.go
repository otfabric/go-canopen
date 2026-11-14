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
// ends. When the master is closed it returns an error that matches
// [canopen.ErrClosed] (and, for one that was in progress,
// context.Canceled); after Close it sends nothing.
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
//
// A network boot-up ([Master.BootNetwork], or the automatic one) that
// starts while the process runs cancels it before it resets the slaves:
// BootSlave then returns an error that matches context.Canceled, no
// result is recorded, and the network boot-up boots the node again. When
// the master is closed BootSlave fails with an error that matches
// [canopen.ErrClosed].
func (m *Master) BootSlave(ctx context.Context, id canopen.NodeID) error {
	g := m.mgr
	if g == nil {
		return ErrNoDictionary
	}
	if err := id.Check(); err != nil {
		return err
	}
	if m.isClosed() {
		return canopen.ErrClosed
	}
	// The process is registered like one the manager starts, so that a
	// network boot-up can cancel it and wait for its end.
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	g.mu.Lock()
	s := g.slave(id)
	if s.booting {
		g.mu.Unlock()
		return fmt.Errorf("master: boot slave %d: %w", id, canopen.ErrBusy)
	}
	s.booting, s.cancel, s.done = true, cancel, done
	g.mu.Unlock()
	defer close(done)
	defer context.AfterFunc(m.bootCtx, cancel)() // Close cancels it too
	st, status, err := g.bootSlave(pctx, id)
	switch {
	case m.isClosed():
		// Closed while the process ran: whatever ended it (the context
		// above, a closed SDO client), no result is recorded.
		g.bootCancelled(id)
		return errClosed
	case pctx.Err() != nil && ctx.Err() == nil:
		g.bootCancelled(id)
		return fmt.Errorf("master: boot slave %d: cancelled by a network boot-up: %w", id, context.Canceled)
	}
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
