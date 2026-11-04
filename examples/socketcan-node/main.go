// SPDX-License-Identifier: MIT
// example:hardware

// Command socketcan-node runs a CANopen device on a Linux SocketCAN
// interface (a real adapter, or vcan for experiments) until interrupted:
// boot-up, heartbeat, SDO server, EMCY and four TPDOs/RPDOs, with a
// counter mapped into TPDO 1 by default (send NMT start to see it).
//
//	sudo ip link add vcan0 type vcan && sudo ip link set vcan0 up
//	go run ./examples/socketcan-node -iface vcan0 -node 5
//	cansend vcan0 000#0105   # NMT start node 5
//	candump vcan0
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

const idxCounter uint16 = 0x2100

func main() {
	iface := flag.String("iface", "can0", "SocketCAN interface")
	id := flag.Uint("node", 5, "node-ID (1..127)")
	hb := flag.Uint("heartbeat", 1000, "producer heartbeat time in ms")
	flag.Parse()
	n, period, err := parseFlags(*id, *hb)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(*iface, n, period); err != nil {
		log.Fatal(err)
	}
}

// parseFlags checks the node-ID and heartbeat flags, which would otherwise
// wrap silently (-node 261 is node 5).
func parseFlags(id, hb uint) (canopen.NodeID, uint16, error) {
	if id < 1 {
		return 0, 0, fmt.Errorf("node-ID %d outside 1..127", id)
	}
	if id > 127 {
		return 0, 0, fmt.Errorf("node-ID %d outside 1..127", id)
	}
	if hb > 0xFFFF {
		return 0, 0, fmt.Errorf("heartbeat %d ms outside 0..65535", hb)
	}
	return canopen.NodeID(id), uint16(hb), nil
}

func run(iface string, id canopen.NodeID, hb uint16) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	conn, err := socketcan.Open(iface)
	if err != nil {
		return err
	}
	bus, err := canbus.NewBus(conn)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = bus.Close() }()

	d, err := od.Standard(od.Config{
		NodeID: id, DeviceType: 0x000F0191, DeviceName: "go-canopen socketcan-node",
		HeartbeatMs: hb, ErrorHistory: 8, RPDOs: 4, TPDOs: 4, HeartbeatConsumers: 4,
	})
	if err != nil {
		return err
	}
	counter := od.NewVar(idxCounter, "Counter", canopen.Unsigned32, od.AccessRWR, nil)
	counter.Subs[0].PDOMappable = true
	if err := d.Add(counter); err != nil {
		return err
	}
	// TPDO 1 sends the counter on change (local writes bypass the PDO
	// reconfiguration rules, as a device's defaults do).
	entry := pdo.Entry{Index: idxCounter, Bits: 32}
	if err := d.SetUint(pdo.MappingIndex(true, 1), 1, uint64(entry.Uint32())); err != nil {
		return err
	}
	if err := d.SetUint(pdo.MappingIndex(true, 1), 0, 1); err != nil {
		return err
	}
	lg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	n, err := node.New(bus, d, node.WithLogger(lg), node.WithStateHandler(func(sc nmt.StateChange) {
		fmt.Printf("NMT %s -> %s\n", sc.From, sc.To)
	}))
	if err != nil {
		return err
	}
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()
	fmt.Printf("node %d on %s; Ctrl-C to stop\n", id, iface)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for i := uint32(1); ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			_ = d.SetValue(idxCounter, 0, i)
		}
	}
}
