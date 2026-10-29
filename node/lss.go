// SPDX-License-Identifier: MIT

package node

import (
	"context"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/nmt"
)

// LSS settings in the store of WithStore: entries under the pseudo index
// lssStoreIndex, outside every dictionary area (0x1000..0x9FFF), so that
// saving and restoring parameter groups keeps them and loading never
// applies them to the dictionary.
const (
	lssStoreIndex  uint16 = 0x0000
	lssStoreNodeID uint8  = 0 // UNSIGNED8 node-ID (1..127, 0xFF)
	lssStoreTiming uint8  = 1 // table selector, table index
)

// loadLSS reads the LSS settings of the store (WithLSS and WithStore) and
// applies a stored node-ID to the dictionary, restoring the defaults of
// the communication area for it.
func (n *Node) loadLSS() error {
	if !n.cfg.lssOn || n.cfg.store == nil {
		return nil
	}
	entries, err := n.cfg.store.Load()
	if err != nil {
		return fmt.Errorf("node: load LSS settings: %w", err)
	}
	var set lss.Settings
	found := false
	for _, e := range entries {
		if e.Index != lssStoreIndex {
			continue
		}
		switch {
		case e.SubIndex == lssStoreNodeID && len(e.Data) == 1:
			id := canopen.NodeID(e.Data[0])
			if id.Valid() || id == lss.Unconfigured {
				set.NodeID, found = id, true
			}
		case e.SubIndex == lssStoreTiming && len(e.Data) == 2:
			set.BitTimingSet, set.Selector, set.BitTiming = true, e.Data[0], lss.BitTiming(e.Data[1])
		}
	}
	if !found {
		return nil
	}
	n.lssSet = &set
	if set.NodeID != n.d.NodeID() {
		n.d.SetNodeID(set.NodeID)
		n.d.Reset(nmt.CommunicationAreaFirst, nmt.CommunicationAreaLast)
	}
	return nil
}

// buildLSS creates the LSS slave of WithLSS.
func (n *Node) buildLSS(id canopen.NodeID) error {
	if !n.cfg.lssOn {
		return nil
	}
	addr, err := lss.AddressFromDictionary(n.d)
	if err != nil {
		return fmt.Errorf("node: LSS: %w", err)
	}
	opts := append([]lss.SlaveOption{lss.WithSlaveLogger(n.log)}, n.cfg.lss...)
	opts = append(opts,
		lss.WithNodeIDHandler(n.slave.SetNodeID),
		lss.WithResetHandler(n.lssReset))
	if n.cfg.store != nil {
		opts = append(opts, lss.WithStoreHandler(n.storeLSS))
	}
	n.lssS, err = lss.NewSlave(n.bus, addr, id, opts...)
	if err != nil {
		return fmt.Errorf("node: LSS: %w", err)
	}
	return nil
}

// lssReset is the reset communication an unconfigured device does on its
// own once LSS configured its node-ID.
func (n *Node) lssReset(canopen.NodeID) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := n.slave.Reset(ctx, nmt.ResetCommunication); err != nil {
		n.log.Warn("node: LSS reset communication", "error", err)
	}
}

// storeLSS saves the pending LSS settings in the store.
func (n *Node) storeLSS(set lss.Settings) error {
	s := n.cfg.store
	n.storeMu.Lock()
	defer n.storeMu.Unlock()
	saved, err := s.Load()
	if err != nil {
		return err
	}
	out := make([]cdcf.Entry, 0, len(saved)+2)
	for _, e := range saved {
		if e.Index != lssStoreIndex {
			out = append(out, e)
		}
	}
	out = append(out, cdcf.Entry{Index: lssStoreIndex, SubIndex: lssStoreNodeID, Data: []byte{byte(set.NodeID)}})
	if set.BitTimingSet {
		out = append(out, cdcf.Entry{Index: lssStoreIndex, SubIndex: lssStoreTiming, Data: []byte{set.Selector, byte(set.BitTiming)}})
	}
	if err := s.Save(out); err != nil {
		return err
	}
	n.log.Info("node LSS settings stored", "nodeId", int(set.NodeID))
	return nil
}

// LSS returns the LSS slave of [WithLSS], or nil.
func (n *Node) LSS() *lss.Slave { return n.lssS }

// StoredLSS returns the LSS settings the store of [WithStore] held when
// the node was created ([WithLSS]): the node-ID, which the node applied,
// and the bit timing, which the application applies to its CAN
// controller. ok is false when nothing was stored.
func (n *Node) StoredLSS() (s lss.Settings, ok bool) {
	if n.lssSet == nil {
		return lss.Settings{}, false
	}
	return *n.lssSet, true
}
