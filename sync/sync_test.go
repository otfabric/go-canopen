// SPDX-License-Identifier: MIT

package syncobj

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

func le32(x uint32) []byte { return []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)} }

func TestProducerCounter(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	p, err := NewProducer(a, WithPeriod(5*time.Millisecond), WithOverflow(3), WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Producing() || p.Period() != 5*time.Millisecond {
		t.Fatal(p.Producing(), p.Period())
	}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, want := range []byte{1, 2, 3, 1, 2} {
		f := recv(t, sub, nil)
		if f.ID != 0x80 || f.Len != 1 || f.Data[0] != want {
			t.Fatalf("frame %v, want counter %d", f, want)
		}
	}
	if err := p.SetOverflow(5); !errors.Is(err, canopen.AbortDeviceState) {
		t.Error(err)
	}
	p.SetPeriod(0)
	time.Sleep(10 * time.Millisecond)
	if err := p.SetOverflow(1); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Error(err)
	}
	if err := p.SetOverflow(0); err != nil {
		t.Fatal(err)
	}
	n := p.Stats().Sent
	for {
		f, err := sub.Receive(ctxShort(t))
		if err != nil {
			break
		}
		_ = f
	}
	if p.Stats().Sent != n {
		t.Error("SYNC with period 0")
	}
	// Without counter: no data byte.
	if err := p.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f := recv(t, sub, nil); f.Len != 0 {
		t.Errorf("frame %v", f)
	}
	p.SetPeriod(5 * time.Millisecond)
	recv(t, sub, nil)
	if err := p.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
	for _, opts := range [][]Option{{WithPeriod(-1)}, {WithOverflow(1)}, {WithOverflow(241)}} {
		if _, err := NewProducer(a, opts...); err == nil {
			t.Error("invalid configuration accepted")
		}
	}
}

func ctxShort(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestProducerConsumer(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c, err := NewConsumer(b, WithHandler(ev.add), WithWindow(time.Hour), WithOverflow(10))
	if err != nil {
		t.Fatal(err)
	}
	if c.InWindow(time.Now()) {
		t.Error("in window before the first SYNC")
	}
	if _, ok := c.Last(); ok {
		t.Error("last before the first SYNC")
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	p, _ := NewProducer(a, WithOverflow(10))
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		_ = p.Send(ctx)
		e := ev.wait(t, time.Second, func(Event) bool { return true })
		if int(e.Counter) != i || e.LengthError || e.WindowEnd.Sub(e.Time) != time.Hour {
			t.Errorf("event %+v", e)
		}
	}
	if !c.InWindow(time.Now()) || c.InWindow(time.Now().Add(2*time.Hour)) {
		t.Error("window")
	}
	if e, ok := c.Last(); !ok || e.Counter != 3 {
		t.Error(e, ok)
	}
	// A lost SYNC and a SYNC of the wrong length.
	_ = p.Send(ctx) // 4
	f, _ := canbus.NewStandard(0x80, []byte{9})
	send(t, a, f)
	f, _ = canbus.NewStandard(0x80, nil)
	send(t, a, f)
	e := ev.wait(t, time.Second, func(e Event) bool { return e.Counter == 0 })
	if !e.LengthError {
		t.Errorf("event %+v", e)
	}
	if st := c.Stats(); st.Received != 6 || st.LengthErrors != 1 || st.CounterErrors != 1 {
		t.Errorf("stats %+v", st)
	}
	// Remote frames on the identifier are no SYNC.
	r, _ := canbus.NewRemote(0x80, false, 0)
	send(t, a, r)
	_ = p.Send(ctx)
	ev.wait(t, time.Second, func(Event) bool { return true })
	if c.Stats().Received != 7 {
		t.Error(c.Stats())
	}
	if err := c.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
}

func TestNoWindow(t *testing.T) {
	a, b := testbus.Pair(t)
	ev := newEvents[Event]()
	c, _ := NewConsumer(b, WithHandler(ev.add), WithCOBID(canopen.NewCOBID(0x1ABCDE)))
	_ = c.Start()
	defer func() { _ = c.Close() }()
	p, _ := NewProducer(a, WithCOBID(canopen.NewCOBID(0x1ABCDE)))
	_ = p.Send(context.Background())
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if !e.WindowEnd.IsZero() || e.Counter != 0 || e.LengthError {
		t.Errorf("event %+v", e)
	}
	if !c.InWindow(time.Now().Add(time.Hour)) {
		t.Error("no window: always inside")
	}
}

func TestDictionary(t *testing.T) {
	a, b := testbus.Pair(t)
	pd, _ := od.Standard(od.Config{NodeID: 1})
	cd, _ := od.Standard(od.Config{NodeID: 2})
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
	if p.Producing() {
		t.Fatal("producing without bit 30")
	}
	// Configure the producer by SDO-style writes.
	for _, w := range []struct {
		index uint16
		data  []byte
		want  error
	}{
		{od.IndexSyncOverflow, []byte{1}, canopen.AbortInvalidValue},
		{od.IndexSyncOverflow, []byte{241}, canopen.AbortInvalidValue},
		{od.IndexSyncOverflow, []byte{4}, nil},
		{od.IndexCyclePeriod, le32(5000), nil}, // 5 ms
		{od.IndexSyncOverflow, []byte{5}, canopen.AbortDeviceState},
		{od.IndexCOBIDSync, le32(0x80 | uint32(canopen.COBGenerate)), nil},
		{od.IndexCOBIDSync, le32(0x81 | uint32(canopen.COBGenerate)), canopen.AbortDeviceState},
	} {
		if err := pd.Write(w.index, 0, w.data); !errors.Is(err, w.want) || (w.want == nil && err != nil) {
			t.Errorf("write 0x%04X % X: %v, want %v", w.index, w.data, err, w.want)
		}
	}
	if !p.Producing() || p.Period() != 5*time.Millisecond {
		t.Fatal(p.Producing(), p.Period())
	}
	// The consumer knows the overflow from its own 0x1019: set it to 4 too.
	if err := cd.Write(od.IndexSyncOverflow, 0, []byte{4}); err != nil {
		t.Fatal(err)
	}
	_ = cd.Write(od.IndexSyncWindow, 0, le32(2000))
	var seen []uint8
	for len(seen) < 6 {
		e := ev.wait(t, time.Second, func(Event) bool { return true })
		if e.LengthError {
			t.Errorf("length error %+v", e)
		}
		seen = append(seen, e.Counter)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[i-1]%4+1 {
			t.Errorf("counters %v", seen)
		}
	}
	// A new COB-ID on both sides (the producer stops generating first).
	_ = pd.Write(od.IndexCOBIDSync, 0, le32(0x81))
	if p.Producing() {
		t.Error("producing after bit 30 cleared")
	}
	_ = pd.Write(od.IndexCOBIDSync, 0, le32(0x81|uint32(canopen.COBGenerate)))
	_ = cd.Write(od.IndexCOBIDSync, 0, le32(0x81))
	time.Sleep(10 * time.Millisecond)
	for len(ev.ch) > 0 {
		<-ev.ch
	}
	e := ev.wait(t, time.Second, func(Event) bool { return true })
	if e.WindowEnd.Sub(e.Time) != 2*time.Millisecond {
		t.Errorf("event %+v", e)
	}
	_ = pd.Write(od.IndexCyclePeriod, 0, le32(0))
	if p.Producing() {
		t.Error("producing with period 0")
	}
	_ = cd.Write(od.IndexSyncWindow, 0, le32(0)) // no re-subscription

	for _, d := range []*od.Dictionary{od.New(1), dictWith(od.IndexCOBIDSync), dictWith(od.IndexCOBIDSync, od.IndexCyclePeriod)} {
		if _, err := NewProducer(a, WithDictionary(d)); err == nil {
			t.Error("incomplete dictionary accepted")
		}
	}
	if _, err := NewConsumer(a, WithDictionary(od.New(1))); err == nil {
		t.Error("dictionary without 0x1005 accepted")
	}
}

func dictWith(indices ...uint16) *od.Dictionary {
	d := od.New(1)
	for _, i := range indices {
		_ = d.Add(od.NewVar(i, "x", canopen.Unsigned32, od.AccessRW, nil))
	}
	return d
}

func TestProducerSendError(t *testing.T) {
	a, _ := testbus.Pair(t)
	p, _ := NewProducer(a, WithPeriod(time.Millisecond))
	_ = a.Close()
	if err := p.Send(context.Background()); err == nil {
		t.Error("send on a closed bus")
	}
	_ = p.Start()
	eventually(t, time.Second, func() bool { return p.Stats().Errors > 1 })
	_ = p.Close()
	_ = p.Close()
	// A consumer whose bus closes stops by itself.
	c, _ := NewConsumer(a)
	if err := c.Start(); err == nil {
		t.Error("Start on a closed bus")
	}
}
