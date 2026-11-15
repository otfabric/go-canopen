// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// TestClosedTPDOSendsNothing: after Close a TPDO is disabled for good.
// Transmit fails with ErrClosed, a SYNC and an enable send nothing, and
// no frame reaches the bus.
func TestClosedTPDOSendsNothing(t *testing.T) {
	a, b := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	rec := record(t, b, 0x185)
	tp := newTPDO(t, a, d, 1)
	tp.SetEnabled(true)
	tp.OnSync(0)
	eventually(t, "the PDO of the SYNC before Close", func() bool { return rec.n() == 1 })
	if err := tp.Close(); err != nil {
		t.Fatal(err)
	}
	before := tp.Stats()

	if err := tp.Transmit(ctx(t)); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Transmit after Close = %v, want ErrClosed", err)
	}
	tp.OnSync(0)
	tp.SetEnabled(false)
	tp.SetEnabled(true) // a closed PDO stays disabled
	tp.OnSync(0)
	tp.Trigger()
	if err := tp.Transmit(ctx(t)); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Transmit after Close and SetEnabled = %v, want ErrClosed", err)
	}
	if after := tp.Stats(); after != before {
		t.Errorf("counters changed after Close: %+v, before %+v", after, before)
	}
	// A frame sent now on the same COB-ID arrives after anything the
	// TPDO sent: it must be the only one.
	mark, _ := canbus.NewStandard(0x185, []byte{0xEE})
	if err := a.Send(ctx(t), &mark); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the marker frame", func() bool { return rec.n() >= 2 })
	if n, last := rec.n(), rec.last(); n != 2 || len(last) != 1 || last[0] != 0xEE {
		t.Errorf("%d frames on the bus, last % X: want the PDO sent before Close and the marker", n, last)
	}
}

// TestClosedRPDOAppliesNothing: Close drops the synchronous PDO waiting
// for its SYNC; a SYNC after Close writes nothing to the dictionary.
func TestClosedRPDOAppliesNothing(t *testing.T) {
	a, b := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, false, 1, Config{COBID: 0x205, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	rp := newRPDO(t, a, d, 1)
	rp.SetEnabled(true)
	f, _ := canbus.NewStandard(0x205, []byte{0x42})
	if err := b.Send(ctx(t), &f); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the PDO buffered", func() bool { return rp.Stats().Received == 1 })
	if err := rp.Close(); err != nil {
		t.Fatal(err)
	}
	rp.OnSync(0)
	rp.SetEnabled(true)
	rp.OnSync(0)
	if st := rp.Stats(); st.Applied != 0 {
		t.Errorf("%d PDOs applied after Close", st.Applied)
	}
	if v := u8(d, idxU8, 0); v != 0 {
		t.Errorf("object written after Close: %#x", v)
	}
}

// enteredBus blocks every send until its context ends, and reports the
// first send it holds.
type enteredBus struct {
	*canbus.Bus
	entered chan struct{}
	once    atomic.Bool
}

func (e *enteredBus) Send(ctx context.Context, _ *canbus.Frame) error {
	if e.once.CompareAndSwap(false, true) {
		close(e.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestCloseEndsTransmit: a Transmit in progress when the TPDO is closed
// ends with ErrClosed, whatever its own context.
func TestCloseEndsTransmit(t *testing.T) {
	a, _ := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	bus := &enteredBus{Bus: a, entered: make(chan struct{})}
	tp := newTPDO(t, bus, d, 1)
	tp.SetEnabled(true)
	done := make(chan error, 1)
	go func() { done <- tp.Transmit(context.Background()) }()
	<-bus.entered
	if err := tp.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, canopen.ErrClosed) {
			t.Errorf("Transmit in progress at Close = %v, want ErrClosed", err)
		}
	case <-ctx(t).Done():
		t.Fatal("Transmit still in progress after Close")
	}
}

// TestEventTimerRestartsAfterReadError: when a mapped object cannot be
// read at an event timer expiry, nothing is sent, and the event timer
// starts again: the next transmission follows one event timer later.
func TestEventTimerRestartsAfterReadError(t *testing.T) {
	const eventTimer = 20 * time.Millisecond
	synctest.Test(t, func(t *testing.T) {
		a, _ := testbus.Pair(t)
		d := device(t, 5)
		configure(t, d, true, 2, Config{COBID: 0x285, TransmissionType: 254, EventTimer: 20, Mapping: Mapping{{idxU8, 0, 8}}})
		var fail atomic.Bool
		d.OnRead(idxU8, 0, func(_ *od.Variable, stored []byte) ([]byte, error) {
			if fail.CompareAndSwap(true, false) {
				return nil, canopen.AbortGeneral
			}
			return stored, nil
		})
		tp := newTPDO(t, a, d, 2)
		tp.SetEnabled(true)
		time.Sleep(5*eventTimer + eventTimer/2)
		if st := tp.Stats(); st.Sent != 6 || st.ReadErrors != 0 {
			t.Fatalf("before the read error: %+v, want 6 sent (the enable and 5 event timers)", st)
		}
		// The next expiry cannot read the object.
		fail.Store(true)
		time.Sleep(eventTimer)
		if st := tp.Stats(); st.Sent != 6 || st.ReadErrors != 1 {
			t.Fatalf("at the read error: %+v, want 6 sent and 1 read error", st)
		}
		// One event timer later the PDO is sent again, and goes on.
		time.Sleep(eventTimer)
		if st := tp.Stats(); st.Sent != 7 {
			t.Errorf("one event timer after the read error: %d sent, want 7", st.Sent)
		}
		time.Sleep(3 * eventTimer)
		if st := tp.Stats(); st.Sent != 10 || st.ReadErrors != 1 {
			t.Errorf("four event timers after the read error: %+v, want 10 sent", st)
		}
	})
}

// TestCloseBetweenSampleAndSend: a TPDO that is closed while a SYNC's
// transmission reads the mapped objects does not send that PDO.
func TestCloseBetweenSampleAndSend(t *testing.T) {
	a, b := testbus.Pair(t)
	d := device(t, 5)
	configure(t, d, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}})
	rec := record(t, b, 0x185)
	entered, held := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	d.OnRead(idxU8, 0, func(_ *od.Variable, stored []byte) ([]byte, error) {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-held
		}
		return stored, nil
	})
	tp := newTPDO(t, a, d, 1)
	tp.SetEnabled(true)
	synced := make(chan struct{})
	go func() {
		tp.OnSync(0)
		close(synced)
	}()
	<-entered // the SYNC's transmission is reading the object
	if err := tp.Close(); err != nil {
		t.Fatal(err)
	}
	close(held)
	<-synced
	if st := tp.Stats(); st.Sent != 0 {
		t.Errorf("%d PDOs sent after Close", st.Sent)
	}
	mark, _ := canbus.NewStandard(0x185, []byte{0xEE})
	if err := a.Send(ctx(t), &mark); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the marker frame", func() bool { return rec.n() >= 1 })
	if n, last := rec.n(), rec.last(); n != 1 || last[0] != 0xEE {
		t.Errorf("%d frames on the bus, last % X: want the marker only", n, last)
	}
}
