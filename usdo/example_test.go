// SPDX-License-Identifier: MIT

package usdo_test

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/usdo"
)

// A client (node 1) reads and writes the dictionary of a server (node 5)
// on a CAN FD bus, then writes one object of every server at once.
func Example() {
	vb := virtual.New(virtual.Config{Name: "vcanfd"})
	defer vb.Close()
	open := func() *canbus.Bus {
		p, _ := vb.Open(virtual.WithFD())
		b, _ := canbus.NewBus(p)
		return b
	}
	serverBus, clientBus := open(), open()
	defer serverBus.Close()
	defer clientBus.Close()

	d, _ := od.Standard(od.Config{NodeID: 5, DeviceName: "drive"})
	srv, _ := usdo.NewServer(serverBus, d)
	_ = srv.Start()
	defer srv.Close()
	c, _ := usdo.NewClient(clientBus, 1, usdo.WithTimeout(100*time.Millisecond))
	_ = c.Start()
	defer c.Close()

	ctx := context.Background()
	name, err := c.Upload(ctx, 5, 0x1008, 0)
	fmt.Println(string(name), err)
	err = c.Download(ctx, 5, 0x1017, 0, []byte{0xE8, 0x03}, usdo.WithDataType(canopen.Unsigned16))
	hb, _ := d.GetUint(0x1017, 0)
	fmt.Println(hb, err)
	res, err := c.DownloadMulti(ctx, usdo.Broadcast, 0x1017, 0, []byte{0xF4, 0x01}, usdo.Expect(5))
	fmt.Println(len(res), res[5], err)
	// Output:
	// drive <nil>
	// 1000 <nil>
	// 1 <nil> <nil>
}
