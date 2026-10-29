// SPDX-License-Identifier: MIT

// Command network-boot runs the CiA 302-2 network boot-up on a virtual CAN
// bus: a CANopen manager (master.WithDictionary) whose dictionary lists
// two slaves in 0x1F81 with their expected identity, a concise DCF each
// (0x1F22) and heartbeat supervision (0x1016). One slave is unconfigured:
// the LSS step of the boot-up (master.WithLSSBoot) gives it its node-ID
// by its identity. The manager checks, configures and starts both
// slaves, then enters Operational.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

const (
	managerID canopen.NodeID = 1
	vendor    uint32         = 0xF7A
	product   uint32         = 0x42
	idxParam  uint16         = 0x2000 // UNSIGNED32 parameter set by the concise DCF
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

// slave starts a device with serial number serial and node-ID id
// (lss.Unconfigured for none).
func slave(vb *virtual.Bus, id canopen.NodeID, serial uint32) (*node.Node, func(), error) {
	bus, err := open(vb)
	if err != nil {
		return nil, nil, err
	}
	d, err := od.Standard(od.Config{NodeID: id, DeviceType: 0x191, VendorID: vendor, ProductCode: product, Revision: 1, SerialNumber: serial})
	if err == nil {
		err = d.Add(od.NewVar(idxParam, "Parameter", canopen.Unsigned32, od.AccessRW, uint32(0)))
	}
	if err != nil {
		_ = bus.Close()
		return nil, nil, err
	}
	n, err := node.New(bus, d, node.WithLSS())
	if err == nil {
		err = n.Start()
	}
	if err != nil {
		_ = bus.Close()
		return nil, nil, err
	}
	return n, func() { _ = n.Close(); _ = bus.Close() }, nil
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{Name: "boot"})
	defer func() { _ = vb.Close() }()
	s5, stop5, err := slave(vb, 5, 501)
	if err != nil {
		return err
	}
	defer stop5()
	s6, stop6, err := slave(vb, lss.Unconfigured, 601)
	if err != nil {
		return err
	}
	defer stop6()

	// The manager's dictionary: NMT start-up, the slave assignments, the
	// expected identities, heartbeat consumption and the concise DCFs.
	d, err := od.Standard(od.Config{NodeID: managerID, HeartbeatConsumers: 2, HeartbeatMs: 100})
	if err != nil {
		return err
	}
	if err := master.AddManagerObjects(d); err != nil {
		return err
	}
	set := func(index uint16, sub uint8, x uint64) {
		if err == nil {
			err = d.SetUint(index, sub, x)
		}
	}
	set(master.IndexNMTStartup, 0, uint64(master.StartupMaster|master.StartupStartAll))
	for i, s := range []struct {
		id     canopen.NodeID
		serial uint32
	}{{5, 501}, {6, 601}} {
		set(master.IndexSlaveAssignment, uint8(s.id), uint64(master.NewAssignment(master.AssignSlave|master.AssignBoot|master.AssignMandatory, 0, 0)))
		set(master.IndexExpectedVendorID, uint8(s.id), uint64(vendor))
		set(master.IndexExpectedProductCode, uint8(s.id), uint64(product))
		set(master.IndexExpectedSerialNumber, uint8(s.id), uint64(s.serial))
		set(od.IndexConsumerHeartbeat, uint8(i+1), uint64(nmt.ConsumerEntry(s.id, 500*time.Millisecond)))
		dcf, e := cdcf.Encode([]cdcf.Entry{
			{Index: od.IndexProducerHeartbeat, Data: []byte{100, 0}},
			{Index: idxParam, Data: []byte{byte(s.id), 0, 0xAB, 0xCD}},
		})
		if err == nil {
			err = e
		}
		if err == nil {
			err = d.Set(cdcf.IndexConciseDCF, uint8(s.id), dcf)
		}
	}
	if err != nil {
		return err
	}

	mbus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = mbus.Close() }()
	done := make(chan error, 1)
	m, err := master.New(mbus, master.WithDictionary(d),
		master.WithLSSBoot(lss.FastscanOptions{Timeout: 5 * time.Millisecond, Known: map[lss.Part]uint32{
			lss.PartVendor: vendor, lss.PartProduct: product, lss.PartRevision: 1,
		}}),
		master.WithEventHandler(func(e master.Event) {
			switch e.Kind {
			case master.EventLSSAssigned, master.EventBootDone, master.EventBootFailed:
				fmt.Println(e)
			case master.EventNetworkBootDone:
				done <- nil
			case master.EventNetworkBootFailed:
				done <- e.Err
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
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, n := range []*node.Node{s5, s6} {
		if err := m.WaitState(ctx, n.NodeID(), canopen.StateOperational); err != nil {
			return err
		}
		v, _ := n.Dictionary().GetUint(idxParam, 0)
		fmt.Printf("slave %d: %s, parameter 0x%08X\n", n.NodeID(), n.State(), v)
	}
	fmt.Printf("manager: %s\n", m.Node().State())
	return nil
}
