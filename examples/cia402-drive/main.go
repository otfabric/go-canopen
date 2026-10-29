// SPDX-License-Identifier: MIT

// Command cia402-drive runs a simulated CiA 402 drive as a CANopen node on
// a virtual CAN bus and operates it from a master over SDO: enable
// (walking the state machine), profile position moves, a fault with its
// emergency message, fault reset, and disable.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/od"
)

const nodeID canopen.NodeID = 3

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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	// The drive: CiA 301 objects plus the CiA 402 objects WithDrive adds,
	// simulated every millisecond.
	d, err := od.Standard(od.Config{NodeID: nodeID, DeviceType: 0x00020192, ErrorHistory: 4})
	if err != nil {
		return err
	}
	n, err := node.New(nodeBus, d, node.WithDrive(cia402.DriveConfig{
		OnTransition: func(from, to cia402.State, tr int) { fmt.Printf("  drive: %s -> %s (transition %d)\n", from, to, tr) },
	}, time.Millisecond))
	if err != nil {
		return err
	}

	m, err := master.New(masterBus, master.WithEventHandler(func(e master.Event) {
		if e.Kind == master.EventEMCY {
			fmt.Println("event:", e)
		}
	}))
	if err != nil {
		return err
	}
	if err := m.Start(); err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	since := time.Now()
	if err := n.Start(); err != nil {
		return err
	}
	defer func() { _ = n.Close() }()
	if err := m.WaitBootUp(ctx, nodeID, since); err != nil {
		return err
	}

	ctl := m.Drive(nodeID, &cia402.ControllerOptions{PollInterval: 2 * time.Millisecond, ResetFault: true})
	fmt.Println("enable")
	if err := ctl.Enable(ctx); err != nil {
		return err
	}
	for _, target := range []int32{10000, -2500, 0} {
		start := time.Now()
		err := ctl.MoveTo(ctx, target, &cia402.MoveOptions{Wait: true, Velocity: 50000, Acceleration: 200000, Deceleration: 200000})
		if err != nil {
			return err
		}
		pos, err := ctl.Position(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("moved to %d in %v (position %d)\n", target, time.Since(start).Round(time.Millisecond), pos)
	}

	fmt.Println("inject fault 0x2310 (over-current)")
	n.Drive().InjectFault(0x2310)
	time.Sleep(50 * time.Millisecond)
	if code, err := ctl.FaultCode(ctx); err == nil {
		fmt.Printf("0x603F error code: 0x%04X\n", code)
	}
	fmt.Println("fault reset and enable")
	if err := ctl.Enable(ctx); err != nil {
		return err
	}
	if err := ctl.Disable(ctx); err != nil {
		return err
	}
	st, err := ctl.State(ctx)
	if err != nil {
		return err
	}
	fmt.Println("final state:", st)
	return nil
}
