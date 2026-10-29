// SPDX-License-Identifier: MIT

package mpdo

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/chaos"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

func newProducer(t testing.TB, bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) *Producer {
	t.Helper()
	p, err := NewProducer(bus, d, n, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.SetEnabled(true)
	return p
}

func newConsumer(t testing.TB, bus canopen.Bus, d *od.Dictionary, n int, opts ...Option) *Consumer {
	t.Helper()
	c, err := NewConsumer(bus, d, n, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.SetEnabled(true)
	return c
}

// recorder collects the events of a consumer.
type recorder struct {
	mu   sync.Mutex
	rx   []Event
	errs []pdo.ErrorEvent
}

func (r *recorder) opts() []Option {
	return []Option{
		WithReceiveHandler(func(e Event) { r.mu.Lock(); r.rx = append(r.rx, e); r.mu.Unlock() }),
		WithErrorHandler(func(e pdo.ErrorEvent) { r.mu.Lock(); r.errs = append(r.errs, e); r.mu.Unlock() }),
	}
}

func (r *recorder) errors() []pdo.ErrorEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pdo.ErrorEvent(nil), r.errs...)
}

func (r *recorder) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.rx...)
}

func TestDAM(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	da, db, dc := device(t, 5), device(t, 6), device(t, 7)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	setMPDO(t, dc, false, 1, 0x281, ModeDAM)
	p := newProducer(t, buses[0], da, 1, WithLogger(slog.Default()))
	var rb recorder
	cb := newConsumer(t, buses[1], db, 1, rb.opts()...)
	cc := newConsumer(t, buses[2], dc, 1)
	if p.Mode() != ModeDAM || cb.Mode() != ModeDAM || p.Number() != 1 || cb.Number() != 1 {
		t.Fatalf("modes %v %v", p.Mode(), cb.Mode())
	}

	// To node 6 only.
	if err := p.SendDAM(ctx(t), 6, idxU32, 0, []byte{0x78, 0x56, 0x34, 0x12}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "node 6 written", func() bool { return getU(db, idxU32, 0) == 0x12345678 })
	eventually(t, "node 7 ignores", func() bool { return cc.Stats().OtherNode == 1 })
	if getU(dc, idxU32, 0) != 0 {
		t.Error("node 7 written")
	}
	ev := rb.events()
	if len(ev) != 1 || ev[0].PDO != 1 || ev[0].Index != idxU32 || !ev[0].Message.DAM || ev[0].Message.Node != 6 || len(ev[0].Data) != 4 {
		t.Errorf("events %+v", ev)
	}

	// Broadcast: a 2-byte value padded with zeros, written with the
	// object's size.
	if err := p.SendDAM(ctx(t), 0, idxArr, 3, []byte{0x34, 0x12}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*od.Dictionary{db, dc} {
		eventually(t, "broadcast", func() bool { return getU(d, idxArr, 3) == 0x1234 })
	}
	// Boolean: one byte.
	if err := p.SendDAM(ctx(t), 0, idxBool, 0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "boolean", func() bool { return getU(dc, idxBool, 0) == 1 })
	if st := p.Stats(); st.Sent != 3 || st.SendErrors != 0 {
		t.Errorf("producer %+v", st)
	}
	if st := cb.Stats(); st.Received != 3 || st.Applied != 3 {
		t.Errorf("consumer %+v", st)
	}
	if len(rb.errors()) != 0 {
		t.Errorf("errors %+v", rb.errors())
	}
}

func TestDAMRejected(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b := testbus.Pair(t)
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	defer db.OnWrite(idxU8, 0, func(_ *od.Variable, data []byte) error {
		if data[0] == 0xEE {
			return canopen.AbortInvalidValue
		}
		return nil
	})()
	p := newProducer(t, a, da, 1)
	var rec recorder
	c := newConsumer(t, b, db, 1, rec.opts()...)

	tests := []struct {
		name  string
		index uint16
		sub   uint8
		value []byte
		stat  func(ConsumerStats) uint64
	}{
		{"missing object", 0x5000, 0, []byte{1}, func(s ConsumerStats) uint64 { return s.NoObject }},
		{"missing sub-index", idxArr, 9, []byte{1}, func(s ConsumerStats) uint64 { return s.NoObject }},
		{"not mappable", idxNoMap, 0, []byte{1}, func(s ConsumerStats) uint64 { return s.NotMappable }},
		{"read-only", idxRO, 0, []byte{1}, func(s ConsumerStats) uint64 { return s.NotMappable }},
		{"above limit", idxU16, 0, []byte{0xE9, 0x03}, func(s ConsumerStats) uint64 { return s.WriteErrors }},
		{"write hook", idxU8, 0, []byte{0xEE}, func(s ConsumerStats) uint64 { return s.WriteErrors }},
		{"8-byte object", idxU64, 0, []byte{1}, func(s ConsumerStats) uint64 { return s.WriteErrors }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.stat(c.Stats())
			if err := p.SendDAM(ctx(t), 6, tt.index, tt.sub, tt.value); err != nil {
				t.Fatal(err)
			}
			eventually(t, "counted", func() bool { return tt.stat(c.Stats()) == before+1 })
		})
	}
	if c.Stats().Applied != 0 {
		t.Errorf("applied %+v", c.Stats())
	}
	// 0x8230 was raised once, for the first missing object.
	errs := rec.errors()
	if len(errs) != 1 || errs[0].Code != EMCYDAMNoObject || !errs[0].Active || errs[0].PDO != 1 || errs[0].Err == nil {
		t.Fatalf("errors %+v", errs)
	}
	// A variable-length object takes the 4 bytes; the next written DAM
	// resolves 0x8230.
	if err := p.SendDAM(ctx(t), 6, idxStr, 0, []byte("wxyz")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "resolved", func() bool { return len(rec.errors()) == 2 })
	if e := rec.errors()[1]; e.Code != EMCYDAMNoObject || e.Active {
		t.Errorf("resolution %+v", e)
	}
	if s, _ := db.GetValue(idxStr, 0); s != "wxyz" {
		t.Errorf("string %v", s)
	}
}

func TestSAM(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	da, db, dc := device(t, 5), device(t, 6), device(t, 7)
	sl, err := NewScannerList(1, 8,
		ScannerEntry{Index: idxU8},
		ScannerEntry{Index: idxArr, SubIndex: 1, BlockSize: 4},
		ScannerEntry{Index: idxRO},
		ScannerEntry{Index: idxNoMap},
		ScannerEntry{Index: idxU64},
		ScannerEntry{Index: 0x5000},
	)
	addObj(t, da, sl, err)
	dl, err := NewDispatcherList(1, 4,
		DispatcherEntry{Producer: 5, ProducerIndex: idxU8, Index: idxU8},
		DispatcherEntry{Producer: 5, ProducerIndex: idxArr, ProducerSubIndex: 1, Index: idxArr, SubIndex: 5, BlockSize: 4},
		DispatcherEntry{Producer: 5, ProducerIndex: idxRO, Index: idxU16},
	)
	addObj(t, db, dl, err)
	setMPDO(t, da, true, 2, 0x382, ModeSAM)
	setMPDO(t, db, false, 2, 0x382, ModeSAM)
	setMPDO(t, dc, false, 2, 0x382, ModeSAM)
	defer da.OnRead(idxRO, 0, func(_ *od.Variable, _ []byte) ([]byte, error) { return []byte{0x2A, 0x00}, nil })()

	p := newProducer(t, buses[0], da, 2)
	var rec recorder
	cb := newConsumer(t, buses[1], db, 2, rec.opts()...)
	cc := newConsumer(t, buses[2], dc, 2)

	if err := da.SetUint(idxU8, 0, 0x11); err != nil {
		t.Fatal(err)
	}
	for s := uint8(1); s <= 4; s++ {
		if err := da.SetUint(idxArr, s, uint64(0x100*uint16(s)+uint16(s))); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Send(ctx(t), idxU8, 0); err != nil {
		t.Fatal(err)
	}
	for s := uint8(1); s <= 4; s++ {
		if err := p.Send(ctx(t), idxArr, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Send(ctx(t), idxRO, 0); err != nil { // read hook value 42
		t.Fatal(err)
	}
	eventually(t, "SAM applied", func() bool { return cb.Stats().Applied == 6 })
	if getU(db, idxU8, 0) != 0x11 || getU(db, idxU16, 0) != 42 {
		t.Errorf("u8 %X u16 %d", getU(db, idxU8, 0), getU(db, idxU16, 0))
	}
	for s := uint8(1); s <= 4; s++ {
		if got := getU(db, idxArr, s+4); got != uint64(0x100*uint16(s)+uint16(s)) {
			t.Errorf("arr[%d] = %X", s+4, got)
		}
		if getU(db, idxArr, s) != 0 {
			t.Errorf("arr[%d] written", s)
		}
	}
	ev := rec.events()
	if len(ev) != 6 || ev[1].Message.Index != idxArr || ev[1].Message.SubIndex != 1 || ev[1].SubIndex != 5 || ev[1].Message.Node != 5 || ev[1].Message.DAM {
		t.Errorf("events %+v", ev)
	}
	eventually(t, "node 7 has no dispatcher", func() bool { return cc.Stats().NotDispatched == 6 })

	// Producer refusals.
	for _, tc := range []struct {
		name  string
		index uint16
		sub   uint8
		want  error
	}{
		{"not scanned", idxU32, 0, ErrNotScanned},
		{"block end", idxArr, 5, canopen.AbortNotMappable},
		{"not mappable", idxNoMap, 0, canopen.AbortNotMappable},
		{"too long", idxU64, 0, canopen.AbortPDOLength},
		{"missing", 0x5000, 0, canopen.AbortNoObject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.Send(ctx(t), tc.index, tc.sub); !errors.Is(err, tc.want) {
				t.Errorf("Send = %v, want %v", err, tc.want)
			}
		})
	}
	if st := p.Stats(); st.Refused != 5 || st.Sent != 6 {
		t.Errorf("producer %+v", st)
	}
	// A SAM producer needs a valid node-ID.
	da.SetNodeID(0)
	if err := p.Send(ctx(t), idxU8, 0); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Errorf("node-ID 0: %v", err)
	}
}

func TestWrongMode(t *testing.T) {
	nettest.CheckLeaks(t)
	buses := testbus.Ports(t, 3)
	ds, dd, db, dc := device(t, 5), device(t, 9), device(t, 6), device(t, 7)
	setMPDO(t, ds, true, 1, 0x281, ModeSAM)
	setMPDO(t, dd, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	setMPDO(t, dc, false, 1, 0x281, ModeSAM)
	sl, err := NewScannerList(1, 1, ScannerEntry{Index: idxU8})
	addObj(t, ds, sl, err)
	ps := newProducer(t, buses[0], ds, 1)
	pd := newProducer(t, buses[0], dd, 1)
	cb := newConsumer(t, buses[1], db, 1)
	cc := newConsumer(t, buses[2], dc, 1)
	if err := ps.SendDAM(ctx(t), 6, idxU8, 0, nil); !errors.Is(err, ErrMode) {
		t.Errorf("SendDAM on SAM: %v", err)
	}
	if err := pd.Send(ctx(t), idxU8, 0); !errors.Is(err, ErrMode) {
		t.Errorf("Send on DAM: %v", err)
	}
	if err := ps.Send(ctx(t), idxU8, 0); err != nil {
		t.Fatal(err)
	}
	if err := pd.SendDAM(ctx(t), 6, idxU8, 0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "wrong mode counted", func() bool { return cb.Stats().WrongMode == 1 && cc.Stats().WrongMode == 1 })
}

func TestProducerErrors(t *testing.T) {
	nettest.CheckLeaks(t)
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	setMPDO(t, d, true, 1, 0x281, ModeDAM)
	if _, err := NewProducer(nil, d, 1); err == nil {
		t.Error("nil bus accepted")
	}
	if _, err := NewProducer(a, d, 0); !errors.Is(err, ErrNumber) {
		t.Errorf("number 0: %v", err)
	}
	if _, err := NewProducer(a, d, 3); !errors.Is(err, ErrNumber) {
		t.Errorf("missing TPDO: %v", err)
	}
	hooks := d.HookCount()
	p, err := NewProducer(a, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := ctx(t)
	if err := p.SendDAM(c, 6, idxU8, 0, nil); !errors.Is(err, ErrNotStarted) {
		t.Errorf("before Start: %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("second Start: %v", err)
	}
	if err := p.SendDAM(c, 6, idxU8, 0, nil); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled: %v", err)
	}
	p.SetEnabled(true)
	if err := p.SendDAM(c, 128, idxU8, 0, nil); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Errorf("dest 128: %v", err)
	}
	if err := p.SendDAM(c, 6, idxU8, 0, make([]byte, 5)); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("5 bytes: %v", err)
	}
	for _, edit := range []struct {
		name  string
		index uint16
		sub   uint8
		x     uint64
	}{
		{"COB-ID invalid", 0x1800, 1, uint64(canopen.COBInvalid | 0x281)},
		{"synchronous", 0x1800, 2, 1},
		{"not an MPDO", 0x1A00, 0, 0},
	} {
		t.Run(edit.name, func(t *testing.T) {
			setMPDO(t, d, true, 1, 0x281, ModeDAM)
			if err := d.SetUint(edit.index, edit.sub, edit.x); err != nil {
				t.Fatal(err)
			}
			if err := p.SendDAM(c, 6, idxU8, 0, nil); !errors.Is(err, ErrInactive) {
				t.Errorf("SendDAM = %v", err)
			}
		})
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if d.HookCount() != hooks {
		t.Errorf("hooks %d -> %d", hooks, d.HookCount())
	}
	if err := p.SendDAM(c, 6, idxU8, 0, nil); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("after Close: %v", err)
	}
	if err := p.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
}

// failBus refuses every transmission.
type failBus struct{ canopen.Bus }

func (failBus) Send(context.Context, *canbus.Frame) error { return errors.New("bus off") }

func TestSendError(t *testing.T) {
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	setMPDO(t, d, true, 1, canopen.COBID(0x1234567)|canopen.COBExtended, ModeDAM)
	p := newProducer(t, failBus{a}, d, 1)
	if err := p.SendDAM(ctx(t), 0, idxU8, 0, nil); err == nil {
		t.Fatal("send error not reported")
	}
	if p.Stats().SendErrors != 1 {
		t.Errorf("%+v", p.Stats())
	}
}

func TestExtendedCOBID(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b := testbus.Pair(t)
	da, db := device(t, 5), device(t, 6)
	cob := canopen.COBID(0x1234567) | canopen.COBExtended
	setMPDO(t, da, true, 1, cob, ModeDAM)
	setMPDO(t, db, false, 1, cob, ModeDAM)
	p := newProducer(t, a, da, 1)
	c := newConsumer(t, b, db, 1)
	if err := p.SendDAM(ctx(t), 6, idxU8, 0, []byte{9}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "29-bit MPDO", func() bool { return getU(db, idxU8, 0) == 9 })
	// An 11-bit frame with the same identifier bits is not for it.
	f, _ := canbus.NewStandard(0x567, []byte{0x86, 0, 0x20, 0, 7, 0, 0, 0})
	_ = a.Send(ctx(t), &f)
	never(t, "11-bit frame used", 30*time.Millisecond, func() bool { return c.Stats().Received > 1 })
}

func TestInhibitTime(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b := testbus.Pair(t)
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	if err := da.SetUint(0x1800, 3, 300); err != nil { // 30 ms
		t.Fatal(err)
	}
	p := newProducer(t, a, da, 1)
	c := newConsumer(t, b, db, 1)
	start := time.Now()
	for i := byte(1); i <= 3; i++ {
		if err := p.SendDAM(ctx(t), 6, idxU8, 0, []byte{i}); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 55*time.Millisecond {
		t.Errorf("3 MPDOs with a 30 ms inhibit time in %v", el)
	}
	if p.Stats().Inhibited != 2 {
		t.Errorf("%+v", p.Stats())
	}
	eventually(t, "values", func() bool { return c.Stats().Applied == 3 })
	// A context that ends during the wait.
	cx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := p.SendDAM(cx, 6, idxU8, 0, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancelled wait: %v", err)
	}
}

func TestConsumerLifecycle(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b := testbus.Pair(t)
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeNone) // not yet an MPDO
	if _, err := NewConsumer(b, nil, 1); err == nil {
		t.Error("nil dictionary accepted")
	}
	if _, err := NewConsumer(b, db, 513); !errors.Is(err, ErrNumber) {
		t.Errorf("513: %v", err)
	}
	hooks := db.HookCount()
	p := newProducer(t, a, da, 1)
	c, err := NewConsumer(b, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("second Start: %v", err)
	}
	send := func(v byte) {
		t.Helper()
		if err := p.SendDAM(ctx(t), 6, idxU8, 0, []byte{v}); err != nil {
			t.Fatal(err)
		}
	}
	// Inactive: no subscription, nothing counted.
	send(1)
	never(t, "inactive consumer received", 30*time.Millisecond, func() bool { return c.Stats() != ConsumerStats{} })
	// Becomes an MPDO: subscribes; disabled: ignored.
	if err := db.SetUint(0x1600, 0, uint64(MappingDAM)); err != nil {
		t.Fatal(err)
	}
	if c.Mode() != ModeDAM {
		t.Fatalf("mode %v", c.Mode())
	}
	send(2)
	eventually(t, "ignored while disabled", func() bool { return c.Stats().Ignored == 1 })
	c.SetEnabled(true)
	send(3)
	eventually(t, "written", func() bool { return getU(db, idxU8, 0) == 3 })
	// A new COB-ID: resubscribes.
	setMPDO(t, da, true, 1, 0x381, ModeDAM)
	if err := db.SetUint(0x1400, 1, 0x381); err != nil {
		t.Fatal(err)
	}
	send(4)
	eventually(t, "new COB-ID", func() bool { return getU(db, idxU8, 0) == 4 })
	// Another object changing does not disturb it.
	if err := db.SetUint(idxU32, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
	if db.HookCount() != hooks {
		t.Errorf("hooks %d -> %d", hooks, db.HookCount())
	}
	// Changes after Close are ignored.
	if err := db.SetUint(0x1400, 1, 0x481); err != nil {
		t.Fatal(err)
	}
}

// subFailBus refuses subscriptions.
type subFailBus struct{ canopen.Bus }

func (subFailBus) Subscribe(...canbus.SubscribeOption) (*canbus.Subscription, error) {
	return nil, errors.New("no filters left")
}

func TestSubscribeFailure(t *testing.T) {
	nettest.CheckLeaks(t)
	_, b := testbus.Pair(t)
	d := device(t, 6)
	setMPDO(t, d, false, 1, 0x281, ModeDAM)
	c, err := NewConsumer(subFailBus{b}, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Start(); err == nil {
		t.Fatal("Start without subscription")
	}
	// Inactive at Start, then the resubscription fails: logged.
	setMPDO(t, d, false, 2, 0x282, ModeNone)
	c2, err := NewConsumer(subFailBus{b}, d, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUint(0x1601, 0, uint64(MappingDAM)); err != nil {
		t.Fatal(err)
	}
}

// TestSubscriptionEnds closes the bus under a running consumer: its
// goroutine ends.
func TestSubscriptionEnds(t *testing.T) {
	nettest.CheckLeaks(t)
	vb, ports := testbus.Virtual(t, 1)
	_ = vb
	d := device(t, 6)
	setMPDO(t, d, false, 1, 0x281, ModeDAM)
	c, err := NewConsumer(ports[0], d, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	_ = ports[0].Close()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine did not end with the subscription")
	}
	_ = c.Close()
}

func TestLengthError(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b := testbus.Pair(t)
	d := device(t, 6)
	setMPDO(t, d, false, 1, 0x281, ModeDAM)
	var rec recorder
	c := newConsumer(t, b, d, 1, rec.opts()...)
	send := func(data []byte) {
		t.Helper()
		f, _ := canbus.NewStandard(0x281, data)
		if err := a.Send(ctx(t), &f); err != nil {
			t.Fatal(err)
		}
	}
	send([]byte{0x86, 0, 0x20, 0})
	send([]byte{0x86, 0, 0x20})
	eventually(t, "short", func() bool { return c.Stats().Short == 2 })
	send([]byte{0x86, 0, 0x20, 0, 5, 0, 0, 0})
	eventually(t, "written", func() bool { return getU(d, idxU8, 0) == 5 })
	errs := rec.errors()
	if len(errs) != 2 || errs[0].Code != pdo.EMCYPDOLength || !errs[0].Active || errs[1].Active {
		t.Errorf("errors %+v", errs)
	}
	// Remote frames are not MPDOs.
	r, _ := canbus.NewRemote(0x281, false, 8)
	_ = a.Send(ctx(t), &r)
	never(t, "RTR received", 30*time.Millisecond, func() bool { return c.Stats().Received > 3 })
}

func TestProtectPDO(t *testing.T) {
	d := device(t, 5)
	if _, err := ProtectPDO(d, true, 9); !errors.Is(err, ErrNumber) {
		t.Errorf("missing PDO: %v", err)
	}
	unprotect, err := ProtectPDO(d, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer unprotect()
	type step struct {
		index uint16
		sub   uint8
		x     uint64
		size  int
		want  error
	}
	valid, invalid := uint64(0x281), uint64(canopen.COBInvalid|0x281)
	tests := []struct {
		name  string
		setup func()
		step  step
	}{
		{"SAM while invalid", func() { setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeNone) }, step{0x1A00, 0, 0xFE, 1, nil}},
		{"DAM while valid", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeNone) }, step{0x1A00, 0, 0xFF, 1, canopen.AbortUnsupported}},
		{"same mode while valid", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeDAM) }, step{0x1A00, 0, 0xFF, 1, nil}},
		{"leave MPDO while valid", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeDAM) }, step{0x1A00, 0, 0, 1, canopen.AbortUnsupported}},
		{"leave MPDO while invalid", func() { setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeDAM) }, step{0x1A00, 0, 0, 1, nil}},
		{"MPDO while synchronous", func() {
			setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeNone)
			_ = d.SetUint(0x1800, 2, 1)
		}, step{0x1A00, 0, 0xFE, 1, canopen.AbortInvalidValue}},
		{"entry of an MPDO", func() { setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeSAM) }, step{0x1A00, 1, 0x20000008, 4, canopen.AbortUnsupported}},
		{"entry of a PDO", func() { setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeNone) }, step{0x1A00, 1, 0x20000008, 4, nil}},
		{"regular count", func() { setMPDO(t, d, true, 1, canopen.COBID(invalid), ModeNone) }, step{0x1A00, 0, 1, 1, nil}},
		{"synchronous MPDO", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeSAM) }, step{0x1800, 2, 0, 1, canopen.AbortInvalidValue}},
		{"type 255 MPDO", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeSAM) }, step{0x1800, 2, 255, 1, nil}},
		{"synchronous PDO", func() { setMPDO(t, d, true, 1, canopen.COBID(valid), ModeNone) }, step{0x1800, 2, 1, 1, nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = d.SetUint(0x1A00, 1, 0)
			tt.setup()
			s := tt.step
			err := d.Write(s.index, s.sub, le(s.x, s.size))
			if !errors.Is(err, s.want) {
				t.Errorf("Write = %v, want %v", err, s.want)
			}
		})
	}
	unprotect()
}

// TestChaos sends DAM- and SAM-MPDOs over a lossy, duplicating, reordering
// bridge: duplicates are harmless and the producer repeats the last value
// until it arrives.
func TestChaos(t *testing.T) {
	nettest.CheckLeaks(t)
	a, b, br := testbus.Chaos(t, chaos.Config{Seed: 11, Drop: 0.3, Duplicate: 0.3, Reorder: 0.2})
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	setMPDO(t, da, true, 2, 0x382, ModeSAM)
	setMPDO(t, db, false, 2, 0x382, ModeSAM)
	sl, err := NewScannerList(1, 1, ScannerEntry{Index: idxU32})
	addObj(t, da, sl, err)
	dl, err := NewDispatcherList(1, 1, DispatcherEntry{Producer: 5, ProducerIndex: idxU32, Index: idxU16})
	addObj(t, db, dl, err)
	pd := newProducer(t, a, da, 1)
	ps := newProducer(t, a, da, 2)
	var rec recorder
	cd := newConsumer(t, b, db, 1, rec.opts()...)
	cs := newConsumer(t, b, db, 2)
	for i := uint16(1); i <= 30; i++ {
		if err := pd.SendDAM(ctx(t), 6, idxArr, uint8(1+i%8), le(uint64(i), 2)); err != nil {
			t.Fatal(err)
		}
		if err := da.SetUint(idxU32, 0, uint64(i)); err != nil {
			t.Fatal(err)
		}
		if err := ps.Send(ctx(t), idxU32, 0); err != nil {
			t.Fatal(err)
		}
	}
	final := func() {
		_ = pd.SendDAM(ctx(t), 6, idxU8, 0, []byte{0xAB})
		_ = ps.Send(ctx(t), idxU32, 0)
	}
	deadline := time.Now().Add(5 * time.Second)
	for getU(db, idxU8, 0) != 0xAB || getU(db, idxU16, 0) != 30 {
		if time.Now().After(deadline) {
			t.Fatalf("values not through: u8 %X u16 %d", getU(db, idxU8, 0), getU(db, idxU16, 0))
		}
		final()
		time.Sleep(5 * time.Millisecond)
	}
	if st := br.Stats(); st.Dropped == 0 || st.Duplicated == 0 {
		t.Errorf("chaos did nothing: %+v", st)
	}
	if st := cd.Stats(); st.WriteErrors+st.NoObject+st.Short != 0 || len(rec.errors()) != 0 {
		t.Errorf("DAM consumer %+v %+v", st, rec.errors())
	}
	if st := cs.Stats(); st.Applied == 0 || st.NotDispatched != 0 {
		t.Errorf("SAM consumer %+v", st)
	}
}

// FuzzReceive feeds arbitrary frames to a consumer of each mode.
func FuzzReceive(f *testing.F) {
	f.Add(uint32(0x281), uint8(0), []byte{0x86, 0, 0x20, 0, 1, 2, 3, 4}, true)
	f.Add(uint32(0x281), uint8(0), []byte{0x80, 0, 0x50, 0, 1, 2, 3, 4}, true)
	f.Add(uint32(0x281), uint8(0), []byte{0x05, 0x03, 0x20, 1, 1, 2, 3, 4}, false)
	f.Add(uint32(0x281), uint8(0), []byte{0x05}, false)
	f.Add(uint32(0x281), uint8(canbus.FlagRemote), []byte{}, true)
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte, dam bool) {
		d := device(t, 6)
		dl, err := NewDispatcherList(1, 2,
			DispatcherEntry{Producer: 5, ProducerIndex: idxArr, ProducerSubIndex: 1, Index: idxArr, SubIndex: 1, BlockSize: 8})
		addObj(t, d, dl, err)
		mode := ModeSAM
		if dam {
			mode = ModeDAM
		}
		setMPDO(t, d, false, 1, 0x281, mode)
		var ev []pdo.ErrorEvent
		c, err := NewConsumer(nopBus{}, d, 1, WithErrorHandler(func(e pdo.ErrorEvent) { ev = append(ev, e) }))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetEnabled(true)
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		for i := 0; i < 2; i++ {
			c.receive(&fr)
			c.report()
		}
		st := c.Stats()
		if st.Applied > st.Received || len(ev) > 2 {
			t.Fatalf("%+v %+v", st, ev)
		}
	})
}

// nopBus is a bus that is never used.
type nopBus struct{}

func (nopBus) Send(context.Context, *canbus.Frame) error { return nil }
func (nopBus) Subscribe(...canbus.SubscribeOption) (*canbus.Subscription, error) {
	return nil, errors.New("nop")
}
