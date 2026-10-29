// SPDX-License-Identifier: MIT

package emcy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Event is an emergency message a [Consumer] received.
type Event struct {
	// Node is the producing node: the one given [WithProducer], or the
	// node-ID of a predefined identifier 0x081..0x0FF; 0 when unknown.
	Node  canopen.NodeID
	COBID canopen.COBID
	Message
	Time time.Time // the frame's receive timestamp, or the time it was processed
}

// ConsumerStats counts what a [Consumer] did.
type ConsumerStats struct {
	Received uint64 `json:"received"` // emergency messages delivered
	Resets   uint64 `json:"resets"`   // of which error resets
	Invalid  uint64 `json:"invalid"`  // malformed frames
}

// Consumer receives emergency messages: by default those of every node on
// the predefined identifiers 0x081..0x0FF, or those of the producers
// given [WithProducer].
type Consumer struct {
	bus canopen.Bus
	cfg config
	lc  lifecycle

	mu    sync.Mutex
	stats ConsumerStats
	last  map[canopen.NodeID]Event
}

// NewConsumer returns an emergency consumer. Options: [WithLogger],
// [WithHandler], [WithProducer].
func NewConsumer(bus canopen.Bus, opts ...Option) *Consumer {
	return &Consumer{bus: bus, cfg: newConfig(opts), last: map[canopen.NodeID]Event{}}
}

// Start subscribes to the emergency identifiers and starts delivering.
func (c *Consumer) Start() error {
	return c.lc.start(func(context.Context) (func(context.Context), error) {
		var fs canbus.FilterSet
		if len(c.cfg.producers) == 0 {
			fs.Filters = []canbus.Filter{{ID: canopen.FuncEMCY, Mask: 0x780, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly}}
		}
		for cob := range c.cfg.producers {
			flt := cob.Filter()
			flt.Remote = canbus.RemoteDataOnly
			fs.Filters = append(fs.Filters, flt)
		}
		sub, err := c.bus.Subscribe(canbus.WithFilters(fs))
		if err != nil {
			return nil, fmt.Errorf("emcy: subscribe: %w", err)
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				c.receive(&f)
			}
		}, nil
	})
}

// source returns the node and COB-ID of a frame, ok false when the frame
// is not an emergency message of interest.
func (c *Consumer) source(f *canbus.Frame) (canopen.NodeID, canopen.COBID, bool) {
	if len(c.cfg.producers) == 0 {
		if f.Flags&canbus.FlagExtended != 0 || f.ID&^0x7F != canopen.FuncEMCY || f.ID == canopen.FuncEMCY {
			return 0, 0, false
		}
		return canopen.NodeID(f.ID & 0x7F), canopen.COBID(f.ID), true
	}
	for cob, node := range c.cfg.producers {
		if cob.Matches(f) {
			return node, cob, true
		}
	}
	return 0, 0, false
}

func (c *Consumer) receive(f *canbus.Frame) {
	node, cob, ok := c.source(f)
	var (
		m   Message
		err error
	)
	if ok {
		m, err = ParseFrame(f)
	}
	c.mu.Lock()
	if !ok || err != nil {
		c.stats.Invalid++
		c.mu.Unlock()
		c.cfg.log.Debug("emcy invalid frame", "id", uint32(f.ID), "error", err)
		return
	}
	t := f.Timestamp
	if t.IsZero() {
		t = time.Now()
	}
	e := Event{Node: node, COBID: cob, Message: m, Time: t}
	c.stats.Received++
	if m.IsReset() {
		c.stats.Resets++
	}
	c.last[node] = e
	c.mu.Unlock()
	c.cfg.log.Info("emcy", "node", int(node), "message", m.String())
	if h := c.cfg.handler; h != nil {
		h(e)
	}
}

// Last returns the last emergency message received from a node.
func (c *Consumer) Last(node canopen.NodeID) (Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.last[node]
	return e, ok
}

// Close stops the consumer and waits for it.
func (c *Consumer) Close() error {
	c.lc.close()
	return nil
}

// Stats returns the counters.
func (c *Consumer) Stats() ConsumerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
