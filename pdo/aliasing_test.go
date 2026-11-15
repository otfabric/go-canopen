// SPDX-License-Identifier: MIT

package pdo

import (
	"bytes"
	"testing"
)

// Aliasing tests (safety.md): values decoded from PDO data are the
// caller's own, independent of the data and of each other, and packed
// data is independent of the values packed.

func aliasMapping() Mapping {
	return Mapping{{idxU8, 0, 8}, {idxI16, 0, 16}, {idxU32, 0, 32}}
}

func flip(b []byte) {
	for i := range b {
		b[i] ^= 0xFF
	}
}

// TestUnpackOwnsValues: changing the PDO data after Unpack, or one
// unpacked value, leaves the other values unchanged; changing the values
// after Pack leaves the packed data unchanged.
func TestUnpackOwnsValues(t *testing.T) {
	m := aliasMapping()
	data := []byte{1, 2, 3, 4, 5, 6, 7}
	vals, err := Unpack(m, data)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{1}, {2, 3}, {4, 5, 6, 7}}
	flip(data)
	flip(vals[0])
	for i := 1; i < len(vals); i++ {
		if !bytes.Equal(vals[i], want[i]) {
			t.Errorf("value %d: % X, want % X", i, vals[i], want[i])
		}
	}
	vals[0] = []byte{1}
	packed, err := Pack(m, vals)
	if err != nil {
		t.Fatal(err)
	}
	wire := bytes.Clone(packed)
	for _, v := range vals {
		flip(v)
	}
	if !bytes.Equal(packed, wire) || !bytes.Equal(packed, []byte{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("packed data % X changed with the values", packed)
	}
}

// TestDecoderOwnsValues: the Raw bytes of decoded values are independent
// of the PDO data and of the values of another Decode; Mapping returns a
// copy.
func TestDecoderOwnsValues(t *testing.T) {
	d := device(t, 5)
	dec, err := NewDecoder(aliasMapping(), d)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{1, 2, 3, 4, 5, 6, 7}
	first, err := dec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := dec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	flip(data)
	for _, v := range first {
		flip(v.Raw)
	}
	want := [][]byte{{1}, {2, 3}, {4, 5, 6, 7}}
	if len(second) != len(want) {
		t.Fatalf("%d values, want %d", len(second), len(want))
	}
	for i, v := range second {
		if !bytes.Equal(v.Raw, want[i]) {
			t.Errorf("value %d of the second Decode: % X, want % X", i, v.Raw, want[i])
		}
	}
	got := dec.Mapping()
	got[0].Bits = 1
	if dec.Mapping()[0].Bits != 8 {
		t.Error("changing the result of Decoder.Mapping changed the decoder's mapping")
	}
	enc, err := NewEncoder(aliasMapping(), d)
	if err != nil {
		t.Fatal(err)
	}
	em := enc.Mapping()
	em[0].Bits = 1
	if enc.Mapping()[0].Bits != 8 {
		t.Error("changing the result of Encoder.Mapping changed the encoder's mapping")
	}
	raw := []byte{9, 8, 7, 6}
	out, err := enc.Encode(uint8(1), int16(2), raw)
	if err != nil {
		t.Fatal(err)
	}
	flip(raw)
	if !bytes.Equal(out, []byte{1, 2, 0, 9, 8, 7, 6}) {
		t.Errorf("encoded % X changed with the value given", out)
	}
}
