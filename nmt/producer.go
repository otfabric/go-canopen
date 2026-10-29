// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// HeartbeatProducerStats counts what a [HeartbeatProducer] did.
type HeartbeatProducerStats struct {
	Sent    uint64 `json:"sent"`    // heartbeats sent
	Skipped uint64 `json:"skipped"` // periods without a heartbeat (state Initialising or unknown)
	Errors  uint64 `json:"errors"`  // send failures
}

// HeartbeatProducer sends a node's heartbeat (CiA 301 7.2.8.3.2.2): its
// NMT state on 0x700 + node-ID every period. A period of 0 produces
// nothing. A new period takes effect at once: the next heartbeat follows
// the change by the new period.
//
// The state comes from a function, usually [Slave.State]; no heartbeat is
// sent while it returns Initialising (the slave is resetting and will
// send a boot-up message) or Unknown.
//
// Periods are kept by a time.Ticker: heartbeats do not drift, but each one
// may be late by the scheduling latency of the Go runtime (typically well
// below a millisecond, more on a loaded system).
type HeartbeatProducer struct {
	bus   canopen.Bus
	cfg   config
	lc    lifecycle
	state func() canopen.NMTState
	wake  chan struct{}

	mu     sync.Mutex
	node   canopen.NodeID
	period time.Duration
	stats  HeartbeatProducerStats
}

// NewHeartbeatProducer returns the heartbeat producer of a node. Options:
// [WithLogger], [WithHeartbeatPeriod], [WithDictionary] (0x1017, which
// must exist).
func NewHeartbeatProducer(bus canopen.Bus, node canopen.NodeID, state func() canopen.NMTState, opts ...Option) (*HeartbeatProducer, error) {
	if err := node.Check(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("nmt: heartbeat producer needs a state source")
	}
	p := &HeartbeatProducer{bus: bus, cfg: newConfig(opts), state: state, node: node, wake: make(chan struct{}, 1)}
	if d := p.cfg.dict; d != nil {
		ms, err := d.GetUint(od.IndexProducerHeartbeat, 0)
		if err != nil {
			return nil, fmt.Errorf("nmt: producer heartbeat time 0x1017: %w", err)
		}
		p.period = time.Duration(ms) * time.Millisecond
	}
	if p.cfg.periodSet {
		p.period = p.cfg.period
	}
	if p.period < 0 {
		return nil, fmt.Errorf("nmt: negative heartbeat period")
	}
	return p, nil
}

// Start starts producing; with a dictionary it follows writes of 0x1017
// from then on.
func (p *HeartbeatProducer) Start() error {
	return p.lc.start(func(context.Context) (func(context.Context), error) {
		var unwatch func()
		if d := p.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				if v.Index == od.IndexProducerHeartbeat && v.SubIndex == 0 {
					p.SetPeriod(time.Duration(canopen.DecodeUint(data)) * time.Millisecond)
				}
			})
		}
		return func(ctx context.Context) {
			if unwatch != nil {
				defer unwatch()
			}
			p.run(ctx)
		}, nil
	})
}

func (p *HeartbeatProducer) run(ctx context.Context) {
	var (
		ticker *time.Ticker
		cur    time.Duration
	)
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	for {
		period := p.Period()
		if period != cur {
			cur = period
			switch {
			case period == 0 && ticker != nil:
				ticker.Stop()
			case period > 0 && ticker == nil:
				ticker = time.NewTicker(period)
			case period > 0:
				ticker.Reset(period)
			}
		}
		var tick <-chan time.Time
		if cur > 0 {
			tick = ticker.C
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-tick:
			p.send(ctx)
		}
	}
}

func (p *HeartbeatProducer) send(ctx context.Context) {
	st := p.state()
	p.mu.Lock()
	node := p.node
	if st == canopen.StateInitialising || st == canopen.StateUnknown {
		p.stats.Skipped++
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	f := ErrorControlFrame(node, st, false)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := p.bus.Send(sctx, &f)
	cancel()
	p.mu.Lock()
	if err != nil {
		p.stats.Errors++
	} else {
		p.stats.Sent++
	}
	p.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		p.cfg.log.Warn("heartbeat send", "node", int(node), "error", err)
	}
}

// SetPeriod changes the period; 0 stops producing. It takes effect at
// once. Negative periods count as 0.
func (p *HeartbeatProducer) SetPeriod(d time.Duration) {
	if d < 0 {
		d = 0
	}
	p.mu.Lock()
	p.period = d
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Period returns the current period.
func (p *HeartbeatProducer) Period() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.period
}

// SetNodeID changes the node-ID the heartbeat is sent for.
func (p *HeartbeatProducer) SetNodeID(node canopen.NodeID) error {
	if err := node.Check(); err != nil {
		return err
	}
	p.mu.Lock()
	p.node = node
	p.mu.Unlock()
	return nil
}

// Close stops producing and waits for the goroutine.
func (p *HeartbeatProducer) Close() error {
	p.lc.close()
	return nil
}

// Stats returns the counters.
func (p *HeartbeatProducer) Stats() HeartbeatProducerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
