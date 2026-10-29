// SPDX-License-Identifier: MIT

package nmt_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/nmt"
)

func port(vb *virtual.Bus) *canbus.Bus {
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

// A node (NMT slave with heartbeat) and an NMT master that waits for its
// boot-up, starts it and waits until it reports Operational.
func Example() {
	vb := virtual.New(virtual.Config{})
	defer func() { _ = vb.Close() }()
	nodeBus, masterBus := port(vb), port(vb)
	defer func() { _ = nodeBus.Close() }()
	defer func() { _ = masterBus.Close() }()

	// The master side: heartbeat consumer and NMT master.
	hb, _ := nmt.NewHeartbeatConsumer(masterBus, nmt.WithHeartbeatHandler(func(e nmt.HeartbeatEvent) {
		fmt.Println(e.Kind, "node", e.Node, e.State)
	}))
	_ = hb.Monitor(10, 100*time.Millisecond)
	_ = hb.Start()
	defer func() { _ = hb.Close() }()
	master := nmt.NewMaster(masterBus)

	// The node: NMT slave and heartbeat producer reporting its state.
	since := time.Now()
	slave, _ := nmt.NewSlave(nodeBus, 10)
	producer, _ := nmt.NewHeartbeatProducer(nodeBus, 10, slave.State, nmt.WithHeartbeatPeriod(20*time.Millisecond))
	_ = slave.Start()
	defer func() { _ = slave.Close() }()
	_ = producer.Start()
	defer func() { _ = producer.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := hb.WaitBootUp(ctx, 10, since); err != nil {
		log.Fatal(err)
	}
	if err := hb.WaitState(ctx, 10, canopen.StatePreOperational); err != nil {
		log.Fatal(err)
	}
	_ = master.Send(ctx, 10, canopen.NMTStart)
	if err := hb.WaitState(ctx, 10, canopen.StateOperational); err != nil {
		log.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // let the handler print
	// Output:
	// boot-up node 10 initialising
	// state-change node 10 pre-operational
	// state-change node 10 operational
}
