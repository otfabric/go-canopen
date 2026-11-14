// SPDX-License-Identifier: MIT

package usdo

import (
	"bytes"
	"testing"
)

// TestDecodeOwnsData (safety.md, aliasing): the Data of a decoded message
// is a copy of the payload, and Encode does not keep the message's Data:
// changing the input or the output afterwards changes nothing else.
func TestDecodeOwnsData(t *testing.T) {
	seg := make([]byte, SegmentSize)
	for i := range seg {
		seg[i] = byte(i + 1)
	}
	for _, m := range []Message{
		{Dest: 5, Cmd: CmdDownloadExpedited, Session: 1, Index: 0x2000, SubIndex: 1, DataType: 7, Data: []byte{1, 2, 3, 4}},
		{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 1, Data: seg},
		{Dest: 5, Cmd: CmdDownloadLast, Session: 3, Data: []byte{9, 8, 7}},
	} {
		t.Run(m.Cmd.String(), func(t *testing.T) {
			want := bytes.Clone(m.Data)
			b, err := m.Encode()
			if err != nil {
				t.Fatal(err)
			}
			wire := bytes.Clone(b)
			for i := range m.Data {
				m.Data[i] ^= 0xFF
			}
			if !bytes.Equal(b, wire) {
				t.Fatalf("the encoded message changed with the message's Data")
			}
			d, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(d.Data, want) {
				t.Fatalf("decoded % X, want % X", d.Data, want)
			}
			for i := range b {
				b[i] ^= 0xFF
			}
			if !bytes.Equal(d.Data, want) {
				t.Fatalf("decoded data changed with the input: % X, want % X", d.Data, want)
			}
			again, err := Decode(wire)
			if err != nil {
				t.Fatal(err)
			}
			for i := range d.Data {
				d.Data[i] ^= 0xFF
			}
			if !bytes.Equal(again.Data, want) {
				t.Fatalf("a second decode shares data with the first: % X, want % X", again.Data, want)
			}
		})
	}
}
