// SPDX-License-Identifier: MIT

package node

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/store"
)

// ErrNoStore reports a store or restore request on a node without
// [WithStore].
var ErrNoStore = errors.New("node: no parameter store")

// loadStored applies the saved values of the areas a reset (or the
// creation of the node, kind 0) restores: everything for a reset node or
// the creation, the communication area for a reset communication.
func (n *Node) loadStored(kind nmt.ResetKind) error {
	s := n.cfg.store
	if s == nil {
		return nil
	}
	entries, err := s.Load()
	if err != nil {
		return fmt.Errorf("node: load parameters: %w", err)
	}
	first, last := nmt.CommunicationAreaFirst, nmt.ApplicationAreaLast
	if kind == nmt.ResetCommunication {
		last = nmt.CommunicationAreaLast
	}
	if err := store.Apply(n.d, entries, first, last); err != nil {
		// A value the dictionary no longer accepts (another firmware):
		// the others are applied, the default stays for this one.
		n.log.Warn("node: stored parameters not applied", "error", err)
	}
	return nil
}

// installStore registers the hooks of 0x1010 and 0x1011 on the
// dictionary (with a store, when the objects exist).
func (n *Node) installStore() {
	if n.cfg.store == nil {
		return
	}
	one := func(*od.Variable, []byte) ([]byte, error) {
		// Bit 0: the device saves (restores) on command.
		return []byte{1, 0, 0, 0}, nil
	}
	if n.has(store.IndexStore) {
		n.unhook = append(n.unhook,
			n.d.OnWrite(store.IndexStore, -1, n.storeHook(store.SignatureSave, n.SaveParameters)),
			n.d.OnRead(store.IndexStore, -1, subsOnly(one)))
	}
	if n.has(store.IndexRestore) {
		n.unhook = append(n.unhook,
			n.d.OnWrite(store.IndexRestore, -1, n.storeHook(store.SignatureLoad, n.RestoreDefaults)),
			n.d.OnRead(store.IndexRestore, -1, subsOnly(one)))
	}
}

// subsOnly applies a read hook to sub-indices 1.. only.
func subsOnly(h od.ReadHook) od.ReadHook {
	return func(v *od.Variable, stored []byte) ([]byte, error) {
		if v.SubIndex == 0 {
			return stored, nil
		}
		return h(v, stored)
	}
}

// storeHook refuses a write without the signature (CiA 301: abort "data
// cannot be transferred or stored") and runs the request otherwise.
func (n *Node) storeHook(sig uint32, do func(uint8) error) od.WriteHook {
	return func(v *od.Variable, data []byte) error {
		if v.SubIndex == 0 {
			return nil
		}
		if len(data) != 4 || binary.LittleEndian.Uint32(data) != sig {
			return canopen.AbortTransfer
		}
		if err := do(v.SubIndex); err != nil {
			n.log.Warn("node: parameter store request failed", "sub", v.SubIndex, "error", err)
			var ac canopen.AbortCode
			if errors.As(err, &ac) {
				return ac
			}
			return canopen.AbortHardware
		}
		return nil
	}
}

// SaveParameters saves the current values of a group of parameters (the
// sub-index of 0x1010: [store.All], [store.Communication],
// [store.Application], [store.Manufacturer]) to the store of
// [WithStore], as a write of "save" to 0x1010 does. The parameters are
// the sub-objects with access rw ([store.Parameter]). Values saved
// earlier for other groups are kept.
func (n *Node) SaveParameters(group uint8) error {
	s := n.cfg.store
	if s == nil {
		return ErrNoStore
	}
	first, last, ok := store.Range(group)
	if !ok {
		return fmt.Errorf("node: store group %d: %w", group, canopen.AbortTransfer)
	}
	n.storeMu.Lock()
	defer n.storeMu.Unlock()
	saved, err := s.Load()
	if err != nil {
		return fmt.Errorf("node: save parameters: %w", err)
	}
	if err := s.Save(store.Merge(saved, store.Snapshot(n.d, first, last), first, last)); err != nil {
		return fmt.Errorf("node: save parameters: %w", err)
	}
	n.log.Info("node parameters saved", "group", group)
	return nil
}

// RestoreDefaults deletes the saved values of a group of parameters (the
// sub-index of 0x1011), as a write of "load" to 0x1011 does: the default
// values apply from the next reset (node or communication, as the group
// requires) on. The current values do not change.
func (n *Node) RestoreDefaults(group uint8) error {
	s := n.cfg.store
	if s == nil {
		return ErrNoStore
	}
	first, last, ok := store.Range(group)
	if !ok {
		return fmt.Errorf("node: restore group %d: %w", group, canopen.AbortTransfer)
	}
	n.storeMu.Lock()
	defer n.storeMu.Unlock()
	saved, err := s.Load()
	if err != nil {
		return fmt.Errorf("node: restore defaults: %w", err)
	}
	if err := s.Save(store.Without(saved, first, last)); err != nil {
		return fmt.Errorf("node: restore defaults: %w", err)
	}
	n.log.Info("node defaults restored", "group", group)
	return nil
}
