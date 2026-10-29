// SPDX-License-Identifier: MIT

package lss_test

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/lss"
)

// Two unconfigured devices get node-IDs from 10 upwards.
func ExampleMaster_AssignAll() {
	vb := virtual.New(virtual.Config{Name: "lss-example"})
	defer vb.Close()
	open := func() *canbus.Bus {
		p, _ := vb.Open()
		b, _ := canbus.NewBus(p)
		return b
	}
	for _, serial := range []uint32{0x2000, 0x1000} {
		bus := open()
		defer bus.Close()
		addr := lss.Address{Vendor: 0x29C, Product: 1, Revision: 1, Serial: serial}
		s, _ := lss.NewSlave(bus, addr, lss.Unconfigured,
			lss.WithResetHandler(func(id canopen.NodeID) {
				// The device enters NMT reset communication with node-ID id.
			}))
		_ = s.Start()
		defer s.Close()
	}

	bus := open()
	defer bus.Close()
	m := lss.NewMaster(bus, lss.WithFastscanTimeout(5*time.Millisecond))
	_ = m.Start()
	defer m.Close()
	as, err := m.AssignAll(context.Background(), 10)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range as {
		fmt.Printf("%v -> %d\n", a.Address, a.NodeID)
	}
	// Output:
	// 0000029C:00000001:00000001:00001000 -> 10
	// 0000029C:00000001:00000001:00002000 -> 11
}
