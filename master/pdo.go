// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// Listen starts a listener for PDOs on a COB-ID with a known mapping,
// whose objects desc describes (a dictionary of the producing node, for
// example built from its EDS). The handler runs on the listener's
// goroutine. The listener is closed with the master, or earlier by the
// caller.
func (m *Master) Listen(cob canopen.COBID, mapping pdo.Mapping, desc *od.Dictionary, h func(f *canbus.Frame, values []pdo.Value)) (*pdo.Listener, error) {
	dec, err := pdo.NewDecoder(mapping, desc)
	if err != nil {
		return nil, err
	}
	l := pdo.NewListener(m.bus, cob, dec, h, pdo.WithLogger(m.log))
	if err := l.Start(); err != nil {
		return nil, err
	}
	if err := m.track(l); err != nil {
		return nil, err
	}
	return l, nil
}

// ListenTPDO reads the configuration of TPDO n of a node over SDO and
// starts a listener for it (see [Master.Listen]). It fails when the TPDO
// is not active (invalid COB-ID or empty mapping).
func (m *Master) ListenTPDO(ctx context.Context, node canopen.NodeID, n int, desc *od.Dictionary, h func(f *canbus.Frame, values []pdo.Value)) (*pdo.Listener, error) {
	cfg, err := m.ReadPDO(ctx, node, true, n)
	if err != nil {
		return nil, err
	}
	if !cfg.Active() {
		return nil, fmt.Errorf("master: node %d TPDO %d: %w", node, n, pdo.ErrInactive)
	}
	return m.Listen(cfg.COBID, cfg.Mapping, desc, h)
}

// Sender returns a sender of PDOs on a COB-ID with a known mapping, whose
// objects desc describes.
func (m *Master) Sender(cob canopen.COBID, mapping pdo.Mapping, desc *od.Dictionary) (*pdo.Sender, error) {
	enc, err := pdo.NewEncoder(mapping, desc)
	if err != nil {
		return nil, err
	}
	return pdo.NewSender(m.bus, cob, enc), nil
}

// RPDOSender reads the configuration of RPDO n of a node over SDO and
// returns a sender of that PDO (see [Master.Sender]). It fails when the
// RPDO is not active.
func (m *Master) RPDOSender(ctx context.Context, node canopen.NodeID, n int, desc *od.Dictionary) (*pdo.Sender, error) {
	cfg, err := m.ReadPDO(ctx, node, false, n)
	if err != nil {
		return nil, err
	}
	if !cfg.Active() {
		return nil, fmt.Errorf("master: node %d RPDO %d: %w", node, n, pdo.ErrInactive)
	}
	return m.Sender(cfg.COBID, cfg.Mapping, desc)
}
