// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// fakeClock is a manual clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// TestTPDOSyncWindow: a synchronous TPDO is sent within the window and
// discarded when the window closed before it could be sent.
func TestTPDOSyncWindow(t *testing.T) {
	a, b := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	_ = d.SetUint(od.IndexSyncWindow, 0, 1000) // 1 ms
	clk := newFakeClock()
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, d, 1, WithClock(clk.now))
	tp.SetEnabled(true)

	tp.OnSync(0) // within the window
	eventually(t, "sent", func() bool { return rec.n() == 1 })

	// The window closes while the mapped object is read.
	slow := d.OnRead(idxU8, 0, func(*od.Variable, []byte) ([]byte, error) {
		clk.advance(2 * time.Millisecond)
		return []byte{7}, nil
	})
	tp.OnSync(0)
	slow()
	if st := tp.Stats(); st.WindowMissed != 1 || st.Sent != 1 {
		t.Errorf("%+v", st)
	}
	// An explicit window that has already closed.
	tp.OnSyncWindow(0, clk.now().Add(-time.Microsecond))
	// No window at all.
	tp.OnSyncWindow(0, time.Time{})
	eventually(t, "sent without window", func() bool { return rec.n() == 2 })
	if st := tp.Stats(); st.WindowMissed != 2 || st.SendErrors != 0 {
		t.Errorf("%+v", st)
	}
	// Window 0 in the dictionary: no window.
	_ = d.SetUint(od.IndexSyncWindow, 0, 0)
	tp.OnSync(0)
	eventually(t, "sent", func() bool { return rec.n() == 3 })
}

// blockBus never completes a send before its context ends.
type blockBus struct{ *canbus.Bus }

func (blockBus) Send(ctx context.Context, _ *canbus.Frame) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestTPDOSyncWindowSendBounded: a bus that cannot take the frame within
// the window makes the TPDO discard it.
func TestTPDOSyncWindowSendBounded(t *testing.T) {
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 0, Mapping: Mapping{{idxU8, 0, 8}}})
	_ = d.SetUint(od.IndexSyncWindow, 0, 2000)
	tp := newTPDO(t, blockBus{a}, d, 1)
	tp.SetEnabled(true)
	tp.Trigger()
	start := time.Now()
	tp.OnSync(0)
	if el := time.Since(start); el > time.Second {
		t.Errorf("send not bounded by the window: %v", el)
	}
	if st := tp.Stats(); st.WindowMissed != 1 || st.SendErrors != 0 || st.Sent != 0 {
		t.Errorf("%+v", st)
	}
	// Without a window the failure is a send error (the event-driven path
	// uses its own context: close it to end the send).
	_ = d.SetUint(od.IndexSyncWindow, 0, 0)
	tp.Trigger()
	go func() { time.Sleep(20 * time.Millisecond); _ = tp.Close() }()
	tp.OnSync(0)
	if st := tp.Stats(); st.SendErrors != 1 {
		t.Errorf("%+v", st)
	}
}

// TestRPDOSyncWindow: synchronous RPDOs received after the window closed
// are discarded until the next SYNC.
func TestRPDOSyncWindow(t *testing.T) {
	a, b := testbus.Pair(t)
	db := device(t, 6)
	configure(t, db, false, 1, Config{COBID: 0x206, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	_ = db.SetUint(od.IndexSyncWindow, 0, 1000)
	clk := newFakeClock()
	rp := newRPDO(t, b, db, 1, WithClock(clk.now))
	rp.SetEnabled(true)
	n := uint64(0)
	send := func(v byte) {
		t.Helper()
		f, _ := canbus.NewStandard(0x206, []byte{v})
		_ = a.Send(ctx(t), &f)
		n++
		eventually(t, "received", func() bool { return rp.Stats().Received == n })
	}
	send(1) // before the first SYNC: no window yet
	rp.OnSync(0)
	if u8(db, idxU8, 0) != 1 {
		t.Fatal("not applied")
	}
	send(2) // within the window
	clk.advance(2 * time.Millisecond)
	send(3) // after it: discarded
	rp.OnSync(0)
	if u8(db, idxU8, 0) != 2 || rp.Stats().OutsideWindow != 1 {
		t.Fatalf("value %d, %+v", u8(db, idxU8, 0), rp.Stats())
	}
	clk.advance(2 * time.Millisecond)
	send(4)
	rp.OnSync(0) // nothing buffered
	if u8(db, idxU8, 0) != 2 || rp.Stats().OutsideWindow != 2 {
		t.Fatalf("value %d, %+v", u8(db, idxU8, 0), rp.Stats())
	}
	// explicit window end from the SYNC consumer
	rp.OnSyncWindow(0, clk.now().Add(time.Millisecond))
	send(5)
	rp.OnSyncWindow(0, time.Time{}) // applies 5, no window from now on
	clk.advance(time.Hour)
	send(6)
	rp.OnSync(0)
	if u8(db, idxU8, 0) != 6 || rp.Stats().OutsideWindow != 2 {
		t.Fatalf("value %d, %+v", u8(db, idxU8, 0), rp.Stats())
	}
	// disabling forgets the window
	rp.OnSyncWindow(0, clk.now().Add(-time.Second))
	rp.SetEnabled(false)
	rp.SetEnabled(true)
	send(7)
	rp.OnSync(0)
	if u8(db, idxU8, 0) != 7 {
		t.Fatalf("value %d after re-enable", u8(db, idxU8, 0))
	}
	// event-driven RPDOs ignore the window
	configure(t, db, false, 1, Config{COBID: 0x206, TransmissionType: 254, Mapping: Mapping{{idxU8, 0, 8}}})
	rp.OnSyncWindow(0, clk.now().Add(-time.Second))
	send(8)
	eventually(t, "applied", func() bool { return u8(db, idxU8, 0) == 8 })
}

// TestSyncWindowAbsent: without object 0x1007 there is no window.
func TestSyncWindowAbsent(t *testing.T) {
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	d.Remove(od.IndexSyncWindow)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	tp := newTPDO(t, a, d, 1)
	if !tp.windowEnd().IsZero() {
		t.Error("window without 0x1007")
	}
}
