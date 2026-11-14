// SPDX-License-Identifier: MIT

package cdcf_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"runtime"
	"testing"
	"unsafe"

	"github.com/otfabric/go-canopen/cdcf"
)

// perCall returns the octets f allocates per call, averaged over runs
// calls, so that what the test framework allocates meanwhile does not
// count.
func perCall(runs int, f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// concise returns a concise DCF announcing count entries, followed by body.
func concise(count uint32, body []byte) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, count), body...)
}

// entryHeader is one entry header: index 0x2000, sub-index 0, size.
func entryHeader(size uint32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{0x00, 0x20, 0x00}, size)
}

// TestDecodeAllocationBudget (safety.md, allocation budgets): Decode
// allocates in proportion to its input, whatever count and sizes the
// input announces. An input it refuses allocates no more than the error;
// one it accepts no more than the entries it holds and their data.
func TestDecodeAllocationBudget(t *testing.T) {
	const body = 64 << 10
	const errBudget = 1 << 10 // the error and its message
	const page = 8 << 10      // a large allocation is rounded up to whole pages
	entry := uint64(unsafe.Sizeof(cdcf.Entry{}))
	zeros := make([]byte, body)
	empties := bytes.Repeat(entryHeader(0), body/7) // the most entries the input holds
	one := append(entryHeader(body-7), make([]byte, body-7)...)
	for _, tc := range []struct {
		name   string
		in     []byte
		ok     bool
		budget uint64
	}{
		{"count at the type maximum", concise(math.MaxUint32, zeros), false, errBudget},
		{"count one above what the input holds", concise(body/7+1, empties), false, errBudget},
		{"count of the int32 boundary", concise(math.MaxInt32, zeros), false, errBudget},
		{"size at the type maximum", concise(1, append(entryHeader(math.MaxUint32), zeros...)), false, errBudget},
		{"size one above what is left", concise(1, append(entryHeader(body+1), zeros...)), false, errBudget},
		{"most entries the input holds", concise(body/7, empties), true, uint64(body/7)*entry + page + errBudget},
		{"one entry with all the data", concise(1, one), true, entry + body + page + errBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			got := perCall(20, func() { _, err = cdcf.Decode(tc.in) })
			if (err == nil) != tc.ok {
				t.Fatalf("Decode: %v, want ok %v", err, tc.ok)
			}
			if got > tc.budget {
				t.Fatalf("%d octets allocated per call for %d octets of input, budget %d", got, len(tc.in), tc.budget)
			}
		})
	}
}

// TestDecodeOwnsData (safety.md, aliasing): the entries' data are copies
// of the input, and Encode keeps no entry's data.
func TestDecodeOwnsData(t *testing.T) {
	entries := []cdcf.Entry{{Index: 0x1017, Data: []byte{0xE8, 0x03}}, {Index: 0x2000, SubIndex: 1, Data: []byte{1, 2, 3}}}
	b, err := cdcf.Encode(entries)
	if err != nil {
		t.Fatal(err)
	}
	wire := bytes.Clone(b)
	entries[0].Data[0], entries[1].Data[2] = 0, 0
	if !bytes.Equal(b, wire) {
		t.Fatal("the encoded configuration changed with the entries' data")
	}
	got, err := cdcf.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		b[i] = 0xFF
	}
	if !bytes.Equal(got[0].Data, []byte{0xE8, 0x03}) || !bytes.Equal(got[1].Data, []byte{1, 2, 3}) {
		t.Fatalf("decoded data changed with the input: % X, % X", got[0].Data, got[1].Data)
	}
}
