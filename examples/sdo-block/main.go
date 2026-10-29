// SPDX-License-Identifier: MIT

// Command sdo-block transfers a 64 KiB DOMAIN to and from a CANopen node
// with SDO block transfer (CRC-checked) on a virtual CAN bus, and compares
// it with segmented transfer.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

const (
	nodeID    canopen.NodeID = 7
	idxImage  uint16         = 0x2F00 // DOMAIN: e.g. a firmware image or a log
	imageSize                = 64 << 10
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
	vb := virtual.New(virtual.Config{Name: "example"})
	defer func() { _ = vb.Close() }()
	nodeBus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = nodeBus.Close() }()
	clientBus, err := open(vb)
	if err != nil {
		return err
	}
	defer func() { _ = clientBus.Close() }()

	d, err := od.Standard(od.Config{NodeID: nodeID})
	if err != nil {
		return err
	}
	if err := d.Add(od.NewVar(idxImage, "Image", canopen.Domain, od.AccessRW, nil)); err != nil {
		return err
	}
	n, err := node.New(nodeBus, d)
	if err != nil {
		return err
	}
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()

	image := make([]byte, imageSize)
	rand.New(rand.NewSource(1)).Read(image)
	for _, mode := range []sdo.BlockMode{sdo.BlockAlways, sdo.BlockNever} {
		c, err := sdo.NewClient(clientBus, nodeID, sdo.WithBlockMode(mode), sdo.WithBlockSize(127))
		if err != nil {
			return err
		}
		start := time.Now()
		if err := c.Download(ctx, idxImage, 0, image); err != nil {
			return fmt.Errorf("download: %w", err)
		}
		down := time.Since(start)
		start = time.Now()
		back, err := c.Upload(ctx, idxImage, 0)
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		up := time.Since(start)
		if !bytes.Equal(back, image) {
			return fmt.Errorf("%s: uploaded data differs", mode)
		}
		st := c.Stats()
		fmt.Printf("%-6s %d bytes: download %v, upload %v, %d block transfers\n",
			mode, len(back), down.Round(time.Millisecond), up.Round(time.Millisecond), st.BlockTransfers)
		_ = c.Close()
	}
	fmt.Printf("server: %d block transfers\n", n.Stats().SDO.BlockTransfers)
	return nil
}
