// SPDX-License-Identifier: MIT

package pdo

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// RPDOStats counts what an [RPDO] did.
type RPDOStats struct {
	Received    uint64 `json:"received"`    // PDOs received while enabled and active
	Applied     uint64 `json:"applied"`     // PDOs written to the dictionary
	Ignored     uint64 `json:"ignored"`     // PDOs received while disabled or inactive
	Short       uint64 `json:"short"`       // PDOs shorter than the mapping, discarded
	Long        uint64 `json:"long"`        // PDOs longer than the mapping, used
	Overwritten uint64 `json:"overwritten"` // synchronous PDOs replaced by a newer one before the SYNC
	WriteErrors uint64 `json:"writeErrors"` // mapped objects the dictionary refused
	Timeouts    uint64 `json:"timeouts"`    // deadline monitoring expiries
	// OutsideWindow counts synchronous PDOs discarded because they arrived
	// after the synchronous window (0x1007) of the last SYNC had closed.
	OutsideWindow uint64 `json:"outsideWindow"`
}

// RPDO is a receive PDO of a device: it reads its parameters from the
// communication (0x1400+n-1) and mapping (0x1600+n-1) objects of a
// dictionary, follows their changes, and writes the values of received
// PDOs to the mapped objects with remote writes (so the dictionary's
// write hooks, limits and watchers apply; a refused object is counted and
// the others are still written). Dummy entries skip their bits.
//
// Event-driven RPDOs (254, 255) are written when they arrive. Synchronous
// ones (0..240) are buffered and written on the next SYNC ([RPDO.OnSync]);
// a newer PDO before the SYNC replaces the buffered one. With a
// synchronous window (object 0x1007, µs, of the dictionary) a synchronous
// PDO that arrives after the window of the last SYNC has closed is
// discarded until the next SYNC, as CiA 301 describes object 0x1007 (and
// device manuals quoting it, e.g. Nanotec's "RxPDOs are rejected until
// the next SYNC"); it is counted in [RPDOStats.OutsideWindow]. Before the
// first SYNC there is no window. CANopenNode does not check the window
// for RPDOs (CO_PDO.c ignores it): this is a deliberate difference. The
// discarded PDO still counts as received for deadline monitoring and the
// length check.
//
// A CANopen FD RPDO ([WithFD]) takes mappings of up to 512 bits; frames
// padded to the next CAN FD length are not "too long".
//
// A PDO shorter than the mapping is discarded and reported with EMCY code
// 0x8210; a longer one is used (its first bytes) and reported with 0x8220,
// as CANopenNode does (CiA 301 only requires reporting it). The event
// timer is the deadline monitor: when it is non-zero, the time between two
// PDOs is monitored from the first PDO received after the RPDO was
// enabled or reconfigured, and an expiry is reported once with 0x8250.
// Errors go to the [WithErrorHandler] function, which also learns when
// they are resolved.
type RPDO struct {
	base

	buf     []byte // synchronous: the PDO waiting for the SYNC
	lastRx  time.Time
	armed   bool   // deadline monitoring running
	missed  bool   // deadline missed, reported
	lenErr  uint16 // active length error code
	pending []ErrorEvent
	winEnd  time.Time // end of the synchronous window of the last SYNC; zero: none

	st struct {
		received, applied, ignored, short, long, overwritten, writeErrors, timeouts, outsideWindow atomic.Uint64
	}
}

// NewRPDO returns RPDO n (1..512) of a device with the dictionary d. It
// installs the reconfiguration rules ([Protect]). Call Start, and
// SetEnabled(true) when the node is NMT operational.
func NewRPDO(bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) (*RPDO, error) {
	r := &RPDO{}
	if err := r.init(bus, d, false, n, opts); err != nil {
		return nil, err
	}
	r.remote = canbus.RemoteDataOnly
	r.reset = r.resetLocked
	r.wantSub = func() bool { return r.active }
	r.mu.Lock()
	r.reloadLocked()
	r.mu.Unlock()
	return r, nil
}

// Number returns the RPDO number.
func (r *RPDO) Number() int { return r.n }

// Start subscribes to the COB-ID and starts the service goroutine.
func (r *RPDO) Start() error { return r.start(r.run) }

// Close stops the service and waits for its goroutine. It is idempotent.
func (r *RPDO) Close() error { return r.close() }

// Config returns the current configuration.
func (r *RPDO) Config() Config { return r.config() }

// SetEnabled enables (NMT operational) or disables the RPDO. Disabling
// drops a buffered synchronous PDO and stops deadline monitoring.
func (r *RPDO) SetEnabled(on bool) { r.setEnabled(on, nil) }

// Stats returns a snapshot of the counters.
func (r *RPDO) Stats() RPDOStats {
	return RPDOStats{
		Received: r.st.received.Load(), Applied: r.st.applied.Load(), Ignored: r.st.ignored.Load(),
		Short: r.st.short.Load(), Long: r.st.long.Load(), Overwritten: r.st.overwritten.Load(),
		WriteErrors: r.st.writeErrors.Load(), Timeouts: r.st.timeouts.Load(),
		OutsideWindow: r.st.outsideWindow.Load(),
	}
}

func (r *RPDO) resetLocked() {
	r.buf = nil
	r.armed, r.missed = false, false
	r.lenErr = 0
	r.winEnd = time.Time{}
}

// OnSync applies a buffered synchronous PDO, from the calling goroutine,
// and opens the synchronous window the dictionary's 0x1007 defines.
func (r *RPDO) OnSync(counter uint8) { r.OnSyncWindow(counter, r.windowEnd()) }

// OnSyncWindow processes a SYNC as [RPDO.OnSync] does, with the end of its
// synchronous window given (a syncobj.Event's WindowEnd; the zero time:
// no window). Synchronous PDOs received after windowEnd are discarded
// until the next SYNC.
func (r *RPDO) OnSyncWindow(_ uint8, windowEnd time.Time) {
	r.mu.Lock()
	data, m := r.buf, r.cfg.Mapping
	r.buf = nil
	if r.enabled {
		r.winEnd = windowEnd
	}
	ok := r.enabled && r.active && r.cfg.TransmissionType.Synchronous()
	r.mu.Unlock()
	if ok && data != nil {
		r.apply(m, data)
	}
}

// apply writes PDO data to the mapped objects.
func (r *RPDO) apply(m Mapping, data []byte) {
	vals, err := unpack(m, data, r.opt.maxBits())
	if err != nil {
		return
	}
	for i, e := range m {
		if e.IsDummy() {
			continue
		}
		if err := r.d.Write(e.Index, e.SubIndex, vals[i]); err != nil {
			r.st.writeErrors.Add(1)
			r.log.Debug("write mapped object", "object", e, "err", err)
		}
	}
	r.st.applied.Add(1)
	if r.opt.onRx != nil {
		r.opt.onRx(data)
	}
}

// run is the service goroutine.
func (r *RPDO) run() {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		at := r.check()
		f, got, stop := r.wait(timer, at)
		if stop {
			return
		}
		if got {
			r.receive(&f)
		}
		r.report()
	}
}

// check detects a missed deadline and returns when to look again.
func (r *RPDO) check() time.Time {
	r.mu.Lock()
	var at time.Time
	if et := r.cfg.EventTimer; et > 0 && r.armed && !r.missed && r.enabled && r.active {
		at = r.lastRx.Add(time.Duration(et) * time.Millisecond)
		if !time.Now().Before(at) {
			r.missed = true
			at = time.Time{}
			r.st.timeouts.Add(1)
			r.pending = append(r.pending, ErrorEvent{PDO: r.n, Code: EMCYRPDOTimeout, Active: true,
				Err: fmt.Errorf("pdo: RPDO%d: no PDO within the event timer of %d ms", r.n, et)})
		}
	}
	r.mu.Unlock()
	r.report()
	return at
}

// report delivers the pending error events.
func (r *RPDO) report() {
	r.mu.Lock()
	evs := r.pending
	r.pending = nil
	r.mu.Unlock()
	if r.opt.onError == nil {
		return
	}
	for _, ev := range evs {
		r.opt.onError(ev)
	}
}

// receive handles a PDO.
func (r *RPDO) receive(f *canbus.Frame) {
	r.mu.Lock()
	cfg := r.cfg
	if f.IsRemote() || !cfg.COBID.Matches(f) {
		r.mu.Unlock()
		return
	}
	if !r.enabled || !r.active {
		r.mu.Unlock()
		r.st.ignored.Add(1)
		return
	}
	r.st.received.Add(1)
	data := append([]byte(nil), f.Payload()...)
	need := cfg.Mapping.Len()
	most := need
	if r.opt.fd {
		most = canbus.RoundUpFDLen(need)
	}
	code := uint16(0)
	switch {
	case len(data) < need:
		code = EMCYPDOLength
		r.st.short.Add(1)
	case len(data) > most:
		code = EMCYPDOLengthExceeded
		r.st.long.Add(1)
	}
	if code != r.lenErr {
		if r.lenErr != 0 {
			r.pending = append(r.pending, ErrorEvent{PDO: r.n, Code: r.lenErr, Active: false})
		}
		if code != 0 {
			r.pending = append(r.pending, ErrorEvent{PDO: r.n, Code: code, Active: true,
				Err: fmt.Errorf("pdo: RPDO%d: %d bytes received, mapping has %d", r.n, len(data), need)})
		}
		r.lenErr = code
	}
	if code == EMCYPDOLength {
		r.mu.Unlock()
		return
	}
	r.lastRx, r.armed = time.Now(), true
	if r.missed {
		r.missed = false
		r.pending = append(r.pending, ErrorEvent{PDO: r.n, Code: EMCYRPDOTimeout, Active: false})
	}
	if cfg.TransmissionType.Synchronous() {
		if r.windowClosed(r.winEnd) {
			r.st.outsideWindow.Add(1)
			r.mu.Unlock()
			r.log.Debug("synchronous PDO outside the synchronous window, discarded")
			return
		}
		if r.buf != nil {
			r.st.overwritten.Add(1)
		}
		r.buf = data
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.apply(cfg.Mapping, data)
}
