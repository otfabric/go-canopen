// SPDX-License-Identifier: MIT

package syncobj

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Event is a SYNC message a [Consumer] received.
type Event struct {
	// Counter is the SYNC counter (1..overflow), or 0 for a SYNC without
	// data.
	Counter uint8
	// Time is the frame's receive timestamp, or the time it was processed.
	Time time.Time
	// WindowEnd is the end of the synchronous window (Time + window
	// length), after which synchronous PDOs must not be sent any more;
	// zero when no window is configured.
	WindowEnd time.Time
	// LengthError reports a SYNC whose length does not match the
	// configured counter overflow value (0 bytes for 0, 1 byte otherwise);
	// CiA 301 has the node signal it with EMCY 0x8240. It is only checked
	// when the overflow value is known.
	LengthError bool
}

// ConsumerStats counts what a [Consumer] did.
type ConsumerStats struct {
	Received      uint64 `json:"received"`
	LengthErrors  uint64 `json:"lengthErrors"`
	CounterErrors uint64 `json:"counterErrors"` // counter not following the previous one: SYNC lost or duplicated
}

// Consumer receives SYNC messages and reports them to a handler, with the
// counter and the end of the synchronous window.
type Consumer struct {
	bus  canopen.Bus
	cfg  config
	lc   lifecycle
	wake chan struct{}

	mu          sync.Mutex
	cob         canopen.COBID
	window      time.Duration
	overflow    uint8
	overflowSet bool
	last        Event
	lastCounter uint8
	stats       ConsumerStats
	hooks       unhook
}

// NewConsumer returns a SYNC consumer. Options: [WithLogger],
// [WithHandler], [WithDictionary], [WithCOBID], [WithWindow],
// [WithOverflow].
func NewConsumer(bus canopen.Bus, opts ...Option) (*Consumer, error) {
	c := &Consumer{bus: bus, cfg: newConfig(opts), wake: make(chan struct{}, 1)}
	c.cob = c.cfg.cob
	if d := c.cfg.dict; d != nil {
		cob, err := d.GetUint(od.IndexCOBIDSync, 0)
		if err != nil {
			return nil, fmt.Errorf("syncobj: COB-ID SYNC 0x1005: %w", err)
		}
		c.cob = canopen.COBID(cob)
		if w, err := d.GetUint(od.IndexSyncWindow, 0); err == nil {
			c.window = time.Duration(w) * time.Microsecond
		}
		if o, err := d.GetUint(od.IndexSyncOverflow, 0); err == nil {
			c.overflow, c.overflowSet = uint8(o), true
		}
		c.hooks.set(installHooks(d))
	}
	if c.cfg.cobSet {
		c.cob = c.cfg.cob
	}
	if c.cfg.windowSet {
		c.window = max(c.cfg.window, 0)
	}
	if c.cfg.overflowSet {
		c.overflow, c.overflowSet = c.cfg.overflow, true
	}
	return c, nil
}

func (c *Consumer) subscribe() (*canbus.Subscription, canopen.COBID, error) {
	c.mu.Lock()
	cob := c.cob
	c.mu.Unlock()
	flt := cob.Filter()
	flt.Remote = canbus.RemoteDataOnly
	sub, err := c.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{flt}}))
	if err != nil {
		return nil, 0, fmt.Errorf("syncobj: subscribe: %w", err)
	}
	return sub, cob, nil
}

// Start subscribes to the SYNC COB-ID and starts delivering; with a
// dictionary it follows writes of 0x1005, 0x1007 and 0x1019 from then on
// (a new COB-ID re-subscribes).
func (c *Consumer) Start() error {
	return c.lc.start(func(context.Context) (func(context.Context), error) {
		sub, cob, err := c.subscribe()
		if err != nil {
			return nil, err
		}
		var unwatch func()
		if d := c.cfg.dict; d != nil {
			unwatch = d.Watch(func(v *od.Variable, data []byte) {
				x := canopen.DecodeUint(data)
				c.mu.Lock()
				switch {
				case v.SubIndex != 0:
				case v.Index == od.IndexCOBIDSync:
					c.cob = canopen.COBID(x)
				case v.Index == od.IndexSyncWindow:
					c.window = time.Duration(x) * time.Microsecond
				case v.Index == od.IndexSyncOverflow:
					c.overflow, c.overflowSet = uint8(x), true
				}
				c.mu.Unlock()
				select {
				case c.wake <- struct{}{}:
				default:
				}
			})
		}
		return func(ctx context.Context) {
			if unwatch != nil {
				defer unwatch()
			}
			c.run(ctx, sub, cob)
		}, nil
	})
}

func (c *Consumer) run(ctx context.Context, sub *canbus.Subscription, cob canopen.COBID) {
	defer func() { _ = sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case <-c.wake:
			c.mu.Lock()
			changed := c.cob.ID() != cob.ID() || c.cob.Extended() != cob.Extended()
			c.mu.Unlock()
			if !changed {
				continue
			}
			nsub, ncob, err := c.subscribe()
			if err != nil {
				c.cfg.log.Warn("sync re-subscribe", "error", err)
				return
			}
			_ = sub.Close()
			sub, cob = nsub, ncob
		case f := <-sub.Frames():
			if e, ok := c.receive(&f, cob); ok {
				if h := c.cfg.handler; h != nil {
					h(e)
				}
			}
		}
	}
}

// receive processes one frame; ok is false for a frame that is no SYNC.
func (c *Consumer) receive(f *canbus.Frame, cob canopen.COBID) (Event, bool) {
	if !cob.Matches(f) || f.Flags&(canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) != 0 {
		return Event{}, false
	}
	t := f.Timestamp
	if t.IsZero() {
		t = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := Event{Time: t}
	if f.Len >= 1 {
		e.Counter = f.Data[0]
	}
	if c.overflowSet {
		want := uint8(0)
		if c.overflow != 0 {
			want = 1
		}
		if f.Len != want {
			e.LengthError = true
			c.stats.LengthErrors++
		}
	}
	if c.window > 0 {
		e.WindowEnd = t.Add(c.window)
	}
	if e.Counter != 0 && c.lastCounter != 0 && e.Counter != c.lastCounter+1 && e.Counter != 1 {
		c.stats.CounterErrors++
	}
	c.lastCounter = e.Counter
	c.stats.Received++
	c.last = e
	return e, true
}

// Last returns the last SYNC received; ok is false before the first one.
func (c *Consumer) Last() (e Event, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, !c.last.Time.IsZero()
}

// InWindow reports whether now lies within the synchronous window of the
// last SYNC: true when no window is configured, false before the first
// SYNC.
func (c *Consumer) InWindow(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last.Time.IsZero() {
		return false
	}
	if c.window <= 0 {
		return true
	}
	return now.Before(c.last.Time.Add(c.window))
}

// Close stops the consumer and waits for it. It removes the write hooks
// the consumer installed on the dictionary.
func (c *Consumer) Close() error {
	c.lc.close()
	c.hooks.remove()
	return nil
}

// Stats returns the counters.
func (c *Consumer) Stats() ConsumerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
