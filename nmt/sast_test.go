// SPDX-License-Identifier: MIT

package nmt

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

func preOp() canopen.NMTState { return canopen.StatePreOperational }

func TestConversions(t *testing.T) {
	if millis(1000) != time.Second || millis(math.MaxUint64) <= 0 {
		t.Fatalf("millis: %v %v", millis(1000), millis(math.MaxUint64))
	}
	if lifeFactor(3) != 3 || lifeFactor(0x1FF) != 0xFF {
		t.Fatalf("lifeFactor: %d %d", lifeFactor(3), lifeFactor(0x1FF))
	}
	if e, ok := entryOf(0x000501F4); !ok || e != 0x000501F4 {
		t.Fatalf("entryOf: 0x%X %v", e, ok)
	}
	if _, ok := entryOf(1 << 32); ok {
		t.Fatal("entryOf(1<<32) ok")
	}
	for _, tc := range []struct {
		d    time.Duration
		n    uint8
		want time.Duration
	}{
		{100 * time.Millisecond, 3, 300 * time.Millisecond},
		{0, 3, 0},
		{time.Second, 0, 0},
		{-time.Second, 3, 0},
		{math.MaxInt64 / 2, 255, math.MaxInt64},
	} {
		if got := mulSat(tc.d, tc.n); got != tc.want {
			t.Fatalf("mulSat(%v, %d) = %v, want %v", tc.d, tc.n, got, tc.want)
		}
	}
	if n, _, ok := ParseConsumerEntry(0xFF05_01F4); !ok || n != 5 {
		t.Fatalf("ParseConsumerEntry with reserved bits: %d %v", n, ok)
	}
}

// TestLifeTimeSaturates: a guard time times a life time factor beyond a
// time.Duration wrapped to a negative life time, a deadline in the past
// and an immediate life guarding event; it now saturates.
func TestLifeTimeSaturates(t *testing.T) {
	r, err := NewGuardResponder(nil, 5, preOp)
	if err != nil {
		t.Fatal(err)
	}
	r.SetLifeGuarding(math.MaxInt64/2, 255)
	r.mu.Lock()
	lt := r.lifeTimeLocked()
	r.mu.Unlock()
	if lt != math.MaxInt64 {
		t.Fatalf("life time %v", lt)
	}
}

// TestWideDictionaryTypes: dictionaries that give 0x100C, 0x100D, 0x1016
// or 0x1017 a wider type than the standard one are neither truncated nor
// wrapped.
func TestWideDictionaryTypes(t *testing.T) {
	d := od.New(5)
	_ = d.Add(od.NewVar(od.IndexGuardTime, "Guard time", canopen.Unsigned64, od.AccessRW, uint64(math.MaxUint64)))
	_ = d.Add(od.NewVar(od.IndexLifeTimeFactor, "Life time factor", canopen.Unsigned64, od.AccessRW, uint64(0x103)))
	_ = d.Add(od.NewVar(od.IndexProducerHeartbeat, "Producer heartbeat time", canopen.Unsigned64, od.AccessRW, uint64(math.MaxUint64)))
	_ = d.Add(od.NewArray(od.IndexConsumerHeartbeat, "Consumer heartbeat time", canopen.Unsigned64, od.AccessRW, 2, nil, od.AccessConst))
	_ = d.SetUint(od.IndexConsumerHeartbeat, 1, 1<<32|0x000601F4) // too wide: unused
	_ = d.SetUint(od.IndexConsumerHeartbeat, 2, 0x000701F4)

	r, err := NewGuardResponder(nil, 5, preOp, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	if r.guardTime <= 0 || r.factor != 0xFF {
		t.Fatalf("guard %v factor %d", r.guardTime, r.factor)
	}
	p, err := NewHeartbeatProducer(nil, 5, preOp, WithDictionary(d))
	if err != nil || p.period <= 0 {
		t.Fatalf("producer period %v, %v", p.period, err)
	}
	c, err := NewHeartbeatConsumer(nil, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	c.mu.Lock()
	got := len(c.fromDict)
	_, has7 := c.fromDict[7]
	c.mu.Unlock()
	if got != 1 || !has7 {
		t.Fatalf("consumer entries %v", c.fromDict)
	}
	data := binary.LittleEndian.AppendUint64(nil, 1<<32|0x000801F4)
	if err := d.Write(od.IndexConsumerHeartbeat, 1, data); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Fatalf("write of a 0x1016 entry above 32 bits: %v", err)
	}
	// A duplicate check skips a too-wide neighbour instead of truncating
	// it into a match.
	_ = d.SetUint(od.IndexConsumerHeartbeat, 2, 1<<32|0x000701F4)
	if err := d.Write(od.IndexConsumerHeartbeat, 1, binary.LittleEndian.AppendUint64(nil, 0x000701F4)); err != nil {
		t.Fatalf("write next to a too-wide entry: %v", err)
	}
}
