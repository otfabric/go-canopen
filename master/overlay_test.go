// SPDX-License-Identifier: MIT

package master_test

import (
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/pdo"
)

// TestOverlayBaseMappingLength: Overlay takes a base mapping of up to
// pdo.MaxEntries entries and refuses a longer one with
// canopen.AbortPDOLength, with and without entries to apply.
func TestOverlayBaseMappingLength(t *testing.T) {
	mapping := func(n int) pdo.Mapping {
		m := make(pdo.Mapping, n)
		for i := range m {
			m[i] = pdo.Entry{Index: 0x2000, SubIndex: uint8(i), Bits: 1}
		}
		return m
	}
	for _, tc := range []struct {
		name string
		n    int
		ok   bool
	}{
		{"none", 0, true},
		{"one", 1, true},
		{"one below the maximum", pdo.MaxEntries - 1, true},
		{"maximum", pdo.MaxEntries, true},
		{"one above the maximum", pdo.MaxEntries + 1, false},
		{"type maximum of the count", 255, false},
		{"far above", 4096, false},
	} {
		for name, entries := range map[string][]master.Entry{
			"without entries": nil,
			"with an entry":   {{Index: 0x1800, SubIndex: 3, Data: u16(10)}},
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				base := pdo.Config{COBID: 0x185, TransmissionType: 1, Mapping: mapping(tc.n)}
				got, err := master.Overlay(base, entries)
				if !tc.ok {
					if !errors.Is(err, canopen.AbortPDOLength) {
						t.Fatalf("Overlay of a %d-entry base mapping = %v, want AbortPDOLength", tc.n, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("Overlay of a %d-entry base mapping: %v", tc.n, err)
				}
				if len(got.Mapping) != tc.n {
					t.Fatalf("mapping of %d entries, want %d", len(got.Mapping), tc.n)
				}
				for i, e := range got.Mapping {
					if e != base.Mapping[i] {
						t.Fatalf("entry %d = %v, want %v", i+1, e, base.Mapping[i])
					}
				}
			})
		}
	}
}
