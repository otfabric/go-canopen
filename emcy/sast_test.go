// SPDX-License-Identifier: MIT

package emcy

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// TestWideDictionaryTypes: a dictionary that gives 0x1014 or 0x1015 a
// type wider than the standard one does not reach the COB-ID or the
// inhibit time truncated or wrapped: a COB-ID above 32 bits is refused,
// an inhibit time beyond a time.Duration saturates (it wrapped to a
// negative and so to no inhibit at all).
func TestWideDictionaryTypes(t *testing.T) {
	wide := func(cob, inh uint64) *od.Dictionary {
		d := od.New(5)
		_ = d.Add(od.NewVar(od.IndexCOBIDEmcy, "COB-ID EMCY", canopen.Unsigned64, od.AccessRW, cob))
		_ = d.Add(od.NewVar(od.IndexInhibitEmcy, "Inhibit time EMCY", canopen.Unsigned64, od.AccessRW, inh))
		return d
	}
	if _, err := NewProducer(nil, 5, WithDictionary(wide(1<<32|0x85, 0))); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Fatalf("COB-ID above 32 bits: %v", err)
	}
	d := wide(0x85, math.MaxUint64)
	p, err := NewProducer(nil, 5, WithDictionary(d))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if p.cob != 0x85 || p.inh <= 0 {
		t.Fatalf("cob 0x%X inhibit %v", p.cob, p.inh)
	}
	data := binary.LittleEndian.AppendUint64(nil, 1<<32|0x85)
	if err := d.Write(od.IndexCOBIDEmcy, 0, data); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Fatalf("write of a COB-ID above 32 bits: %v", err)
	}
}

func TestInhibitDurationAndCOBIDOf(t *testing.T) {
	if got := inhibitDuration(10); got != time.Millisecond {
		t.Fatalf("inhibitDuration(10) = %v", got)
	}
	if got := inhibitDuration(math.MaxUint64); got <= 0 {
		t.Fatalf("inhibitDuration(max) = %v", got)
	}
	if c, ok := cobidOf(0x85); !ok || c != 0x85 {
		t.Fatalf("cobidOf(0x85) = 0x%X, %v", c, ok)
	}
	if _, ok := cobidOf(1 << 32); ok {
		t.Fatal("cobidOf(1<<32) ok")
	}
}

func TestMessageBytesCode(t *testing.T) {
	b := Message{Code: 0x8130, Register: RegCommunication}.Bytes()
	if b[0] != 0x30 || b[1] != 0x81 || b[2] != byte(RegCommunication) {
		t.Fatalf("bytes % X", b)
	}
}
