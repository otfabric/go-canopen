// SPDX-License-Identifier: MIT

package emcy_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen/emcy"
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

// A node raises and clears an error; a monitoring tool prints the
// emergency messages of all nodes.
func Example() {
	vb := virtual.New(virtual.Config{})
	defer func() { _ = vb.Close() }()
	nodeBus, toolBus := port(vb), port(vb)
	defer func() { _ = nodeBus.Close() }()
	defer func() { _ = toolBus.Close() }()

	events := make(chan emcy.Event, 8)
	consumer := emcy.NewConsumer(toolBus, emcy.WithHandler(func(e emcy.Event) { events <- e }))
	_ = consumer.Start()
	defer func() { _ = consumer.Close() }()

	producer, _ := emcy.NewProducer(nodeBus, 5)
	ctx := context.Background()
	_ = producer.Raise(ctx, emcy.CodeTemperatureDevice, emcy.ClassRegister(emcy.CodeTemperatureDevice), [5]byte{0x42})
	_ = producer.Clear(ctx, emcy.CodeTemperatureDevice)

	for i := 0; i < 2; i++ {
		select {
		case e := <-events:
			fmt.Printf("node %d: 0x%04X %s, register %s\n", e.Node, e.Code, emcy.CodeText(e.Code), e.Register)
		case <-time.After(time.Second):
			log.Fatal("no emergency")
		}
	}
	fmt.Println("history:", len(producer.History()))
	// Output:
	// node 5: 0x4200 device temperature, register generic|temperature
	// node 5: 0x0000 error reset or no error, register none
	// history: 1
}
