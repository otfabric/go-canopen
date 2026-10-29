// SPDX-License-Identifier: MIT

// Command eds-dictionary builds a CANopen device from an EDS (a CiA 401
// I/O module), runs it on a virtual CAN bus, configures it from a DCF with
// the master, and exports its configuration as a DCF again.
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

var (
	//go:embed io401.eds
	edsFile []byte
	//go:embed node37.dcf
	dcfFile []byte
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The EDS describes the device; its DCF configures node 0x25 of a line.
	desc, err := eds.ParseBytes(edsFile)
	if err != nil {
		return err
	}
	diags := desc.Validate()
	fmt.Printf("EDS %s: %d objects, %d findings (errors: %v)\n", desc.FileInfo.FileName, len(desc.Objects), len(diags), eds.HasErrors(diags))
	dcf, err := eds.ParseBytes(dcfFile)
	if err != nil {
		return err
	}
	id := canopen.NodeID(dcf.DeviceComissioning.NodeID)
	d, err := desc.Dictionary(id)
	if err != nil {
		return err
	}

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
	m, err := master.New(masterBus)
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
	since := time.Now()
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()
	if err := m.WaitBootUp(ctx, id, since); err != nil {
		return err
	}
	found, err := m.Scan(ctx, []canopen.NodeID{id})
	if err != nil || len(found) != 1 {
		return fmt.Errorf("scan: %v %v", found, err)
	}
	fmt.Printf("node 0x%02X: device type 0x%08X, %s\n", id, found[0].DeviceType, found[0].Identity)

	// Configure from the DCF.
	entries, err := master.DCFEntries(dcf, id)
	if err != nil {
		return err
	}
	fmt.Printf("DCF %q: %d entries\n", dcf.DeviceComissioning.NodeName, len(entries))
	if err := m.ConfigureFromDCF(ctx, id, dcf); err != nil {
		return err
	}
	c, err := m.SDO(id)
	if err != nil {
		return err
	}
	hb, err := c.ReadUint(ctx, od.IndexProducerHeartbeat, 0)
	if err != nil {
		return err
	}
	fmt.Printf("producer heartbeat after configuration: %d ms\n", hb)
	if err := m.Monitor(id, time.Duration(hb)*time.Millisecond*3/2); err != nil {
		return err
	}
	if err := m.StartNode(ctx, id); err != nil {
		return err
	}
	if err := m.WaitState(ctx, id, canopen.StateOperational); err != nil {
		return err
	}
	fmt.Println("node state:", m.State(id))

	// Export the node's configuration as a DCF.
	out := eds.FromDictionary(n.Dictionary(), eds.ExportOptions{
		FileInfo: desc.FileInfo, DeviceInfo: desc.DeviceInfo, DCF: true,
		Comissioning: eds.DeviceComissioning{NodeName: "exported"},
	})
	var buf bytes.Buffer
	if err := eds.Write(&buf, out); err != nil {
		return err
	}
	fmt.Printf("exported DCF: %d bytes; [1017]:\n", buf.Len())
	sc := bufio.NewScanner(&buf)
	in := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "[") {
			in = line == "[1017]"
		}
		if in && line != "" {
			fmt.Println("  " + line)
		}
	}
	return nil
}
