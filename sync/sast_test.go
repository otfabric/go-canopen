// SPDX-License-Identifier: MIT

package syncobj

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func le64(x uint64) []byte { return binary.LittleEndian.AppendUint64(nil, x) }

// widen turns index:00 of d into an UNSIGNED64 and stores x in it: a
// malformed dictionary (EDS) whose object is wider than CiA 301 defines.
func widen(t *testing.T, d *od.Dictionary, index uint16, x uint64) {
	t.Helper()
	v, err := d.Variable(index, 0)
	if err != nil {
		t.Fatal(err)
	}
	v.DataType = canopen.Unsigned64
	if err := d.Set(index, 0, le64(x)); err != nil {
		t.Fatal(err)
	}
}

func standard(t *testing.T) *od.Dictionary {
	t.Helper()
	d, err := od.Standard(od.Config{NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestNewWide: dictionary values wider than their object are refused
// instead of being truncated: a 0x1019 of 256 used to become the valid 0,
// a 0x1006 above 32 bits a wrapped period.
func TestNewWide(t *testing.T) {
	a, _ := testbus.Pair(t)
	for _, c := range []struct {
		index uint16
		x     uint64
		opt   Option // overrides the value
	}{
		{od.IndexSyncOverflow, 256, WithOverflow(4)},
		{od.IndexCyclePeriod, 1<<63 | 1, WithPeriod(time.Millisecond)},
		{od.IndexCOBIDSync, 1<<32 | 0x80, nil},
	} {
		d := standard(t)
		widen(t, d, c.index, c.x)
		if _, err := NewProducer(a, WithDictionary(d)); err == nil {
			t.Errorf("producer 0x%04X = 0x%X accepted", c.index, c.x)
		}
		if c.opt != nil {
			p, err := NewProducer(a, WithDictionary(d), c.opt)
			if err != nil {
				t.Errorf("producer 0x%04X overridden: %v", c.index, err)
			} else {
				_ = p.Close()
			}
		}
	}
	d := standard(t)
	widen(t, d, od.IndexCOBIDSync, 1<<32|0x80)
	if _, err := NewConsumer(a, WithDictionary(d)); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("consumer: %v", err)
	}
	// A consumer ignores a window above 32 bits and an invalid overflow.
	d = standard(t)
	widen(t, d, od.IndexSyncWindow, 1<<63|1)
	if err := d.Set(od.IndexSyncOverflow, 0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	c, err := NewConsumer(a, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c.Window() != 0 || c.overflowSet {
		t.Errorf("window %v overflow %d/%v", c.Window(), c.overflow, c.overflowSet)
	}
}

// TestHookWide: a COB-ID SYNC write above 32 bits is refused, not compared
// by its truncation; a current value above 32 bits does not block a write.
func TestHookWide(t *testing.T) {
	a, _ := testbus.Pair(t)
	d := standard(t)
	p, err := NewProducer(a, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	widen(t, d, od.IndexCOBIDSync, 0x80|uint64(canopen.COBGenerate))
	if err := d.Write(od.IndexCOBIDSync, 0, le64(1<<32|0x80|uint64(canopen.COBGenerate))); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("wide write: %v", err)
	}
	widen(t, d, od.IndexCOBIDSync, 1<<32|0x80|uint64(canopen.COBGenerate))
	if err := d.Write(od.IndexCOBIDSync, 0, le64(0x81|uint64(canopen.COBGenerate))); err != nil {
		t.Errorf("write over wide current value: %v", err)
	}
}

// TestWatchWide: the services ignore dictionary changes to values wider
// than their object, and invalid counter overflow values.
func TestWatchWide(t *testing.T) {
	a, b := testbus.Pair(t)
	pd, cd := standard(t), standard(t)
	p, err := NewProducer(a, WithDictionary(pd))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	c, err := NewConsumer(b, WithDictionary(cd))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := pd.SetUint(od.IndexSyncOverflow, 0, 4); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetUint(od.IndexSyncOverflow, 0, 4); err != nil {
		t.Fatal(err)
	}
	if err := cd.SetUint(od.IndexSyncWindow, 0, 1000); err != nil {
		t.Fatal(err)
	}
	if err := pd.Set(od.IndexSyncOverflow, 0, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := cd.Set(od.IndexSyncOverflow, 0, []byte{241}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*od.Dictionary{pd, cd} {
		widen(t, d, od.IndexCOBIDSync, 1<<32|0x81|uint64(canopen.COBGenerate))
		widen(t, d, od.IndexCyclePeriod, 1<<63|1)
		widen(t, d, od.IndexSyncWindow, 1<<63|1)
		widen(t, d, od.IndexSyncOverflow, 0x102)
	}
	p.mu.Lock()
	pcob, povf, gen := p.cob, p.overflow, p.generate
	p.mu.Unlock()
	if pcob != 0x80 || povf != 4 || gen || p.Period() != 0 {
		t.Errorf("producer cob %v overflow %d generate %v period %v", pcob, povf, gen, p.Period())
	}
	c.mu.Lock()
	ccob, covf := c.cob, c.overflow
	c.mu.Unlock()
	if ccob != 0x80 || covf != 4 || c.Window() != time.Millisecond {
		t.Errorf("consumer cob %v overflow %d window %v", ccob, covf, c.Window())
	}
}
