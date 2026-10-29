// SPDX-License-Identifier: MIT

package timestamp_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen/timestamp"
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

// A TIME producer distributes the time; a consumer receives it.
func Example() {
	vb := virtual.New(virtual.Config{})
	defer func() { _ = vb.Close() }()
	masterBus, nodeBus := port(vb), port(vb)
	defer func() { _ = masterBus.Close() }()
	defer func() { _ = nodeBus.Close() }()

	times := make(chan timestamp.Event, 1)
	consumer, _ := timestamp.NewConsumer(nodeBus, timestamp.WithHandler(func(e timestamp.Event) { times <- e }))
	_ = consumer.Start()
	defer func() { _ = consumer.Close() }()

	producer, _ := timestamp.NewProducer(masterBus)
	t := time.Date(2026, 10, 29, 8, 30, 0, 250_000_000, time.UTC)
	if err := producer.Send(context.Background(), t); err != nil {
		log.Fatal(err)
	}
	e := <-times
	fmt.Println(e.Time.Format(time.RFC3339Nano))
	fmt.Printf("days %d, ms %d, bytes % X\n", e.Days, e.Ms, e.Bytes())
	// Output:
	// 2026-10-29T08:30:00.25Z
	// days 15642, ms 30600250, bytes 3A EC D2 01 1A 3D
}
