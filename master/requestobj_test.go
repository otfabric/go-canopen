// SPDX-License-Identifier: MIT

package master_test

import (
	"errors"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/od"
)

// TestRequestObjectValueLength: the request objects 0x1F82 and 0x1F83
// take a one-byte value. Declared with a type of no fixed size, a write of
// 0 bytes or of more than one is refused; one byte is executed.
func TestRequestObjectValueLength(t *testing.T) {
	for _, dt := range []canopen.DataType{canopen.Domain, canopen.OctetString} {
		d, err := od.Standard(od.Config{NodeID: masterID})
		if err != nil {
			t.Fatal(err)
		}
		for _, idx := range []uint16{master.IndexRequestNMT, master.IndexRequestGuarding} {
			if err := d.Add(od.NewArray(idx, "request", dt, od.AccessRW, int(canopen.MaxNodeID)+1, nil, od.AccessConst)); err != nil {
				t.Fatal(err)
			}
		}
		if err := master.AddManagerObjects(d); err != nil {
			t.Fatal(err)
		}
		set32(t, d, master.IndexNMTStartup, 0, master.StartupMaster)
		assign(t, d, 5, master.NewAssignment(master.AssignSlave, 0, 0))
		m, err := master.New(testbus.Ports(t, 1)[0], master.WithDictionary(d))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name  string
			index uint16
			sub   uint8
			data  []byte
			want  error
		}{
			{"NMT, empty", master.IndexRequestNMT, 5, []byte{}, canopen.AbortLengthLow},
			{"NMT, two bytes", master.IndexRequestNMT, 5, []byte{4, 0}, canopen.AbortLengthHigh},
			{"NMT all, empty", master.IndexRequestNMT, 0x80, nil, canopen.AbortLengthLow},
			{"NMT, one byte", master.IndexRequestNMT, 5, []byte{4}, nil},
			{"guarding, empty", master.IndexRequestGuarding, 5, []byte{}, canopen.AbortLengthLow},
			{"guarding, two bytes", master.IndexRequestGuarding, 5, []byte{0, 0}, canopen.AbortLengthHigh},
			{"guarding all, empty", master.IndexRequestGuarding, 0x80, nil, canopen.AbortLengthLow},
			{"guarding, one byte", master.IndexRequestGuarding, 5, []byte{0}, nil},
		} {
			err := d.Write(tc.index, tc.sub, tc.data)
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Errorf("%s %s: write of % X = %v, want %v", dt, tc.name, tc.data, err, tc.want)
			}
		}
		_ = m.Close()
	}
}
