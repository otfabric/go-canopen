// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// DefaultBootTimeout bounds a boot procedure without BootConfig.Timeout.
const DefaultBootTimeout = 10 * time.Second

// ErrIdentity reports a node whose device type or identity is not the
// expected one.
var ErrIdentity = errors.New("master: unexpected device type or identity")

// BootConfig is the boot procedure of a node (a simplified CiA 302 boot
// slave process): check the device, configure it, supervise it, start it.
type BootConfig struct {
	// DeviceType, when not 0, must equal 0x1000.
	DeviceType uint32
	// Identity, when set, must match 0x1018; its fields that are 0 are
	// not checked.
	Identity *Identity
	// Entries are written with [Master.Configure].
	Entries []Entry
	// HeartbeatTimeout, when not 0, makes the master supervise the node's
	// heartbeat with it ([Master.Monitor]); configure the node's producer
	// time (0x1017) in Entries.
	HeartbeatTimeout time.Duration
	// NoStart leaves the node in Pre-operational; by default the procedure
	// ends with NMT start.
	NoStart bool
	// Timeout bounds the whole procedure; 0 means DefaultBootTimeout.
	Timeout time.Duration
}

// SetBoot registers the boot procedure of a node: from then on, every
// boot-up message of the node starts it in the background (a boot-up
// during a procedure cancels it and starts again), and its end is reported
// as EventBootDone or EventBootFailed. nil removes the registration.
func (m *Master) SetBoot(node canopen.NodeID, cfg *BootConfig) error {
	if err := node.Check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg == nil {
		delete(m.boots, node)
		return nil
	}
	c := *cfg
	m.boots[node] = &c
	return nil
}

// bootUp starts the registered boot procedure of a node that booted.
func (m *Master) bootUp(node canopen.NodeID, at time.Time) {
	m.mu.Lock()
	cfg := m.boots[node]
	m.mu.Unlock()
	if g := m.mgr; cfg == nil && g != nil {
		// The CiA 302 boot slave process of a node of the network list.
		if g.isMaster() && g.assignment(node).Has(AssignSlave) {
			g.request(node, at)
		}
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg = m.boots[node]
	if cfg == nil || m.bootCtx.Err() != nil {
		return
	}
	if cancel := m.running[node]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(m.bootCtx)
	m.running[node] = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := m.Boot(ctx, node, *cfg)
		m.mu.Lock()
		superseded := ctx.Err() != nil && m.bootCtx.Err() == nil
		if !superseded {
			delete(m.running, node)
		}
		m.mu.Unlock()
		cancel()
		switch {
		case superseded:
			// A new boot-up restarted the procedure.
		case err != nil:
			m.disp.push(Event{Kind: EventBootFailed, Node: node, State: m.State(node), Err: err})
		default:
			m.disp.push(Event{Kind: EventBootDone, Node: node, State: m.State(node)})
		}
	}()
}

// Boot runs a boot procedure now: device type and identity check,
// configuration, heartbeat supervision and NMT start. The node must be
// pre-operational (or operational) for its SDO server to answer.
func (m *Master) Boot(ctx context.Context, node canopen.NodeID, cfg BootConfig) error {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultBootTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := m.SDO(node)
	if err != nil {
		return err
	}
	if cfg.DeviceType != 0 {
		dt, err := c.ReadUint(ctx, od.IndexDeviceType, 0)
		if err != nil {
			return fmt.Errorf("master: node %d: device type: %w", node, err)
		}
		if uint32(dt) != cfg.DeviceType {
			return fmt.Errorf("%w: node %d device type 0x%08X, want 0x%08X", ErrIdentity, node, dt, cfg.DeviceType)
		}
	}
	if want := cfg.Identity; want != nil {
		got, err := readIdentity(ctx, c)
		if err != nil {
			return fmt.Errorf("master: node %d: %w", node, err)
		}
		if !matches(got, *want) {
			return fmt.Errorf("%w: node %d has %s, want %s", ErrIdentity, node, got, *want)
		}
	}
	if err := m.Configure(ctx, node, cfg.Entries); err != nil {
		return err
	}
	if cfg.HeartbeatTimeout > 0 {
		if err := m.Monitor(node, cfg.HeartbeatTimeout); err != nil {
			return err
		}
	}
	if !cfg.NoStart {
		if err := m.StartNode(ctx, node); err != nil {
			return fmt.Errorf("master: node %d: start: %w", node, err)
		}
	}
	return nil
}

// matches compares the non-zero fields of want.
func matches(got, want Identity) bool {
	eq := func(g, w uint32) bool { return w == 0 || g == w }
	return eq(got.VendorID, want.VendorID) && eq(got.ProductCode, want.ProductCode) &&
		eq(got.Revision, want.Revision) && eq(got.SerialNumber, want.SerialNumber)
}
