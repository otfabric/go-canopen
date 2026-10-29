// SPDX-License-Identifier: MIT

package pdo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// TPDOStats counts what a [TPDO] did.
type TPDOStats struct {
	Sent        uint64 `json:"sent"`        // PDOs transmitted
	SendErrors  uint64 `json:"sendErrors"`  // transmissions the bus refused
	ReadErrors  uint64 `json:"readErrors"`  // transmissions skipped: a mapped object could not be read
	Unchanged   uint64 `json:"unchanged"`   // change events with unchanged data, not sent
	Syncs       uint64 `json:"syncs"`       // SYNCs seen while enabled and active
	RTRs        uint64 `json:"rtrs"`        // remote transmission requests received
	EventTimers uint64 `json:"eventTimers"` // transmissions caused by the event timer
	// WindowMissed counts synchronous transmissions discarded because the
	// synchronous window (0x1007) closed before they were sent.
	WindowMissed uint64 `json:"windowMissed"`
}

// TPDO is a transmit PDO of a device: it reads its parameters from the
// communication (0x1800+n-1) and mapping (0x1A00+n-1) objects of a
// dictionary, follows their changes, and sends the mapped values
// according to the transmission type:
//
//   - 0 (synchronous acyclic): on the SYNC after an event ([TPDO.Trigger],
//     a change of a mapped object, a remote request).
//   - 1..240 (synchronous cyclic): on every nth SYNC; with a SYNC start
//     value s > 0 and SYNCs that carry a counter, the first transmission
//     is on the SYNC with counter s. A remote request is answered on the
//     next SYNC.
//   - 252: sampled on every SYNC, sent on a remote request.
//   - 253: sampled and sent on a remote request.
//   - 254, 255 (event-driven): on an event — a change of a mapped object
//     (the dictionary's watchers; a write of an unchanged value is not
//     sent), [TPDO.Trigger], a remote request, the event timer expiring,
//     or the service being enabled — no sooner than the inhibit time after
//     the previous transmission. The event timer restarts with every
//     transmission.
//
// Synchronous transmissions (types 0..240) are made only within the
// synchronous window after the SYNC (object 0x1007, µs, of the
// dictionary; 0 or absent: no window): a PDO sampled at the SYNC but not
// sent before the window closes is discarded and counted in
// [TPDOStats.WindowMissed], as CiA 301 and CANopenNode (which clears the
// pending synchronous TPDOs when the window passes, CO_SYNC.c/CANopen.c)
// do. The send is bounded by the window: a bus that cannot take the frame
// in time drops it.
//
// Remote requests are answered unless COB-ID bit 30 is set or the PDO is
// a CANopen FD PDO ([WithFD]: CAN FD has no remote frames). The TPDO
// transmits only while enabled ([TPDO.SetEnabled]: NMT operational) and
// active ([Config.Active]). Mapped objects are read with remote reads, so
// read hooks may supply values. SYNC reaches the TPDO through
// [TPDO.OnSync], which the node wires to its SYNC consumer.
type TPDO struct {
	base

	syncCnt   int  // SYNCs counted towards the next cyclic transmission
	waitStart bool // waiting for the SYNC start value
	syncReq   bool // type 0: an event since the last SYNC; 1..240: RTR pending
	evForce   bool // 254/255: a transmission is due
	evChange  bool // 254/255: a mapped object changed
	sample    []byte
	hasSample bool
	lastSent  time.Time
	lastData  []byte
	evDue     time.Time // event timer deadline

	st struct {
		sent, sendErrors, readErrors, unchanged, syncs, rtrs, eventTimers, windowMissed atomic.Uint64
	}
}

// NewTPDO returns TPDO n (1..512) of a device with the dictionary d. It
// installs the reconfiguration rules ([Protect]). Call Start, and
// SetEnabled(true) when the node is NMT operational.
func NewTPDO(bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) (*TPDO, error) {
	t := &TPDO{}
	if err := t.init(bus, d, true, n, opts); err != nil {
		return nil, err
	}
	t.remote = canbus.RemoteOnly
	t.reset = t.resetLocked
	t.wantSub = func() bool { return t.active && t.cfg.COBID&canopen.COBNoRTR == 0 && !t.opt.fd }
	t.onValue = t.watchValues
	t.mu.Lock()
	t.reloadLocked()
	t.mu.Unlock()
	return t, nil
}

// Number returns the TPDO number.
func (t *TPDO) Number() int { return t.n }

// Start subscribes to remote requests and starts the service goroutine.
func (t *TPDO) Start() error {
	if err := t.start(t.run); err != nil {
		return err
	}
	return nil
}

// Close stops the service and waits for its goroutine. It is idempotent.
func (t *TPDO) Close() error { return t.close() }

// Config returns the current configuration.
func (t *TPDO) Config() Config { return t.config() }

// Stats returns a snapshot of the counters.
func (t *TPDO) Stats() TPDOStats {
	return TPDOStats{
		Sent: t.st.sent.Load(), SendErrors: t.st.sendErrors.Load(), ReadErrors: t.st.readErrors.Load(),
		Unchanged: t.st.unchanged.Load(), Syncs: t.st.syncs.Load(), RTRs: t.st.rtrs.Load(),
		EventTimers: t.st.eventTimers.Load(), WindowMissed: t.st.windowMissed.Load(),
	}
}

// SetEnabled enables (NMT operational) or disables the TPDO. Enabling an
// event-driven TPDO transmits it (as CANopenNode does), so that consumers
// get the current values and their deadline monitoring starts.
func (t *TPDO) SetEnabled(on bool) {
	t.setEnabled(on, func() { t.evForce = t.active && t.cfg.TransmissionType.EventDriven() })
}

// resetLocked restarts the transmission state after a configuration
// change or an enable/disable.
func (t *TPDO) resetLocked() {
	t.syncCnt = 0
	t.waitStart = t.cfg.SyncStart != 0
	t.syncReq, t.evChange, t.hasSample = false, false, false
	t.evForce = false
	t.evDue = time.Time{}
	if et := t.cfg.EventTimer; et > 0 && t.enabled && t.active && t.cfg.TransmissionType.EventDriven() {
		t.evDue = time.Now().Add(time.Duration(et) * time.Millisecond)
	}
	t.lastData = nil
}

// Trigger signals an application event: an event-driven TPDO is sent (no
// sooner than its inhibit time allows), a synchronous acyclic one on the
// next SYNC. Other types ignore it.
func (t *TPDO) Trigger() { t.event(true) }

func (t *TPDO) event(force bool) {
	t.mu.Lock()
	switch tt := t.cfg.TransmissionType; {
	case !t.enabled || !t.active:
	case tt == TransSyncAcyclic:
		t.syncReq = true
	case tt.EventDriven():
		if force {
			t.evForce = true
		} else {
			t.evChange = true
		}
		t.poke()
	}
	t.mu.Unlock()
}

// OnSync processes a SYNC with its counter (0 for a SYNC without one),
// received now. It sends the TPDO from the calling goroutine when it is
// due, within the synchronous window the dictionary's 0x1007 defines.
func (t *TPDO) OnSync(counter uint8) { t.OnSyncWindow(counter, t.windowEnd()) }

// OnSyncWindow processes a SYNC as [TPDO.OnSync] does, with the end of its
// synchronous window given (a syncobj.Event's WindowEnd; the zero time:
// no window). A synchronous transmission not made before windowEnd is
// discarded and counted in [TPDOStats.WindowMissed].
func (t *TPDO) OnSyncWindow(counter uint8, windowEnd time.Time) {
	t.mu.Lock()
	if !t.enabled || !t.active {
		t.mu.Unlock()
		return
	}
	t.st.syncs.Add(1)
	cfg := t.cfg
	tt := cfg.TransmissionType
	send, sample := false, false
	switch {
	case tt == TransSyncAcyclic:
		send = t.syncReq
		t.syncReq = false
	case tt <= TransSyncMax:
		if t.waitStart && counter != 0 {
			if counter == cfg.SyncStart {
				t.waitStart = false
				t.syncCnt = 0
				send = true
			}
		} else {
			t.syncCnt++
			if t.syncCnt >= int(tt) {
				t.syncCnt = 0
				send = true
			}
		}
		if t.syncReq {
			send, t.syncReq = true, false
		}
	case tt == TransRTRSync:
		sample = true
	}
	t.mu.Unlock()
	if !send && !sample {
		return
	}
	data, err := t.read(cfg.Mapping)
	if err != nil {
		return
	}
	if sample {
		t.mu.Lock()
		t.sample, t.hasSample = data, true
		t.mu.Unlock()
		return
	}
	t.transmitInWindow(cfg.COBID, data, windowEnd)
}

// transmitInWindow sends a synchronous PDO if the window that ends at end
// is still open, with the send bounded by it.
func (t *TPDO) transmitInWindow(c canopen.COBID, data []byte, end time.Time) {
	if end.IsZero() {
		_ = t.transmit(t.ctx, c, data)
		return
	}
	left := end.Sub(t.opt.clock())
	if left <= 0 {
		t.st.windowMissed.Add(1)
		t.log.Debug("synchronous window closed, PDO discarded")
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, left)
	defer cancel()
	if err := t.transmitCounted(ctx, c, data, false); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil && t.ctx.Err() == nil {
			t.st.windowMissed.Add(1)
			t.log.Debug("synchronous window closed during send, PDO discarded")
			return
		}
		t.st.sendErrors.Add(1)
	}
}

// Transmit reads the mapped objects and sends the TPDO now, whatever its
// transmission type and inhibit time. It fails with [ErrDisabled] or
// [ErrInactive] when the TPDO cannot be sent.
func (t *TPDO) Transmit(ctx context.Context) error {
	t.mu.Lock()
	cfg, enabled, act := t.cfg, t.enabled, t.active
	t.mu.Unlock()
	switch {
	case !enabled:
		return ErrDisabled
	case !act:
		return ErrInactive
	}
	data, err := t.read(cfg.Mapping)
	if err != nil {
		return err
	}
	return t.transmit(ctx, cfg.COBID, data)
}

// read reads and packs the mapped objects.
func (t *TPDO) read(m Mapping) ([]byte, error) {
	vals := make([][]byte, len(m))
	for i, e := range m {
		b, err := t.d.Read(e.Index, e.SubIndex)
		if err != nil {
			t.st.readErrors.Add(1)
			t.log.Warn("read mapped object", "object", e, "err", err)
			return nil, fmt.Errorf("pdo: TPDO%d: read %s: %w", t.n, e, err)
		}
		vals[i] = b
	}
	data, err := pack(m, vals, t.opt.maxBits())
	if err != nil {
		t.st.readErrors.Add(1)
		return nil, err
	}
	return data, nil
}

// transmit sends data and restarts the inhibit and event timers.
func (t *TPDO) transmit(ctx context.Context, c canopen.COBID, data []byte) error {
	return t.transmitCounted(ctx, c, data, true)
}

// transmitCounted is transmit; countErr selects whether a failure counts
// as a send error.
func (t *TPDO) transmitCounted(ctx context.Context, c canopen.COBID, data []byte, countErr bool) error {
	t.mu.Lock()
	now := time.Now()
	t.lastSent, t.lastData = now, data
	if et := t.cfg.EventTimer; et > 0 && t.cfg.TransmissionType.EventDriven() {
		t.evDue = now.Add(time.Duration(et) * time.Millisecond)
	}
	t.mu.Unlock()
	t.poke()
	if err := t.send(ctx, c, data); err != nil {
		if countErr {
			t.st.sendErrors.Add(1)
		}
		t.log.Debug("send", "err", err)
		return err
	}
	t.st.sent.Add(1)
	return nil
}

// inhibitEnd returns when the inhibit time after the last transmission
// ends, mu held.
func (t *TPDO) inhibitEnd() time.Time {
	if t.lastSent.IsZero() || t.cfg.InhibitTime == 0 {
		return time.Time{}
	}
	return t.lastSent.Add(time.Duration(t.cfg.InhibitTime) * 100 * time.Microsecond)
}

// run is the service goroutine: event-driven transmissions, the event
// timer and remote requests.
func (t *TPDO) run() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		at := t.step()
		f, got, stop := t.wait(timer, at)
		if stop {
			return
		}
		if got {
			t.onRTR(&f)
		}
	}
}

// step sends a due event-driven transmission and returns when the
// goroutine must next look (zero: only when woken).
func (t *TPDO) step() time.Time {
	t.mu.Lock()
	cfg := t.cfg
	if !t.enabled || !t.active || !cfg.TransmissionType.EventDriven() {
		t.mu.Unlock()
		return time.Time{}
	}
	now := time.Now()
	if !t.evDue.IsZero() && !now.Before(t.evDue) {
		t.evForce = true
		t.evDue = time.Time{}
		t.st.eventTimers.Add(1)
	}
	if t.evForce || t.evChange {
		if end := t.inhibitEnd(); !end.IsZero() && now.Before(end) {
			t.mu.Unlock()
			return end
		}
		force := t.evForce
		last := t.lastData
		t.evForce, t.evChange = false, false
		t.mu.Unlock()
		data, err := t.read(cfg.Mapping)
		if err == nil {
			if !force && last != nil && bytes.Equal(data, last) {
				t.st.unchanged.Add(1)
			} else {
				_ = t.transmit(t.ctx, cfg.COBID, data)
			}
		}
		t.mu.Lock()
	}
	at := t.evDue
	if t.evForce || t.evChange {
		at = now // another event arrived meanwhile
	}
	t.mu.Unlock()
	return at
}

// onRTR answers a remote transmission request.
func (t *TPDO) onRTR(f *canbus.Frame) {
	t.mu.Lock()
	cfg := t.cfg
	if !f.IsRemote() || !cfg.COBID.Matches(f) || cfg.COBID&canopen.COBNoRTR != 0 || !t.enabled || !t.active || t.opt.fd {
		t.mu.Unlock()
		return
	}
	t.st.rtrs.Add(1)
	tt := cfg.TransmissionType
	var data []byte
	now := false
	switch {
	case tt <= TransSyncMax:
		t.syncReq = true
	case tt == TransRTRSync:
		if t.hasSample {
			data = t.sample
		}
		now = true
	case tt == TransRTR:
		now = true
	default:
		t.evForce = true
	}
	t.mu.Unlock()
	if !now {
		return
	}
	if data == nil {
		var err error
		if data, err = t.read(cfg.Mapping); err != nil {
			return
		}
	}
	_ = t.transmit(t.ctx, cfg.COBID, data)
}

// watchValues triggers change events for mapped objects.
func (t *TPDO) watchValues(v *od.Variable) {
	t.mu.Lock()
	mapped := t.mapped[varKey{v.Index, v.SubIndex}]
	t.mu.Unlock()
	if mapped {
		t.event(false)
	}
}
