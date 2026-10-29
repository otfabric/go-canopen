// SPDX-License-Identifier: MIT

package pdo

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestConfigureReadConfig(t *testing.T) {
	tests := []struct {
		transmit bool
		n        int
		cfg      Config
	}{
		{true, 1, Config{COBID: 0x181, TransmissionType: 254, InhibitTime: 100, EventTimer: 250, Mapping: Mapping{{idxU8, 0, 8}, {idxRO, 0, 16}}}},
		{true, 3, Config{COBID: 0x382 | canopen.COBNoRTR, TransmissionType: 5, SyncStart: 3, Mapping: Mapping{{idxBits, 1, 1}, {idxBits, 2, 1}}}},
		{false, 2, Config{COBID: 0x305, TransmissionType: 0, EventTimer: 100, Mapping: Mapping{{0x0005, 0, 8}, {idxI16, 0, 16}}}},
		{false, 4, Config{COBID: 0x505 | canopen.COBInvalid, TransmissionType: 255}},
		{true, 4, Config{COBID: 0x1ABCDEF | canopen.COBExtended, TransmissionType: 255, Mapping: Mapping{{idxU32, 0, 32}}}},
	}
	for _, tt := range tests {
		d := device(t, 5)
		_ = Protect(d, tt.transmit, tt.n)
		sdo := &fakeSDO{d: d}
		if err := Configure(context.Background(), sdo, tt.transmit, tt.n, tt.cfg); err != nil {
			t.Fatalf("%s: %v (writes %v)", kind(tt.transmit, tt.n), err, sdo.writes)
		}
		got, err := ReadConfig(context.Background(), sdo, tt.transmit, tt.n)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Mapping) == 0 {
			got.Mapping = nil
		}
		if !reflect.DeepEqual(got, tt.cfg) {
			t.Errorf("%s: read %+v, want %+v", kind(tt.transmit, tt.n), got, tt.cfg)
		}
	}
}

func TestConfigureSequence(t *testing.T) {
	d := device(t, 5)
	_ = Protect(d, true, 1)
	// The default TPDO1 is valid with an empty mapping; configure it twice
	// so the second run must invalidate an active PDO first.
	cfg := Config{COBID: 0x185, TransmissionType: 1, InhibitTime: 3, Mapping: Mapping{{idxU8, 0, 8}}}
	for i := 0; i < 2; i++ {
		sdo := &fakeSDO{d: d}
		if err := Configure(context.Background(), sdo, true, 1, cfg); err != nil {
			t.Fatal(err)
		}
		want := "1800:01 1A00:00 1A00:01 1A00:00 1800:02 1800:03 1800:05 1800:06 1800:01"
		if got := strings.Join(sdo.writes, " "); got != want {
			t.Errorf("sequence %s, want %s", got, want)
		}
	}
	// Every step's failure is reported with the object.
	for i := 1; i <= 9; i++ {
		sdo := &fakeSDO{d: d, failAt: i}
		err := Configure(context.Background(), sdo, true, 1, cfg)
		if !errors.Is(err, canopen.AbortGeneral) || !strings.Contains(err.Error(), sdo.writes[i-1]) {
			t.Errorf("fail at %d: %v", i, err)
		}
	}
	// A refused mapping.
	err := Configure(context.Background(), &fakeSDO{d: d}, true, 1, Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxNoMap, 0, 8}}})
	if !errors.Is(err, canopen.AbortNotMappable) {
		t.Errorf("unmappable: %v", err)
	}
	if err := Configure(context.Background(), &fakeSDO{d: d}, true, 0, cfg); !errors.Is(err, ErrNumber) {
		t.Errorf("PDO 0: %v", err)
	}
	if err := Configure(context.Background(), &fakeSDO{d: d}, true, 1, Config{Mapping: make(Mapping, 65)}); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("65 entries: %v", err)
	}
	if _, err := ReadConfig(context.Background(), &fakeSDO{d: d}, false, 0); !errors.Is(err, ErrNumber) {
		t.Errorf("ReadConfig PDO 0: %v", err)
	}
}

// TestConfigureMinimal configures a node whose PDO lacks the optional
// sub-indices.
func TestConfigureMinimal(t *testing.T) {
	d := device(t, 5)
	d.Remove(0x1800)
	if err := d.Add(od.NewRecord(0x1800, "TPDO1",
		od.Sub(1, "cob", canopen.Unsigned32, od.AccessRW, uint32(0x185)),
		od.Sub(2, "type", canopen.Unsigned8, od.AccessRW, uint8(255)))); err != nil {
		t.Fatal(err)
	}
	cfg := Config{COBID: 0x185, TransmissionType: 1, Mapping: Mapping{{idxU8, 0, 8}}}
	if err := Configure(context.Background(), &fakeSDO{d: d}, true, 1, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.EventTimer = 5
	if err := Configure(context.Background(), &fakeSDO{d: d}, true, 1, cfg); !errors.Is(err, canopen.AbortNoSubIndex) {
		t.Errorf("event timer on a node without it: %v", err)
	}
}

func TestCodec(t *testing.T) {
	d := device(t, 5)
	m := Mapping{{idxBool, 0, 1}, {0x0002, 0, 8}, {idxI16, 0, 16}, {idxReal, 0, 32}, {idxBits, 1, 1}}
	enc, err := NewEncoder(m, d)
	if err != nil {
		t.Fatal(err)
	}
	data, err := enc.Encode(true, int16(-300), float32(1.5), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 8 {
		t.Fatalf("% X", data)
	}
	dec, err := NewDecoder(m, d)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := dec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{true, int64(-300), float32(1.5), false}
	if len(vals) != len(want) {
		t.Fatalf("%v", vals)
	}
	for i, v := range vals {
		if v.Value != want[i] {
			t.Errorf("value %d = %v, want %v", i, v.Value, want[i])
		}
	}
	if s := vals[1].String(); s != "i16=-300" {
		t.Errorf("String %q", s)
	}
	if s := (Value{Index: 0x2000, Type: canopen.Unsigned8, Raw: []byte{7}}).String(); s != "2000:00=0x07" {
		t.Errorf("String %q", s)
	}
	if !reflect.DeepEqual(dec.Mapping(), m) || !reflect.DeepEqual(enc.Mapping(), m) {
		t.Error("Mapping")
	}
	if _, err := dec.Decode(data[:3]); !errors.Is(err, ErrLength) {
		t.Errorf("short: %v", err)
	}
	if _, err := enc.Encode(1); err == nil {
		t.Error("value count")
	}
	if _, err := enc.Encode(2, 0, 0, false); err == nil {
		t.Error("BOOLEAN 2")
	}
	for _, bad := range []Mapping{
		{{0x2FFF, 0, 8}},
		{{idxU8, 0, 16}},
		{{0x0005, 0, 16}},
		{{idxU32, 0, 32}, {idxU32, 0, 32}, {idxU8, 0, 8}},
	} {
		if _, err := NewDecoder(bad, d); err == nil {
			t.Errorf("decoder for %s", bad)
		}
		if _, err := NewEncoder(bad, d); err == nil {
			t.Errorf("encoder for %s", bad)
		}
	}
}

func TestSenderListener(t *testing.T) {
	a, b := testbus.Pair(t)
	d := device(t, 5)
	m := Mapping{{idxU8, 0, 8}, {idxI16, 0, 16}}
	enc, _ := NewEncoder(m, d)
	dec, _ := NewDecoder(m, d)
	var mu sync.Mutex
	var got [][]Value
	l := NewListener(b, 0x205, dec, func(f *canbus.Frame, v []Value) {
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
	}, WithLogger(slog.Default()))
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	s := NewSender(a, 0x205, enc)
	if err := s.Send(ctx(t), 7, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx(t), 7); err == nil {
		t.Error("Send with a missing value")
	}
	short, _ := canbus.NewStandard(0x205, []byte{1})
	_ = a.Send(ctx(t), &short)
	eventually(t, "listener", func() bool { st := l.Stats(); return st.Received == 1 && st.Errors == 1 })
	mu.Lock()
	if len(got) != 1 || got[0][0].Value != uint64(7) || got[0][1].Value != int64(-1) {
		t.Errorf("%v", got)
	}
	mu.Unlock()
	// Request sends an RTR of the PDO length.
	rec := record(t, a, 0x205)
	if err := l.Request(ctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "RTR", func() bool { return rec.n() == 1 })
	rec.mu.Lock()
	if f := rec.frames[0]; !f.IsRemote() || f.Len != 3 {
		t.Errorf("RTR %v", f)
	}
	rec.mu.Unlock()
	_ = l.Close()
	_ = l.Close()
	if err := l.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
	if err := NewSender(a, 0x205, enc).Send(ctx(t), 1, 70000); err == nil {
		t.Error("out of range value sent")
	}
	// A listener on a closed bus.
	_ = b.Close()
	if err := NewListener(b, 0x205, dec, nil).Start(); err == nil {
		t.Error("Start on a closed bus")
	}
}

func TestListenerBusClosed(t *testing.T) {
	a, _ := testbus.Pair(t)
	dec, _ := NewDecoder(Mapping{{idxU8, 0, 8}}, device(t, 5))
	l := NewListener(a, 0x185, dec, nil)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	l.wg.Wait() // the goroutine ends with the subscription
	_ = l.Close()
}

func TestSubKey(t *testing.T) {
	if s := keyOf(false, 0x185).String(); s != "none" {
		t.Error(s)
	}
	if s := keyOf(true, 0x185|canopen.COBInvalid|canopen.COBNoRTR).String(); s != "0x185" {
		t.Error(s)
	}
}

func FuzzUnpack(f *testing.F) {
	f.Add(uint32(0x20000108), uint32(0x20030001), uint32(0x20010010), []byte{1, 2, 3, 4})
	f.Add(uint32(0x00050040), uint32(0), uint32(0), []byte{})
	f.Add(uint32(0x200000FF), uint32(0x20000101), uint32(0x00070020), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9})
	d := device(f, 5)
	f.Fuzz(func(t *testing.T, e1, e2, e3 uint32, data []byte) {
		m := Mapping{ParseEntry(e1), ParseEntry(e2), ParseEntry(e3)}
		vals, err := Unpack(m, data)
		if err == nil {
			packed, err := Pack(m, vals)
			if err != nil {
				t.Fatalf("repack: %v", err)
			}
			if len(packed) != m.Len() {
				t.Fatalf("repacked %d bytes, mapping %d", len(packed), m.Len())
			}
		}
		if dec, err := NewDecoder(m, d); err == nil {
			_, _ = dec.Decode(data)
		}
		_ = m.Validate(d, false)
	})
}
