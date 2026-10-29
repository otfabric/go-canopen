// SPDX-License-Identifier: MIT

package sdo_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

// A device serves its object dictionary; a master reads and writes it.
func Example() {
	vcan := virtual.New(virtual.Config{Name: "vcan0"})
	defer vcan.Close()
	open := func() *canbus.Bus {
		p, _ := vcan.Open()
		b, _ := canbus.NewBus(p)
		return b
	}
	deviceBus, masterBus := open(), open()
	defer deviceBus.Close()
	defer masterBus.Close()

	// The device: node 5.
	d, _ := od.Standard(od.Config{NodeID: 5, DeviceType: 0x00020192, DeviceName: "demo drive"})
	srv, _ := sdo.NewServer(deviceBus, d)
	if err := srv.Start(); err != nil {
		panic(err)
	}
	defer srv.Close()

	// The master.
	c, _ := sdo.NewClient(masterBus, 5, sdo.WithBlockMode(sdo.BlockAuto))
	defer c.Close()
	ctx := context.Background()

	devType, _ := c.ReadUint(ctx, 0x1000, 0)
	name, _ := c.ReadString(ctx, 0x1008, 0)
	fmt.Printf("device type 0x%08X, name %q\n", devType, name)

	if err := c.WriteUint16(ctx, 0x1017, 0, 1000); err != nil {
		panic(err)
	}
	hb, _ := c.ReadUint(ctx, 0x1017, 0)
	fmt.Println("heartbeat", hb, "ms")

	_, err := c.Upload(ctx, 0x6000, 0)
	var ae *canopen.AbortError
	if errors.As(err, &ae) && ae.Remote {
		fmt.Printf("0x%04X: abort 0x%08X (%s)\n", ae.Index, uint32(ae.Code), ae.Code.Text())
	}
	// Output:
	// device type 0x00020192, name "demo drive"
	// heartbeat 1000 ms
	// 0x6000: abort 0x06020000 (object does not exist in the object dictionary)
}
