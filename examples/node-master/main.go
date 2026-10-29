// SPDX-License-Identifier: MIT

// Command node-master runs a CANopen device and a network manager on a
// virtual CAN bus: the master waits for the boot-up, reads the device's
// identity by SDO, maps a TPDO and an RPDO, starts the node and exchanges
// process data with it.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

const (
	nodeID      canopen.NodeID = 5
	idxCounter  uint16         = 0x2100 // UNSIGNED32 input, sent by TPDO 1
	idxSetpoint uint16         = 0x2000 // UNSIGNED16 output, received by RPDO 1
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// open returns a new port of the virtual bus.
func open(vb *virtual.Bus) (*canbus.Bus, error) {
	p, err := vb.Open()
	if err != nil {
		return nil, err
	}
	return canbus.NewBus(p)
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vb := virtual.New(virtual.Config{Name: "example"})
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

	// The device: the communication profile and two application objects.
	d, err := od.Standard(od.Config{
		NodeID: nodeID, DeviceType: 0x000F0191, VendorID: 0xF7A, ProductCode: 0x42, Revision: 1, SerialNumber: 1234,
		DeviceName: "example I/O", HeartbeatMs: 100, RPDOs: 1, TPDOs: 1,
	})
	if err != nil {
		return err
	}
	counter := od.NewVar(idxCounter, "Counter", canopen.Unsigned32, od.AccessRWR, nil)
	setpoint := od.NewVar(idxSetpoint, "Setpoint", canopen.Unsigned16, od.AccessRWW, nil)
	counter.Subs[0].PDOMappable, setpoint.Subs[0].PDOMappable = true, true
	if err := d.Add(counter); err != nil {
		return err
	}
	if err := d.Add(setpoint); err != nil {
		return err
	}

	// The master, reporting what it sees.
	booted := make(chan struct{}, 1)
	m, err := master.New(masterBus, master.WithEventHandler(func(e master.Event) {
		fmt.Println("event:", e)
		if e.Kind == master.EventBootUp && e.Node == nodeID {
			select {
			case booted <- struct{}{}:
			default:
			}
		}
	}))
	if err != nil {
		return err
	}
	if err := m.Start(); err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	n, err := node.New(nodeBus, d)
	if err != nil {
		return err
	}
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()
	select {
	case <-booted:
	case <-ctx.Done():
		return fmt.Errorf("no boot-up: %w", ctx.Err())
	}

	// SDO: identity and device name.
	id, err := m.Identity(ctx, nodeID)
	if err != nil {
		return err
	}
	c, err := m.SDO(nodeID)
	if err != nil {
		return err
	}
	name, err := c.ReadString(ctx, od.IndexDeviceName, 0)
	if err != nil {
		return err
	}
	fmt.Printf("node %d: %q, %s\n", nodeID, name, id)

	// PDOs: the counter in TPDO 1 (event-driven), the setpoint in RPDO 1.
	tpdo := pdo.Config{COBID: 0x185, TransmissionType: pdo.TransEventProfile,
		Mapping: pdo.Mapping{{Index: idxCounter, Bits: 32}}}
	rpdo := pdo.Config{COBID: 0x205, TransmissionType: pdo.TransEventProfile,
		Mapping: pdo.Mapping{{Index: idxSetpoint, Bits: 16}}}
	if err := m.ConfigurePDO(ctx, nodeID, true, 1, tpdo); err != nil {
		return err
	}
	if err := m.ConfigurePDO(ctx, nodeID, false, 1, rpdo); err != nil {
		return err
	}
	got := make(chan uint64, 16)
	if _, err := m.ListenTPDO(ctx, nodeID, 1, d, func(_ *canbus.Frame, v []pdo.Value) { got <- v[0].Value.(uint64) }); err != nil {
		return err
	}
	snd, err := m.RPDOSender(ctx, nodeID, 1, d)
	if err != nil {
		return err
	}
	if err := m.Monitor(nodeID, 300*time.Millisecond); err != nil {
		return err
	}
	if err := m.StartNode(ctx, nodeID); err != nil {
		return err
	}
	if err := m.WaitState(ctx, nodeID, canopen.StateOperational); err != nil {
		return err
	}
	for i := uint32(1); i <= 3; i++ {
		if err := d.SetValue(idxCounter, 0, i*100); err != nil {
			return err
		}
		for {
			select {
			case v := <-got:
				fmt.Println("TPDO counter:", v)
				if v != uint64(i*100) {
					continue
				}
			case <-ctx.Done():
				return fmt.Errorf("no TPDO: %w", ctx.Err())
			}
			break
		}
	}
	if err := snd.Send(ctx, uint16(1500)); err != nil {
		return err
	}
	for {
		v, _ := d.GetUint(idxSetpoint, 0)
		if v == 1500 {
			fmt.Println("RPDO setpoint applied:", v)
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("RPDO not applied: %w", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	st := n.Stats()
	fmt.Printf("node stats: state %s, %d SDO uploads, %d TPDOs sent, %d RPDOs applied\n",
		st.State, st.SDO.Uploads, st.TPDO[1].Sent, st.RPDO[1].Applied)
	return nil
}
