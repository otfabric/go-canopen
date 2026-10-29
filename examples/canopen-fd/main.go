// SPDX-License-Identifier: MIT

// Command canopen-fd runs a CANopen FD network (CiA 1301) on a virtual CAN
// FD bus: a master and a device in FD mode. The master reads and writes
// the device's dictionary with its USDO client (the device serves the
// USDO in place of the SDO), maps a 64-byte TPDO, starts the device,
// receives the PDO and a 20-byte CANopen FD emergency message.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

const (
	nodeID     canopen.NodeID = 5
	idxSamples uint16         = 0x2100 // ARRAY of 16 INTEGER32 inputs
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// open returns a CAN FD port of the virtual bus.
func open(vb *virtual.Bus) (*canbus.Bus, error) {
	p, err := vb.Open(virtual.WithFD())
	if err != nil {
		return nil, err
	}
	return canbus.NewBus(p)
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{Name: "fd"})
	defer func() { _ = vb.Close() }()
	nodeBus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = nodeBus.Close() }()
	masterBus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = masterBus.Close() }()

	d, err := od.Standard(od.Config{
		NodeID: nodeID, DeviceType: 0x191, VendorID: 0xF7A, ProductCode: 0xFD, Revision: 1, SerialNumber: 7,
		DeviceName: "FD sensor", HeartbeatMs: 100, TPDOs: 1, MappingEntries: 16,
	})
	if err != nil {
		return err
	}
	samples := od.NewArray(idxSamples, "Samples", canopen.Integer32, od.AccessRWR, 16, nil, od.AccessConst)
	for _, v := range samples.Subs[1:] {
		v.PDOMappable = true
	}
	if err := d.Add(samples); err != nil {
		return err
	}
	n, err := node.New(nodeBus, d, node.WithFD(true))
	if err != nil {
		return err
	}

	got := make(chan []pdo.Value, 4)
	emergencies := make(chan *emcy.Event, 4)
	booted := make(chan struct{}, 1)
	m, err := master.New(masterBus, master.WithFD(true), master.WithEventHandler(func(e master.Event) {
		switch e.Kind {
		case master.EventBootUp:
			booted <- struct{}{}
		case master.EventEMCY:
			emergencies <- e.EMCY
		default:
		}
	}))
	if err != nil {
		return err
	}
	if err := m.Start(); err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()
	select {
	case <-booted:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Identity and name over USDO.
	id, err := m.Identity(ctx, nodeID)
	if err != nil {
		return err
	}
	uc, err := m.USDO()
	if err != nil {
		return err
	}
	name, err := uc.Upload(ctx, nodeID, 0x1008, 0)
	if err != nil {
		return err
	}
	fmt.Printf("node %d: %q, %s (USDO)\n", nodeID, name, id)

	// TPDO 1: the 16 samples, 64 bytes in one CAN FD frame.
	mapping := make(pdo.Mapping, 16)
	for i := range mapping {
		mapping[i] = pdo.Entry{Index: idxSamples, SubIndex: uint8(i + 1), Bits: 32}
	}
	cfg := pdo.Config{COBID: canopen.COBID(0x180 + uint32(nodeID)), TransmissionType: pdo.TransEventManufacturer, Mapping: mapping}
	if err := m.ConfigurePDO(ctx, nodeID, true, 1, cfg); err != nil {
		return err
	}
	if _, err := m.Listen(cfg.COBID, mapping, d, func(f *canbus.Frame, v []pdo.Value) {
		if f.IsFD() {
			got <- v
		}
	}); err != nil {
		return err
	}
	if err := m.StartNode(ctx, nodeID); err != nil {
		return err
	}
	if err := m.WaitState(ctx, nodeID, canopen.StateOperational); err != nil {
		return err
	}
	for i := 1; i <= 16; i++ {
		if err := d.SetValue(idxSamples, uint8(i), int32(i*1000)); err != nil {
			return err
		}
	}
	// The node enables its PDOs right after it reports Operational.
	for {
		err := n.TPDO(1).Transmit(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, pdo.ErrDisabled) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	for {
		select {
		case v := <-got:
			if v[15].Value != int64(16000) {
				continue // the PDO sent when the node became operational
			}
			fmt.Printf("64-byte PDO: %v ... %v\n", v[0], v[15])
		case <-ctx.Done():
			return ctx.Err()
		}
		break
	}

	// A CANopen FD emergency (20 bytes, with the CiA specification).
	if err := n.EMCY().Raise(ctx, 0x5000, emcy.RegGeneric, [5]byte{1, 2}); err != nil {
		return err
	}
	select {
	case e := <-emergencies:
		if e.FD == nil {
			return fmt.Errorf("classical emergency: %s", e.Message)
		}
		fmt.Printf("FD emergency of node %d: %s\n", e.Node, e.FD)
	case <-ctx.Done():
		return ctx.Err()
	}
	st := n.Stats()
	fmt.Printf("node USDO: %d uploads, %d downloads; TPDO 1 sent %d\n", st.USDO.Uploads, st.USDO.Downloads, st.TPDO[1].Sent)
	return nil
}
