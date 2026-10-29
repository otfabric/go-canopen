// SPDX-License-Identifier: MIT

package timestamp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestTimeOfDay(t *testing.T) {
	for _, tc := range []struct {
		t    time.Time
		want TimeOfDay
		enc  [Size]byte
	}{
		{Epoch, TimeOfDay{0, 0}, [Size]byte{}},
		{time.Date(1984, 1, 2, 0, 0, 1, 500_000_000, time.UTC), TimeOfDay{1500, 1}, [Size]byte{0xDC, 0x05, 0, 0, 1, 0}},
		{time.Date(2026, 10, 29, 23, 59, 59, 999_999_999, time.UTC), TimeOfDay{86_399_999, 15642}, [Size]byte{0xFF, 0x5B, 0x26, 0x05, 0x1A, 0x3D}},
		{Latest, TimeOfDay{86_399_999, 0xFFFF}, [Size]byte{0xFF, 0x5B, 0x26, 0x05, 0xFF, 0xFF}},
	} {
		got, err := FromTime(tc.t)
		if err != nil || got != tc.want {
			t.Errorf("%v: %+v %v", tc.t, got, err)
			continue
		}
		if got.Bytes() != tc.enc {
			t.Errorf("%v: % X", tc.t, got.Bytes())
		}
		back, err := ParseTimeOfDay(tc.enc[:])
		if err != nil || back != tc.want || !back.Valid() {
			t.Errorf("parse: %+v %v", back, err)
		}
		if !back.Time().Equal(tc.t.Truncate(time.Millisecond)) {
			t.Errorf("time %v", back.Time())
		}
		if back.String() == "" {
			t.Error("empty string")
		}
	}
	// Time zones: the instant counts, in UTC.
	cet := time.FixedZone("CET", 3600)
	got, _ := FromTime(time.Date(1984, 1, 2, 0, 30, 0, 0, cet))
	if got != (TimeOfDay{Ms: 23*3600_000 + 30*60_000, Days: 0}) {
		t.Errorf("zone: %+v", got)
	}
	for _, bad := range []time.Time{Epoch.Add(-time.Millisecond), Latest.Add(time.Millisecond)} {
		if _, err := FromTime(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// Reserved bits are ignored, a day or more is invalid, length is checked.
	if v, err := ParseTimeOfDay([]byte{0, 0, 0, 0xF0, 2, 0}); err != nil || v != (TimeOfDay{0, 2}) {
		t.Error(v, err)
	}
	for _, b := range [][]byte{{0x00, 0x5C, 0x26, 0x05, 0, 0}, {1, 2, 3, 4, 5}, {1, 2, 3, 4, 5, 6, 7}} {
		if _, err := ParseTimeOfDay(b); !errors.Is(err, canopen.ErrDataType) {
			t.Errorf("% X: %v", b, err)
		}
	}
	if (TimeOfDay{Ms: msPerDay}).Valid() {
		t.Error("valid")
	}
}

func TestTimeDifference(t *testing.T) {
	for _, d := range []time.Duration{0, time.Millisecond, 25 * time.Hour, MaxTimeDifference} {
		td, err := FromDuration(d)
		if err != nil {
			t.Fatal(err)
		}
		b := td.Bytes()
		back, err := ParseTimeDifference(b[:])
		if err != nil || back.Duration() != d || back.String() != d.String() {
			t.Errorf("%v: %+v %v", d, back, err)
		}
	}
	if td, _ := FromDuration(25*time.Hour + 1500*time.Microsecond); td != (TimeDifference{Ms: 3600_001, Days: 1}) {
		t.Errorf("%+v", td)
	}
	for _, d := range []time.Duration{-1, MaxTimeDifference + time.Millisecond} {
		if _, err := FromDuration(d); err == nil {
			t.Errorf("%v accepted", d)
		}
	}
	for _, b := range [][]byte{{0x00, 0x5C, 0x26, 0x05, 0, 0}, {1}} {
		if _, err := ParseTimeDifference(b); err == nil {
			t.Errorf("% X accepted", b)
		}
	}
}

func TestFrame(t *testing.T) {
	td := TimeOfDay{Ms: 1234, Days: 5}
	f := Frame(canopen.NewCOBID(0x100), td)
	if f.ID != 0x100 || f.Len != 6 || f.IsExtended() {
		t.Fatal(f)
	}
	if got, err := ParseFrame(&f); err != nil || got != td {
		t.Fatal(got, err)
	}
	f = Frame(canopen.NewCOBID(0x123456), td)
	if !f.IsExtended() {
		t.Fatal(f)
	}
	f.Flags |= canbus.FlagRemote
	if _, err := ParseFrame(&f); !errors.Is(err, canopen.ErrProtocol) {
		t.Error(err)
	}
	f, _ = canbus.NewStandard(0x100, []byte{1, 2})
	if _, err := ParseFrame(&f); !errors.Is(err, canopen.ErrProtocol) {
		t.Error(err)
	}
}

func TestProducerConsumer(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c, err := NewConsumer(b, WithHandler(ev.add), WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Last(); ok {
		t.Error("last before the first message")
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	p, err := NewProducer(a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 12, 0, 0, 123_456_789, time.UTC)
	if err := p.Send(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if !e.Time.Equal(now.Truncate(time.Millisecond)) || e.Received.IsZero() {
		t.Errorf("event %+v", e)
	}
	if last, ok := c.Last(); !ok || last.Time != e.Time {
		t.Error(last, ok)
	}
	if err := p.Send(context.Background(), Epoch.Add(-time.Hour)); err == nil {
		t.Error("time before the epoch sent")
	}
	f, _ := canbus.NewStandard(0x100, []byte{1})
	send(t, a, f)
	eventually(t, time.Second, func() bool { return c.Stats().Invalid == 1 })
	if st := p.Stats(); st.Sent != 1 {
		t.Error(st)
	}
	// Frames that do not match the COB-ID are not counted.
	g, _ := canbus.NewExtended(0x100, []byte{1})
	if _, ok := c.receive(&g); ok || c.Stats().Invalid != 1 {
		t.Error("extended frame matched")
	}
	if err := c.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
}

func TestPeriodic(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c, _ := NewConsumer(b, WithHandler(ev.add), WithCOBID(canopen.NewCOBID(0x101)))
	_ = c.Start()
	defer func() { _ = c.Close() }()
	fixed := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	p, _ := NewProducer(a, WithCOBID(canopen.NewCOBID(0x101)), WithPeriod(5*time.Millisecond), WithClock(func() time.Time { return fixed }))
	_ = p.Start()
	defer func() { _ = p.Close() }()
	for i := 0; i < 3; i++ {
		if e := ev.wait(t, time.Second, func(Event) bool { return true }); !e.Time.Equal(fixed) {
			t.Fatal(e)
		}
	}
	if _, err := NewProducer(a, WithPeriod(-1)); err == nil {
		t.Error("negative period accepted")
	}
	_ = p.Close()
	_ = p.Close()
	if err := p.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Error(err)
	}
	// A failing send is logged and counted.
	_ = a.Close()
	p2, _ := NewProducer(a, WithPeriod(time.Millisecond), WithClock(nil))
	_ = p2.Start()
	eventually(t, time.Second, func() bool { return p2.Stats().Errors > 0 })
	_ = p2.Close()
	if _, err := NewConsumer(a); err != nil {
		t.Fatal(err)
	}
	c2, _ := NewConsumer(a)
	if err := c2.Start(); err == nil {
		t.Error("Start on a closed bus")
	}
}

func TestDictionary(t *testing.T) {
	a, b := testbus.Pair(t)
	pd, _ := od.Standard(od.Config{NodeID: 1, Time: true})
	cd, _ := od.Standard(od.Config{NodeID: 2, Time: true})
	ev := newEvents[Event]()
	c, err := NewConsumer(b, WithDictionary(cd), WithHandler(ev.add))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Start()
	defer func() { _ = c.Close() }()
	p, err := NewProducer(a, WithDictionary(pd))
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Start()
	defer func() { _ = p.Close() }()
	ctx := context.Background()
	now := time.Now()
	// Default 0x1012 = 0x100: neither produce nor consume.
	if err := p.Send(ctx, now); !errors.Is(err, ErrNotProducer) {
		t.Fatal(err)
	}
	le := func(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }
	if err := pd.Write(od.IndexCOBIDTime, 0, le(0x100|uint32(canopen.COBGenerate))); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, now); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return c.Stats().Ignored == 1 })
	if err := cd.Write(od.IndexCOBIDTime, 0, le(0x100|uint32(canopen.COBConsume))); err != nil {
		t.Fatal(err)
	}
	_ = p.Send(ctx, now)
	ev.wait(t, time.Second, func(Event) bool { return true })
	// The CAN-ID cannot change while in use.
	if err := pd.Write(od.IndexCOBIDTime, 0, le(0x102|uint32(canopen.COBGenerate))); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("change in use: %v", err)
	}
	// Move both to 0x102.
	_ = pd.Write(od.IndexCOBIDTime, 0, le(0x102))
	_ = pd.Write(od.IndexCOBIDTime, 0, le(0x102|uint32(canopen.COBGenerate)))
	_ = cd.Write(od.IndexCOBIDTime, 0, le(0x102))
	_ = cd.Write(od.IndexCOBIDTime, 0, le(0x102|uint32(canopen.COBConsume)))
	_ = cd.Write(od.IndexCOBIDTime, 0, le(0x102|uint32(canopen.COBConsume))) // unchanged: no re-subscription
	time.Sleep(10 * time.Millisecond)
	_ = p.Send(ctx, now)
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if !e.Time.Equal(now.UTC().Truncate(time.Millisecond)) {
		t.Errorf("event %+v", e)
	}
	// Explicit COB-ID with a dictionary keeps its flags.
	p3, _ := NewProducer(a, WithDictionary(pd), WithCOBID(canopen.NewCOBID(0x100)))
	if err := p3.Send(ctx, now); !errors.Is(err, ErrNotProducer) {
		t.Error(err)
	}
	c3, _ := NewConsumer(b, WithDictionary(cd), WithCOBID(canopen.NewCOBID(0x100)))
	f := Frame(canopen.NewCOBID(0x100), TimeOfDay{})
	if _, ok := c3.receive(&f); ok || c3.Stats().Ignored != 1 {
		t.Error("consumed without bit 31")
	}
	if _, err := NewProducer(a, WithDictionary(od.New(1))); err == nil {
		t.Error("dictionary without 0x1012 accepted")
	}
	if _, err := NewConsumer(a, WithDictionary(od.New(1))); err == nil {
		t.Error("dictionary without 0x1012 accepted")
	}
}
