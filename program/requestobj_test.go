// SPDX-License-Identifier: MIT

package program_test

import (
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
)

// TestControlValueLength: the program control 0x1F51 takes a one-byte
// command. Declared with a type of no fixed size, a write of 0 bytes or
// of more than one is refused; declared with another fixed-size type, the
// dictionary checks the length and the first byte is the command, as
// before.
func TestControlValueLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		dt   canopen.DataType
		data []byte
		want error
	}{
		{"DOMAIN, empty", canopen.Domain, []byte{}, canopen.AbortLengthLow},
		{"DOMAIN, two bytes", canopen.Domain, []byte{0, 0}, canopen.AbortLengthHigh},
		{"DOMAIN, one byte", canopen.Domain, []byte{0}, nil},
		{"OCTET_STRING, empty", canopen.OctetString, nil, canopen.AbortLengthLow},
		{"VISIBLE_STRING, two bytes", canopen.VisibleString, []byte("00"), canopen.AbortLengthHigh},
		{"UNSIGNED16", canopen.Unsigned16, []byte{0, 0}, nil},
		{"UNSIGNED8", canopen.Unsigned8, []byte{0}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := od.New(5)
			if err := d.Add(od.NewArray(program.IndexControl, "Program control", tc.dt, od.AccessRW, 1, nil, od.AccessConst)); err != nil {
				t.Fatal(err)
			}
			dev, err := program.Attach(d, program.Config{Handler: &image{}})
			if err != nil {
				t.Fatal(err)
			}
			defer dev.Close()
			if err := d.Write(program.IndexControl, 1, tc.data); !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("write of % X = %v, want %v", tc.data, err, tc.want)
			}
			want := program.Started
			if tc.want == nil {
				want = program.Stopped
			}
			if st := dev.State(1); st != want {
				t.Errorf("state %s, want %s", st, want)
			}
		})
	}
}
