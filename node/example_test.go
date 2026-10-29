// SPDX-License-Identifier: MIT

package node_test

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
)

// The quick start of the README: a CANopen device and a network manager
// on two ports of a virtual CAN bus. Replace the virtual ports with
// socketcan.Open("can0") (Linux) or an slcan adapter for a real network.
func Example() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vb := virtual.New(virtual.Config{Name: "quickstart"})
	defer vb.Close()
	port := func() *canbus.Bus {
		p, err := vb.Open()
		if err != nil {
			log.Fatal(err)
		}
		b, err := canbus.NewBus(p)
		if err != nil {
			log.Fatal(err)
		}
		return b
	}
	deviceBus, masterBus := port(), port()
	defer deviceBus.Close()
	defer masterBus.Close()

	// The device: the CiA 301 communication objects and a node-ID.
	d, err := od.Standard(od.Config{NodeID: 5, DeviceType: 0x000F0191, VendorID: 0xF7A, DeviceName: "quickstart", HeartbeatMs: 100})
	if err != nil {
		log.Fatal(err)
	}
	dev, err := node.New(deviceBus, d)
	if err != nil {
		log.Fatal(err)
	}

	// The network manager.
	m, err := master.New(masterBus)
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Start(); err != nil {
		log.Fatal(err)
	}
	defer m.Close()

	since := time.Now()
	if err := dev.Start(); err != nil { // boot-up message, Pre-operational
		log.Fatal(err)
	}
	defer dev.Close()
	if err := m.WaitBootUp(ctx, 5, since); err != nil {
		log.Fatal(err)
	}

	// SDO: read the device type and the device name.
	c, err := m.SDO(5)
	if err != nil {
		log.Fatal(err)
	}
	devType, err := c.ReadUint(ctx, od.IndexDeviceType, 0)
	if err != nil {
		log.Fatal(err)
	}
	name, err := c.ReadString(ctx, od.IndexDeviceName, 0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("node 5: device type 0x%08X, %q\n", devType, name)

	// NMT: start the node and wait for its heartbeat to report it.
	if err := m.StartNode(ctx, 5); err != nil {
		log.Fatal(err)
	}
	if err := m.WaitState(ctx, 5, canopen.StateOperational); err != nil {
		log.Fatal(err)
	}
	fmt.Println("node 5:", m.State(5))
	// Output:
	// node 5: device type 0x000F0191, "quickstart"
	// node 5: operational
}
