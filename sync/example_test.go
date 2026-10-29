// SPDX-License-Identifier: MIT

package syncobj_test

import (
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	syncobj "github.com/otfabric/go-canopen/sync"
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

// A SYNC producer with a 10 ms cycle and a counter overflowing at 4, and a
// consumer printing the counters.
func Example() {
	vb := virtual.New(virtual.Config{})
	defer func() { _ = vb.Close() }()
	masterBus, nodeBus := port(vb), port(vb)
	defer func() { _ = masterBus.Close() }()
	defer func() { _ = nodeBus.Close() }()

	counters := make(chan uint8, 16)
	consumer, _ := syncobj.NewConsumer(nodeBus, syncobj.WithHandler(func(e syncobj.Event) { counters <- e.Counter }))
	_ = consumer.Start()
	defer func() { _ = consumer.Close() }()

	producer, _ := syncobj.NewProducer(masterBus, syncobj.WithPeriod(10*time.Millisecond), syncobj.WithOverflow(4))
	_ = producer.Start()
	defer func() { _ = producer.Close() }()

	for i := 0; i < 6; i++ {
		fmt.Print(<-counters, " ")
	}
	fmt.Println()
	// Output: 1 2 3 4 1 2
}
