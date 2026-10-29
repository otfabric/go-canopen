// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// varKey addresses a sub-object.
type varKey struct {
	index uint16
	sub   uint8
}

// base is what TPDO and RPDO share: the dictionary binding, the
// configuration, the subscription and the goroutine.
type base struct {
	bus      canopen.Bus
	d        *od.Dictionary
	n        int
	transmit bool
	opt      options
	log      *slog.Logger
	remote   canbus.RemoteMatch // frames the service receives

	mu      sync.Mutex
	cfg     Config
	active  bool // cfg.Active() and the mapping is valid
	enabled bool
	mapped  map[varKey]bool
	sub     *canbus.Subscription
	key     subKey
	started bool
	closed  bool
	// reset is called with mu held after the configuration changed or the
	// service was enabled or disabled.
	reset func()
	// wantSub reports whether the service needs a subscription, mu held.
	wantSub func() bool
	// onValue is called, without mu, when another object changes.
	onValue func(v *od.Variable)

	wake    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	unwatch func()
	// unprotect removes the hooks Protect installed.
	unprotect func()
}

func (b *base) init(bus canopen.Bus, d *od.Dictionary, transmit bool, n int, opts []Option) error {
	unprotect, err := Protect(d, transmit, n)
	if err != nil {
		return err
	}
	b.unprotect = unprotect
	b.bus, b.d, b.n, b.transmit = bus, d, n, transmit
	b.opt = buildOptions(opts)
	b.log = b.opt.log.With("pdo", kind(transmit, n))
	b.wake = make(chan struct{}, 1)
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return nil
}

// reloadLocked reads the configuration from the dictionary.
func (b *base) reloadLocked() {
	cfg, err := Load(b.d, b.transmit, b.n)
	if err == nil && cfg.Active() {
		err = cfg.Mapping.Validate(b.d, b.transmit)
	}
	if err == nil && cfg.COBID.Valid() {
		err = checkCOBID(cfg.COBID)
	}
	if err == nil && !cfg.TransmissionType.ValidFor(b.transmit) {
		err = canopen.AbortInvalidValue
	}
	b.cfg = cfg
	b.active = err == nil && cfg.Active()
	if err != nil {
		b.log.Warn("PDO configuration not usable, PDO inactive", "err", err)
	}
	b.mapped = make(map[varKey]bool, len(cfg.Mapping))
	for _, e := range cfg.Mapping {
		b.mapped[varKey{e.Index, e.SubIndex}] = true
	}
	b.reset()
	b.resubLocked()
}

// resubLocked opens, replaces or closes the subscription as the
// configuration needs.
func (b *base) resubLocked() {
	if !b.started || b.closed {
		return
	}
	want := keyOf(b.wantSub(), b.cfg.COBID)
	if want == b.key {
		return
	}
	if b.sub != nil {
		_ = b.sub.Close()
		b.sub = nil
	}
	b.key = subKey{}
	if !want.on {
		return
	}
	s, err := b.bus.Subscribe(canbus.WithFilters(filter(want.cob, b.remote)))
	if err != nil {
		b.log.Error("subscribe", "cob-id", want, "err", err)
		return
	}
	b.sub, b.key = s, want
}

// poke wakes the goroutine.
func (b *base) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// start subscribes, starts watching the dictionary and runs loop in a
// goroutine.
func (b *base) start(loop func()) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return canopen.ErrClosed
	}
	if b.started {
		return nil
	}
	b.started = true
	b.reloadLocked()
	if b.wantSub() && b.sub == nil {
		b.started = false
		return errors.New("pdo: " + kind(b.transmit, b.n) + ": subscribe failed")
	}
	b.unwatch = b.d.Watch(b.watch)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		loop()
	}()
	return nil
}

// watch follows changes of the PDO parameters.
func (b *base) watch(v *od.Variable, _ []byte) {
	if v.Index != CommIndex(b.transmit, b.n) && v.Index != MappingIndex(b.transmit, b.n) {
		if b.onValue != nil {
			b.onValue(v)
		}
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.reloadLocked()
	}
	b.mu.Unlock()
	b.poke()
}

// close stops the goroutine and waits for it.
func (b *base) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.cancel()
	if b.sub != nil {
		_ = b.sub.Close()
		b.sub = nil
	}
	unwatch := b.unwatch
	b.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	b.unprotect()
	b.wg.Wait()
	return nil
}

// setEnabled switches the service on or off.
func (b *base) setEnabled(on bool, enabled func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.enabled == on {
		return
	}
	b.enabled = on
	b.reset()
	if on && enabled != nil {
		enabled()
	}
	b.poke()
}

// config returns the configuration.
func (b *base) config() Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.cfg
	c.Mapping = append(Mapping(nil), c.Mapping...)
	return c
}

// wait blocks until the context ends, the goroutine is woken, the timer
// fires (at; zero means never) or a frame arrives. It returns the frame
// and whether there is one; stop is true when the service ends.
func (b *base) wait(t *time.Timer, at time.Time) (f canbus.Frame, got, stop bool) {
	b.mu.Lock()
	sub := b.sub
	b.mu.Unlock()
	var frames <-chan canbus.Frame
	var subDone <-chan struct{}
	if sub != nil {
		frames, subDone = sub.Frames(), sub.Done()
	}
	var tc <-chan time.Time
	if !at.IsZero() {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(max(time.Until(at), 0))
		tc = t.C
	}
	select {
	case <-b.ctx.Done():
		return f, false, true
	case <-b.wake:
	case <-tc:
	case f = <-frames:
		return f, true, false
	case <-subDone:
		b.mu.Lock()
		swapped := b.sub != sub
		b.mu.Unlock()
		if !swapped {
			b.log.Error("subscription ended", "err", sub.Err())
			return f, false, true
		}
	}
	return f, false, false
}

// send transmits a data frame on the COB-ID.
func (b *base) send(ctx context.Context, c canopen.COBID, data []byte) error {
	f, err := newFrame(c, data)
	if err != nil {
		return err
	}
	return b.bus.Send(ctx, &f)
}
