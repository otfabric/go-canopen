// SPDX-License-Identifier: MIT

package pdo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

const idxBytes uint16 = 0x2300 // ARRAY of 64 mappable UNSIGNED8

// fdDevice returns a device with 64 mapping entries per PDO and the
// 64-byte array.
func fdDevice(t testing.TB, node canopen.NodeID) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: node, TPDOs: 2, RPDOs: 2, MappingEntries: 64})
	if err != nil {
		t.Fatal(err)
	}
	arr := od.NewArray(idxBytes, "bytes", canopen.Unsigned8, od.AccessRW, 64, nil, od.AccessConst)
	for _, v := range arr.Subs[1:] {
		v.PDOMappable = true
	}
	if err := d.Add(arr); err != nil {
		t.Fatal(err)
	}
	return d
}

// bytesMapping maps n bytes of the array.
func bytesMapping(n int) Mapping {
	m := make(Mapping, n)
	for i := range m {
		m[i] = Entry{idxBytes, uint8(i + 1), 8}
	}
	return m
}

func TestPackFD(t *testing.T) {
	m := bytesMapping(64)
	vals := make([][]byte, 64)
	for i := range vals {
		vals[i] = []byte{byte(i * 3)}
	}
	data, err := PackFD(m, vals)
	if err != nil || len(data) != 64 || data[63] != 189 {
		t.Fatalf("%v %v", data, err)
	}
	if _, err := Pack(m, vals); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("classical Pack of 512 bits: %v", err)
	}
	out, err := UnpackFD(m, data)
	if err != nil || len(out) != 64 || out[10][0] != 30 {
		t.Fatalf("%v %v", out, err)
	}
	if _, err := Unpack(m, data); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("classical Unpack of 512 bits: %v", err)
	}
	// unaligned entries across the whole frame
	odd := Mapping{{1, 1, 1}, {1, 2, 64}, {1, 3, 64}, {1, 4, 64}, {1, 5, 64}, {1, 6, 64}, {1, 7, 64}, {1, 8, 64}, {1, 9, 63}}
	v := make([][]byte, len(odd))
	for i := range v {
		v[i] = bytes.Repeat([]byte{byte(0xA5 + i)}, 8)
	}
	v[0] = []byte{1}
	data, err = PackFD(odd, v)
	if err != nil || len(data) != 64 {
		t.Fatalf("%d %v", len(data), err)
	}
	back, err := UnpackFD(odd, data)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 8; i++ {
		if !bytes.Equal(back[i], v[i]) {
			t.Errorf("entry %d: % X", i, back[i])
		}
	}
	if back[8][7] != v[8][7]&0x7F || back[0][0] != 1 {
		t.Errorf("edges: % X % X", back[8], back[0])
	}
	if _, err := PackFD(append(odd, Entry{1, 10, 2}), append(v, nil)); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("513 bits: %v", err)
	}
	if _, err := UnpackFD(odd, data[:63]); !errors.Is(err, ErrLength) {
		t.Errorf("short: %v", err)
	}
}

func TestValidateAndProtectFD(t *testing.T) {
	d := fdDevice(t, 5)
	m := bytesMapping(64)
	if err := m.Validate(d, true); err != canopen.AbortPDOLength {
		t.Errorf("classical: %v", err)
	}
	if err := m.ValidateFD(d, true); err != nil {
		t.Errorf("FD: %v", err)
	}
	// Configure over SDO with the FD rules installed.
	un, err := ProtectFD(d, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Configure(context.Background(), &fakeSDO{d: d}, true, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m}); err != nil {
		t.Fatalf("FD configure: %v", err)
	}
	un()
	d2 := fdDevice(t, 5)
	un, _ = Protect(d2, true, 1)
	defer un()
	err = Configure(context.Background(), &fakeSDO{d: d2}, true, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m})
	if !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("classical configure of 512 bits: %v", err)
	}
	if _, err := ProtectFD(d, true, 0); err == nil {
		t.Error("PDO 0")
	}
}

// TestFDEndToEnd sends a 64-byte CANopen FD TPDO to an FD RPDO.
func TestFDEndToEnd(t *testing.T) {
	p := testbus.Ports(t, 3, virtual.WithFD())
	da, db := fdDevice(t, 5), fdDevice(t, 6)
	m := bytesMapping(64)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m})
	configure(t, db, false, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m})
	rec := record(t, p[2], 0x185)
	tp := newTPDO(t, p[0], da, 1, WithFD(true))
	var got []byte
	done := make(chan struct{}, 4)
	rp := newRPDO(t, p[1], db, 1, WithFD(true), WithReceiveHandler(func(data []byte) {
		got = append([]byte(nil), data...)
		done <- struct{}{}
	}))
	rp.SetEnabled(true)
	for i := 1; i <= 64; i++ {
		_ = da.SetUint(idxBytes, uint8(i), uint64(i*2))
	}
	tp.SetEnabled(true)
	if err := tp.Transmit(ctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "RPDO applied", func() bool { return u8(db, idxBytes, 64) == 128 })
	<-done
	if len(got) != 64 || got[0] != 2 {
		t.Errorf("received % X", got)
	}
	eventually(t, "frame recorded", func() bool { return rec.n() > 0 })
	rec.mu.Lock()
	f := rec.frames[len(rec.frames)-1]
	rec.mu.Unlock()
	if !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.Len != 64 {
		t.Errorf("frame %v", f)
	}
	if st := rp.Stats(); st.Long != 0 || st.Short != 0 || st.Applied == 0 {
		t.Errorf("%+v", st)
	}
	if !tp.Config().Active() {
		t.Error("inactive")
	}
}

// TestFDPadding: a 9-byte mapping travels in a 12-byte frame, which is not
// "too long"; a 16-byte frame is.
func TestFDPadding(t *testing.T) {
	p := testbus.Ports(t, 2, virtual.WithFD())
	da, db := fdDevice(t, 5), fdDevice(t, 6)
	m := bytesMapping(9)
	configure(t, da, true, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m})
	configure(t, db, false, 1, Config{COBID: 0x185, TransmissionType: 254, Mapping: m})
	rec := record(t, p[1], 0x185)
	tp := newTPDO(t, p[0], da, 1, WithFD(false))
	var nev atomic.Int32
	rp := newRPDO(t, p[1], db, 1, WithFD(false), WithErrorHandler(func(ErrorEvent) { nev.Add(1) }))
	rp.SetEnabled(true)
	tp.SetEnabled(true)
	_ = da.SetUint(idxBytes, 9, 99)
	eventually(t, "applied", func() bool { return u8(db, idxBytes, 9) == 99 })
	if rec.n() == 0 || len(rec.last()) != 12 {
		t.Fatalf("frame of %d bytes", len(rec.last()))
	}
	send := func(n int, fd bool) {
		var f canbus.Frame
		if fd {
			f, _ = canbus.NewFD(0x185, false, false, make([]byte, n))
		} else {
			f, _ = canbus.NewStandard(0x185, make([]byte, n))
		}
		_ = p[0].Send(ctx(t), &f)
	}
	before := rp.Stats().Received
	send(16, true)
	send(8, false)
	eventually(t, "received", func() bool { return rp.Stats().Received == before+2 })
	if st := rp.Stats(); st.Long != 1 || st.Short != 1 {
		t.Errorf("%+v", st)
	}
	eventually(t, "length events", func() bool { return nev.Load() > 0 })
	// TPDO FD does not answer remote requests (and has no subscription)
	if tp.Stats().RTRs != 0 {
		t.Error("RTR")
	}
}

func TestFDCodec(t *testing.T) {
	p := testbus.Ports(t, 2, virtual.WithFD())
	d := fdDevice(t, 5)
	m := bytesMapping(64)
	if _, err := NewDecoder(m, d); !errors.Is(err, canopen.AbortPDOLength) {
		t.Errorf("classical decoder of 512 bits: %v", err)
	}
	enc, err := NewEncoder(m, d, WithFD(false))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(m, d, WithFD(false))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []Value, 1)
	l := NewListener(p[1], 0x205, dec, func(f *canbus.Frame, v []Value) {
		if f.IsFD() {
			got <- v
		}
	})
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Request(ctx(t)); !errors.Is(err, ErrNoRTR) {
		t.Errorf("FD request: %v", err)
	}
	vals := make([]any, 64)
	for i := range vals {
		vals[i] = uint8(i)
	}
	if err := NewSender(p[0], 0x205, enc).Send(ctx(t), vals...); err != nil {
		t.Fatal(err)
	}
	v := <-got
	if len(v) != 64 || fmt.Sprint(v[63].Value) != "63" {
		t.Errorf("%v", v)
	}
}
