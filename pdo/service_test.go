// SPDX-License-Identifier: MIT

package pdo

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// recorder collects the frames of a subscription.
type recorder struct {
	mu     sync.Mutex
	frames []canbus.Frame
}

func record(t *testing.T, bus *canbus.Bus, id canbus.ID) *recorder {
	t.Helper()
	s, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(id)}}))
	if err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, err := s.Receive(context.Background())
			if err != nil {
				return
			}
			r.mu.Lock()
			r.frames = append(r.frames, f)
			r.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = s.Close(); <-done })
	return r
}

func (r *recorder) n() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func (r *recorder) last() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.frames) == 0 {
		return nil
	}
	f := r.frames[len(r.frames)-1]
	return append([]byte(nil), f.Payload()...)
}

func newTPDO(t *testing.T, bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) *TPDO {
	t.Helper()
	tp, err := NewTPDO(bus, d, n, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Close() })
	return tp
}

func newRPDO(t *testing.T, bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) *RPDO {
	t.Helper()
	rp, err := NewRPDO(bus, d, n, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := rp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rp.Close() })
	return rp
}

var e2eMapping = Mapping{{idxU8, 0, 8}, {idxBool, 0, 1}, {idxBits, 2, 1}, {idxI16, 0, 16}, {idxU32, 0, 32}}

// TestEndToEnd sends TPDO1 of node 5 to RPDO1 of node 6 for every
// transmission type.
func TestEndToEnd(t *testing.T) {
	tests := []struct {
		name     string
		tt       TransmissionType
		rt       TransmissionType
		start    uint8
		stimulus func(t *testing.T, tp *TPDO, rp *RPDO, l *Listener, rec *recorder)
	}{
		{"0 acyclic sync", 0, 0, 0, func(t *testing.T, tp *TPDO, rp *RPDO, _ *Listener, rec *recorder) {
			tp.OnSync(0) // no event yet: nothing
			never(t, "TPDO without event", 20*time.Millisecond, func() bool { return rec.n() > 0 })
			tp.Trigger()
			tp.OnSync(0)
			eventually(t, "RPDO buffered", func() bool { return rp.Stats().Received == 1 })
			rp.OnSync(0)
		}},
		{"1 cyclic", 1, 1, 0, func(t *testing.T, tp *TPDO, rp *RPDO, _ *Listener, rec *recorder) {
			tp.OnSync(0)
			eventually(t, "RPDO buffered", func() bool { return rp.Stats().Received == 1 })
			rp.OnSync(0)
		}},
		{"3 cyclic, SYNC start 5", 3, 240, 5, func(t *testing.T, tp *TPDO, rp *RPDO, _ *Listener, rec *recorder) {
			for c := uint8(1); c <= 4; c++ {
				tp.OnSync(c)
			}
			never(t, "TPDO before SYNC start", 20*time.Millisecond, func() bool { return rec.n() > 0 })
			tp.OnSync(5)
			eventually(t, "first TPDO", func() bool { return rec.n() == 1 })
			tp.OnSync(6)
			tp.OnSync(7)
			never(t, "TPDO before the 3rd SYNC", 20*time.Millisecond, func() bool { return rec.n() > 1 })
			tp.OnSync(8)
			eventually(t, "second TPDO", func() bool { return rec.n() == 2 })
			eventually(t, "RPDO buffered", func() bool { return rp.Stats().Received == 2 })
			rp.OnSync(9)
		}},
		{"252 RTR sync", 252, 255, 0, func(t *testing.T, tp *TPDO, rp *RPDO, l *Listener, rec *recorder) {
			tp.OnSync(0) // sample
			if err := tp.d.SetValue(idxU8, 0, uint8(99)); err != nil {
				t.Fatal(err)
			}
			if err := l.Request(ctx(t)); err != nil {
				t.Fatal(err)
			}
			eventually(t, "TPDO", func() bool { return rec.n() == 1 })
			if rec.last()[0] != 42 {
				t.Errorf("252 sent % X, want the SYNC sample", rec.last())
			}
		}},
		{"253 RTR", 253, 254, 0, func(t *testing.T, tp *TPDO, rp *RPDO, l *Listener, rec *recorder) {
			if err := l.Request(ctx(t)); err != nil {
				t.Fatal(err)
			}
		}},
		{"254 event", 254, 255, 0, func(t *testing.T, tp *TPDO, rp *RPDO, l *Listener, rec *recorder) {}},
		{"255 event", 255, 254, 0, func(t *testing.T, tp *TPDO, rp *RPDO, l *Listener, rec *recorder) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := testbus.Pair(t)
			da, db := device(t, 5), device(t, 6)
			configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: tt.tt, SyncStart: tt.start, Mapping: e2eMapping})
			configure(t, db, false, 1, Config{COBID: 0x185, TransmissionType: tt.rt, Mapping: e2eMapping})
			rec := record(t, b, 0x185)
			tp := newTPDO(t, a, da, 1)
			rp := newRPDO(t, b, db, 1)
			dec, err := NewDecoder(e2eMapping, db)
			if err != nil {
				t.Fatal(err)
			}
			l := NewListener(b, 0x185, dec, nil)
			if err := l.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			_ = da.SetValue(idxU8, 0, uint8(42))
			_ = da.SetValue(idxBool, 0, true)
			_ = da.SetValue(idxBits, 2, true)
			_ = da.SetValue(idxI16, 0, int16(-2))
			_ = da.SetValue(idxU32, 0, uint32(0xDEADBEEF))
			rp.SetEnabled(true)
			tp.SetEnabled(true)
			tt.stimulus(t, tp, rp, l, rec)
			eventually(t, "values at the RPDO", func() bool {
				v, _ := db.GetValue(idxU32, 0)
				return v == uint64(0xDEADBEEF)
			})
			for _, c := range []struct {
				i    uint16
				s    uint8
				want any
			}{{idxU8, 0, uint64(42)}, {idxBool, 0, true}, {idxBits, 2, true}, {idxBits, 1, false}, {idxI16, 0, int64(-2)}} {
				if v, _ := db.GetValue(c.i, c.s); v != c.want {
					t.Errorf("%04X:%02X = %v, want %v", c.i, c.s, v, c.want)
				}
			}
		})
	}
}

// TestTPDOPacking checks the exact frame of the end-to-end mapping.
func TestTPDOPacking(t *testing.T) {
	a, b := testbus.Pair(t)
	da := device(t, 5)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: e2eMapping})
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, da, 1)
	_ = da.SetValue(idxU8, 0, uint8(0x42))
	_ = da.SetValue(idxBool, 0, true)
	_ = da.SetValue(idxI16, 0, int16(-2))
	_ = da.SetValue(idxU32, 0, uint32(0xDEADBEEF))
	tp.SetEnabled(true)
	eventually(t, "TPDO", func() bool { return rec.n() >= 1 })
	// 42 | bool<<8 | bit2<<9 | i16<<10 | u32<<26: 58 bits, 8 bytes.
	acc := uint64(0x42) | 1<<8 | 0<<9 | uint64(0xFFFE)<<10 | uint64(0xDEADBEEF)<<26
	want := make([]byte, 8)
	for i := range want {
		want[i] = byte(acc >> (8 * i))
	}
	if got := rec.last(); !bytes.Equal(got, want) {
		t.Errorf("frame % X, want % X", got, want)
	}
}

func TestTPDOEventAndInhibit(t *testing.T) {
	a, b := testbus.Pair(t)
	da := device(t, 5)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 255, InhibitTime: 500, Mapping: Mapping{{idxU8, 0, 8}}})
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, da, 1)
	_ = da.SetValue(idxU8, 0, uint8(1)) // disabled: nothing
	never(t, "TPDO while disabled", 20*time.Millisecond, func() bool { return rec.n() > 0 })
	tp.SetEnabled(true) // sends the current value
	eventually(t, "TPDO on enable", func() bool { return rec.n() == 1 })
	start := time.Now()
	for i := 2; i <= 20; i++ {
		_ = da.SetValue(idxU8, 0, uint8(i))
		time.Sleep(time.Millisecond)
	}
	eventually(t, "last value", func() bool { l := rec.last(); return l != nil && l[0] == 20 })
	elapsed := time.Since(start)
	// With 50 ms inhibit time, at most elapsed/50ms + 1 more PDOs.
	if max := int(elapsed/(50*time.Millisecond)) + 2; rec.n() > max {
		t.Errorf("%d PDOs in %v with 50 ms inhibit time", rec.n(), elapsed)
	}
	// Writing an unchanged value sends nothing.
	n := rec.n()
	_ = da.SetValue(idxU8, 0, uint8(20))
	never(t, "PDO for unchanged value", 80*time.Millisecond, func() bool { return rec.n() > n })
	if tp.Stats().Unchanged == 0 {
		t.Error("unchanged not counted")
	}
	// Trigger always sends.
	tp.Trigger()
	eventually(t, "triggered PDO", func() bool { return rec.n() == n+1 })
	// Disabled: no event.
	tp.SetEnabled(false)
	tp.SetEnabled(false)
	_ = da.SetValue(idxU8, 0, uint8(21))
	tp.Trigger()
	never(t, "PDO while disabled", 80*time.Millisecond, func() bool { return rec.n() > n+1 })
	if err := tp.Transmit(ctx(t)); !errors.Is(err, ErrDisabled) {
		t.Errorf("Transmit disabled: %v", err)
	}
}

func TestTPDOEventTimer(t *testing.T) {
	a, b := testbus.Pair(t)
	da := device(t, 5)
	configure(t, da, true, 2, Config{COBID: 0x285, TransmissionType: 254, EventTimer: 20, Mapping: Mapping{{idxU8, 0, 8}}})
	rec := record(t, b, 0x285)
	tp := newTPDO(t, a, da, 2)
	tp.SetEnabled(true)
	eventually(t, "periodic PDOs", func() bool { return rec.n() >= 5 })
	if tp.Stats().EventTimers < 3 {
		t.Errorf("event timer transmissions: %+v", tp.Stats())
	}
	// Changing the event timer while running restarts it.
	if err := da.Write(CommIndex(true, 2), 5, le16(0)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	n := rec.n()
	never(t, "PDO with event timer off", 80*time.Millisecond, func() bool { return rec.n() > n })
}

func TestTPDORTR(t *testing.T) {
	a, b := testbus.Pair(t)
	da := device(t, 5)
	cfg := Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}}
	configure(t, da, true, 1, cfg)
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, da, 1)
	tp.SetEnabled(true)
	rtr := func() {
		f, _ := canbus.NewRemote(0x185, false, 1)
		if err := b.Send(ctx(t), &f); err != nil {
			t.Fatal(err)
		}
	}
	data := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		n := 0
		for _, f := range rec.frames {
			if !f.IsRemote() {
				n++
			}
		}
		return n
	}
	// Synchronous: the RTR is answered on the next SYNC (here the cyclic
	// transmission anyway).
	rtr()
	eventually(t, "RTR counted", func() bool { return tp.Stats().RTRs == 1 })
	if data() != 0 {
		t.Fatal("synchronous TPDO answered RTR before SYNC")
	}
	tp.OnSync(0)
	eventually(t, "PDO on SYNC", func() bool { return data() == 1 })

	// Event-driven: answered at once.
	if err := da.Write(CommIndex(true, 1), 2, []byte{254}); err != nil {
		t.Fatal(err)
	}
	rtr()
	eventually(t, "PDO on RTR", func() bool { return data() == 2 })

	// Bit 30: no RTR allowed, no answer.
	if err := da.Write(CommIndex(true, 1), 1, le32(0x185|uint32(canopen.COBNoRTR))); err != nil {
		t.Fatal(err)
	}
	rtr()
	never(t, "answer with bit 30", 50*time.Millisecond, func() bool { return data() > 2 })
	if tp.Stats().RTRs != 2 {
		t.Errorf("RTRs %d", tp.Stats().RTRs)
	}
	// 253 with bit 30 clear again.
	if err := da.Write(CommIndex(true, 1), 1, le32(0x185)); err != nil {
		t.Fatal(err)
	}
	if err := da.Write(CommIndex(true, 1), 2, []byte{253}); err != nil {
		t.Fatal(err)
	}
	rtr()
	eventually(t, "PDO on RTR (253)", func() bool { return data() == 3 })
	// 252 without a sample yet sends the current values.
	if err := da.Write(CommIndex(true, 1), 2, []byte{252}); err != nil {
		t.Fatal(err)
	}
	rtr()
	eventually(t, "PDO on RTR (252)", func() bool { return data() == 4 })
}

func TestTPDOTransmit(t *testing.T) {
	a, b := testbus.Pair(t)
	da := device(t, 5)
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, da, 1)
	tp.SetEnabled(true)
	if err := tp.Transmit(ctx(t)); !errors.Is(err, ErrInactive) {
		t.Errorf("Transmit of an empty PDO: %v", err)
	}
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxRO, 0, 16}}})
	_ = da.SetValue(idxRO, 0, uint16(0x1234))
	if err := tp.Transmit(ctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "PDO", func() bool { return rec.n() == 1 })
	if !bytes.Equal(rec.last(), []byte{0x34, 0x12}) {
		t.Errorf("% X", rec.last())
	}
	// A read hook refusing the read: no PDO, error counted.
	da.OnRead(idxRO, 0, func(*od.Variable, []byte) ([]byte, error) { return nil, canopen.AbortHardware })
	if err := tp.Transmit(ctx(t)); !errors.Is(err, canopen.AbortHardware) {
		t.Errorf("Transmit with failing read: %v", err)
	}
	tp.OnSync(0)
	if tp.Stats().ReadErrors != 2 || tp.Stats().Sent != 1 {
		t.Errorf("stats %+v", tp.Stats())
	}
	if c := tp.Config(); c.COBID != 0x185 || len(c.Mapping) != 1 || tp.Number() != 1 {
		t.Errorf("config %+v", c)
	}
	if err := tp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tp.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
}

func TestTPDOSendError(t *testing.T) {
	a, _ := testbus.Pair(t)
	da := device(t, 5)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	tp, err := NewTPDO(a, da, 1)
	if err != nil {
		t.Fatal(err)
	}
	tp.SetEnabled(true)
	_ = a.Close()
	if err := tp.Transmit(ctx(t)); err == nil {
		t.Error("Transmit on a closed bus")
	}
	if tp.Stats().SendErrors != 1 {
		t.Errorf("stats %+v", tp.Stats())
	}
	if err := tp.Start(); err == nil {
		t.Error("Start on a closed bus")
	}
	_ = tp.Close()
}

func TestNewErrors(t *testing.T) {
	a, _ := testbus.Pair(t)
	if _, err := NewTPDO(a, device(t, 5), 9); err == nil {
		t.Error("TPDO 9")
	}
	if _, err := NewRPDO(a, device(t, 5), 0); err == nil {
		t.Error("RPDO 0")
	}
}

// TestBusClosed checks that the goroutines end when the bus closes.
func TestBusClosed(t *testing.T) {
	a, _ := testbus.Pair(t)
	da := device(t, 5)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	configure(t, da, false, 1, Config{COBID: 0x205, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	tp := newTPDO(t, a, da, 1)
	rp := newRPDO(t, a, da, 1)
	_ = a.Close()
	done := make(chan struct{})
	go func() { _ = tp.Close(); _ = rp.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hangs after the bus closed")
	}
}

func TestRPDOLengthAndDeadline(t *testing.T) {
	vb, ports := testbus.Virtual(t, 2)
	a, b := ports[0], ports[1]
	db := device(t, 6)
	configure(t, db, false, 1, Config{COBID: 0x206, TransmissionType: 255, EventTimer: 40, Mapping: Mapping{{idxU8, 0, 8}, {0x0006, 0, 16}, {idxI16, 0, 16}}})
	var mu sync.Mutex
	var evs []ErrorEvent
	var rx int
	rp := newRPDO(t, b, db, 1, WithLogger(slog.Default()), WithErrorHandler(func(e ErrorEvent) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	}), WithReceiveHandler(func([]byte) { mu.Lock(); rx++; mu.Unlock() }))
	events := func() []ErrorEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]ErrorEvent(nil), evs...)
	}
	send := func(data ...byte) {
		t.Helper()
		f, _ := canbus.NewStandard(0x206, data)
		if err := a.Send(ctx(t), &f); err != nil {
			t.Fatal(err)
		}
	}
	send(1, 0, 0, 2, 0) // disabled: ignored
	eventually(t, "ignored", func() bool { return rp.Stats().Ignored == 1 })
	rp.SetEnabled(true)
	send(7, 0xAA, 0xBB, 0x34, 0x12)
	eventually(t, "applied", func() bool { return u8(db, idxI16, 0) == 0x1234 && u8(db, idxU8, 0) == 7 })
	send(8, 0, 0)
	eventually(t, "short reported", func() bool { return len(events()) == 1 })
	if e := events()[0]; e.Code != EMCYPDOLength || !e.Active || e.PDO != 1 || e.Err == nil {
		t.Errorf("short: %+v", e)
	}
	send(8, 0, 0) // still short: not reported again
	send(9, 0, 0, 0x01, 0x00, 0xFF)
	eventually(t, "long reported", func() bool { return len(events()) == 3 })
	if e := events(); e[1].Code != EMCYPDOLength || e[1].Active || e[2].Code != EMCYPDOLengthExceeded || !e[2].Active {
		t.Errorf("long: %+v", e)
	}
	eventually(t, "long applied", func() bool { return u8(db, idxU8, 0) == 9 && u8(db, idxI16, 0) == 1 })
	send(10, 0, 0, 0, 0)
	eventually(t, "long resolved", func() bool { return len(events()) == 4 })
	if e := events()[3]; e.Code != EMCYPDOLengthExceeded || e.Active {
		t.Errorf("resolved: %+v", e)
	}
	// Deadline: drop everything.
	vb.SetFaults(virtual.Faults{Drop: 1})
	send(11, 0, 0, 0, 0)
	eventually(t, "deadline", func() bool { return len(events()) == 5 })
	if e := events()[4]; e.Code != EMCYRPDOTimeout || !e.Active {
		t.Errorf("deadline: %+v", e)
	}
	time.Sleep(100 * time.Millisecond)
	if len(events()) != 5 || rp.Stats().Timeouts != 1 {
		t.Errorf("deadline reported more than once: %+v", events())
	}
	vb.SetFaults(virtual.Faults{})
	send(12, 0, 0, 0, 0)
	eventually(t, "deadline resolved", func() bool { return len(events()) == 6 })
	if e := events()[5]; e.Code != EMCYRPDOTimeout || e.Active {
		t.Errorf("deadline resolved: %+v", e)
	}
	st := rp.Stats()
	if st.Short != 2 || st.Long != 1 || st.Applied < 4 {
		t.Errorf("stats %+v", st)
	}
	mu.Lock()
	if rx != int(st.Applied) {
		t.Errorf("receive handler called %d times, applied %d", rx, st.Applied)
	}
	mu.Unlock()
	rp.SetEnabled(false)
	if c := rp.Config(); c.EventTimer != 40 || rp.Number() != 1 {
		t.Errorf("config %+v", c)
	}
}

func TestRPDOSyncAndWriteErrors(t *testing.T) {
	a, b := testbus.Pair(t)
	db := device(t, 6)
	configure(t, db, false, 2, Config{COBID: 0x306, TransmissionType: 0, Mapping: Mapping{{idxBool, 0, 8}, {idxU8, 0, 8}}})
	rp := newRPDO(t, b, db, 2)
	rp.SetEnabled(true)
	send := func(data ...byte) {
		f, _ := canbus.NewStandard(0x306, data)
		_ = a.Send(ctx(t), &f)
	}
	send(1, 5)
	send(1, 6)
	eventually(t, "two received", func() bool { return rp.Stats().Received == 2 })
	if u8(db, idxU8, 0) != 0 {
		t.Fatal("synchronous RPDO applied before SYNC")
	}
	rp.OnSync(1)
	if u8(db, idxU8, 0) != 6 || rp.Stats().Overwritten != 1 {
		t.Errorf("after SYNC: %d %+v", u8(db, idxU8, 0), rp.Stats())
	}
	rp.OnSync(2) // nothing buffered
	// A BOOLEAN of 2 is refused by the dictionary, the other object is
	// still written.
	send(2, 7)
	eventually(t, "received", func() bool { return rp.Stats().Received == 3 })
	rp.OnSync(3)
	if u8(db, idxU8, 0) != 7 || rp.Stats().WriteErrors != 1 {
		t.Errorf("write error: %d %+v", u8(db, idxU8, 0), rp.Stats())
	}
	// A refusing write hook.
	db.OnWrite(idxU8, 0, func(*od.Variable, []byte) error { return canopen.AbortDeviceState })
	send(1, 8)
	eventually(t, "received", func() bool { return rp.Stats().Received == 4 })
	rp.OnSync(4)
	if rp.Stats().WriteErrors != 2 {
		t.Errorf("hook refusal: %+v", rp.Stats())
	}
	// Disable drops the buffer.
	send(1, 9)
	eventually(t, "received", func() bool { return rp.Stats().Received == 5 })
	rp.SetEnabled(false)
	rp.SetEnabled(true)
	rp.OnSync(5)
	if rp.Stats().Applied != 3 {
		t.Errorf("buffer survived disable: %+v", rp.Stats())
	}
}

// TestReconfigureWhileRunning moves the RPDO to another COB-ID.
func TestReconfigureWhileRunning(t *testing.T) {
	a, b := testbus.Pair(t)
	db := device(t, 6)
	rp := newRPDO(t, b, db, 1)
	rp.SetEnabled(true)
	for i, id := range []canopen.COBID{0x206, 0x222, 0x1ABCDE | canopen.COBExtended} {
		configure(t, db, false, 1, Config{COBID: id, TransmissionType: 255, Mapping: Mapping{{idxU8, 0, 8}}})
		f, _ := newFrame(id, []byte{byte(i + 1)})
		eventually(t, "applied on "+id.String(), func() bool {
			_ = a.Send(ctx(t), &f)
			time.Sleep(time.Millisecond)
			return u8(db, idxU8, 0) == uint64(i+1)
		})
	}
	// Invalidate: nothing more is received.
	if err := db.Write(CommIndex(false, 1), 1, le32(0x206|uint32(canopen.COBInvalid))); err != nil {
		t.Fatal(err)
	}
	if rp.Config().Active() {
		t.Error("still active")
	}
	// An inconsistent configuration set locally deactivates the PDO.
	_ = db.Set(MappingIndex(false, 1), 1, le32(Entry{idxNoMap, 0, 8}.Uint32()))
	_ = db.Set(CommIndex(false, 1), 1, le32(0x206))
	if rp.Config().COBID != 0x206 {
		t.Error("not reloaded")
	}
	f, _ := canbus.NewStandard(0x206, []byte{1})
	_ = a.Send(ctx(t), &f)
	never(t, "inconsistent RPDO received", 30*time.Millisecond, func() bool { return rp.Stats().Received > 3 })
}

// TestChaos runs an event-driven TPDO with an event timer over a lossy,
// duplicating bridge: the event timer repeats lost PDOs, duplicates are
// harmless, and the RPDO deadline fires when the link breaks.
func TestChaos(t *testing.T) {
	a, b, br := testbus.Chaos(t, chaos.Config{Seed: 7, Drop: 0.4, Duplicate: 0.3, Reorder: 0.1})
	da, db := device(t, 5), device(t, 6)
	m := Mapping{{idxU32, 0, 32}}
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 255, EventTimer: 10, Mapping: m})
	configure(t, db, false, 1, Config{COBID: 0x185, TransmissionType: 255, EventTimer: 500, Mapping: m})
	var mu sync.Mutex
	var evs []ErrorEvent
	tp := newTPDO(t, a, da, 1)
	rp := newRPDO(t, b, db, 1, WithErrorHandler(func(e ErrorEvent) { mu.Lock(); evs = append(evs, e); mu.Unlock() }))
	rp.SetEnabled(true)
	tp.SetEnabled(true)
	for i := uint32(1); i <= 20; i++ {
		_ = da.SetValue(idxU32, 0, i)
		time.Sleep(2 * time.Millisecond)
	}
	eventually(t, "last value through chaos", func() bool { return u8(db, idxU32, 0) == 20 })
	if br.Stats().Dropped == 0 || br.Stats().Duplicated == 0 {
		t.Errorf("chaos did nothing: %+v", br.Stats())
	}
	mu.Lock()
	n := len(evs)
	mu.Unlock()
	if n != 0 {
		t.Errorf("deadline fired with a 10 ms event timer: %+v", evs)
	}
	tp.SetEnabled(false) // the producer stops: the deadline fires once
	eventually(t, "deadline", func() bool { mu.Lock(); defer mu.Unlock(); return len(evs) == 1 })
	if evs[0].Code != EMCYRPDOTimeout {
		t.Errorf("%+v", evs)
	}
}
