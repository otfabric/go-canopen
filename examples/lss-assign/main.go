// SPDX-License-Identifier: MIT

// Command lss-assign starts three CANopen devices without node-ID (LSS,
// CiA 305) on a virtual CAN bus. The master lists them with LSS Fastscan,
// gives each a node-ID with AssignAll (stored in the devices' parameter
// stores), and the devices boot with their new node-IDs.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/store"
)

const (
	vendor   uint32 = 0xF7A
	product  uint32 = 0x42
	revision uint32 = 1
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func open(vb *virtual.Bus) (*canbus.Bus, error) {
	p, err := vb.Open()
	if err != nil {
		return nil, err
	}
	return canbus.NewBus(p)
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{Name: "lss"})
	defer func() { _ = vb.Close() }()

	// Three devices that differ only by serial number, unconfigured
	// (node-ID 0xFF): they stay silent in NMT Initialising.
	var nodes []*node.Node
	for _, serial := range []uint32{3001, 3002, 3003} {
		bus, err := open(vb)
		if err != nil {
			return err
		}
		defer func() { _ = bus.Close() }()
		d, err := od.Standard(od.Config{
			NodeID: lss.Unconfigured, DeviceType: 0x191, VendorID: vendor, ProductCode: product,
			Revision: revision, SerialNumber: serial, HeartbeatMs: 100,
		})
		if err != nil {
			return err
		}
		if err := store.AddObjects(d); err != nil {
			return err
		}
		n, err := node.New(bus, d, node.WithLSS(), node.WithStore(store.NewMemory()))
		if err != nil {
			return err
		}
		if err := n.Start(); err != nil {
			return err
		}
		defer func() { _ = n.Close() }()
		nodes = append(nodes, n)
	}

	mbus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = mbus.Close() }()
	booted := make(chan canopen.NodeID, 8)
	m, err := master.New(mbus,
		master.WithLSSOptions(lss.WithFastscanTimeout(5*time.Millisecond)),
		master.WithEventHandler(func(e master.Event) {
			if e.Kind == master.EventBootUp {
				booted <- e.Node
			}
		}))
	if err != nil {
		return err
	}
	if err := m.Start(); err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	// Fastscan only the serial number: vendor, product and revision are
	// known (a full scan costs about 260 listening windows per device).
	fs := lss.FastscanOptions{Known: map[lss.Part]uint32{
		lss.PartVendor: vendor, lss.PartProduct: product, lss.PartRevision: revision,
	}}
	found, err := m.LSS().Fastscan(ctx, fs)
	if err != nil {
		return err
	}
	fmt.Printf("Fastscan found %d unconfigured devices\n", len(found))
	for _, a := range found {
		fmt.Printf("  %s\n", a)
	}
	assigned, err := m.LSS().AssignAll(ctx, 10, lss.AssignFastscan(fs), lss.AssignStore())
	if err != nil {
		return err
	}
	for _, a := range assigned {
		fmt.Printf("assigned node-ID %d to %s\n", a.NodeID, a.Address)
	}
	for range assigned {
		select {
		case id := <-booted:
			fmt.Printf("boot-up of node %d\n", id)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, n := range nodes {
		set := n.LSS().Pending()
		fmt.Printf("device %08X: node-ID %d, state %s, pending %d\n", n.LSS().Address().Serial, n.NodeID(), n.State(), set.NodeID)
	}
	if len(assigned) != len(nodes) {
		return fmt.Errorf("%d of %d devices configured", len(assigned), len(nodes))
	}
	return nil
}
