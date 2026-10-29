// SPDX-License-Identifier: MIT

package cdcf_test

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-canopen/cdcf"
)

// FuzzDecode checks that Decode never panics, and that what it accepts
// encodes back to the same bytes.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{1, 0, 0, 0, 0x17, 0x10, 0, 2, 0, 0, 0, 0xE8, 0x03})
	f.Add([]byte{2, 0, 0, 0, 0x17, 0x10, 0, 0, 0, 0, 0, 0x00, 0x18, 1, 4, 0, 0, 0, 1, 2, 3, 4})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, b []byte) {
		entries, err := cdcf.Decode(b)
		if err != nil {
			return
		}
		if len(b) == 0 {
			return
		}
		out, err := cdcf.Encode(entries)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("round trip\n% X\n% X", b, out)
		}
		_ = cdcf.PDOSequence(entries)
	})
}
