// SPDX-License-Identifier: MIT

package sdo

import (
	"strconv"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/od"
)

// TestSizeField checks the 32-bit size of initiate frames: data of
// 4 GiB and more (and negative counts) must not be truncated.
func TestSizeField(t *testing.T) {
	type tc struct {
		n    int
		want uint32
		ok   bool
	}
	cases := []tc{{0, 0, true}, {5, 5, true}, {-1, 0, false}}
	if strconv.IntSize == 64 {
		big := int64(1) << 32 // not a constant: the file builds on 32-bit
		cases = append(cases, tc{int(big - 1), 0xFFFFFFFF, true}, tc{int(big), 0, false}, tc{int(big + 7), 0, false})
	}
	for _, c := range cases {
		if got, ok := sizeField(c.n); got != c.want || ok != c.ok {
			t.Errorf("sizeField(%d) = %d, %v; want %d, %v", c.n, got, ok, c.want, c.ok)
		}
	}
	if err := errTooLarge(1); err == nil {
		t.Fatal("no error")
	}
}

func TestU8(t *testing.T) {
	for n, want := range map[int]uint8{-3: 0, 0: 0, 7: 7, 127: 127, 255: 255, 256: 255, 1000: 255} {
		if got := u8(n); got != want {
			t.Errorf("u8(%d) = %d, want %d", n, got, want)
		}
	}
}

// TestResolveCOBIDsWide: a 0x1200 entry wider than UNSIGNED32 was
// truncated into a COB-ID; it now falls back to the default COB-IDs.
func TestResolveCOBIDsWide(t *testing.T) {
	for _, sub := range []uint8{1, 2} {
		d := testDict(t)
		d.Remove(od.IndexSDOServer)
		rx, tx := uint64(0x123), uint64(0x124)
		if sub == 1 {
			rx |= 1 << 32
		} else {
			tx |= 1 << 32
		}
		if err := d.Add(od.NewRecord(od.IndexSDOServer, "SDO server parameter",
			od.Sub(1, "rx", canopen.Unsigned64, od.AccessRO, rx),
			od.Sub(2, "tx", canopen.Unsigned64, od.AccessRO, tx))); err != nil {
			t.Fatal(err)
		}
		a, _ := testbus.Pair(t)
		s, err := NewServer(a, d)
		if err != nil {
			t.Fatal(err)
		}
		c2s, s2c, err := s.resolveCOBIDs()
		if err != nil {
			t.Fatal(err)
		}
		if c2s != canopen.COBID(0x600)+canopen.COBID(testNode) || s2c != canopen.COBID(0x580)+canopen.COBID(testNode) {
			t.Fatalf("sub %d: COB-IDs %v/%v, want the defaults", sub, c2s, s2c)
		}
	}
}

// TestResetCommandFrames: any ID-0 frame of 2 bytes or more (remote,
// error, CAN FD, 3..64 bytes) fenced the server for good, while the NMT
// slave ignored it, so no reset and no boot-up reopened the server. The
// fence now takes exactly the frames the slave executes.
func TestResetCommandFrames(t *testing.T) {
	a, _ := testbus.Pair(t)
	s, err := NewServer(a, testDict(t), WithResetFence(testNode))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(flags canbus.Flags, n uint8, data ...byte) canbus.Frame {
		f := canbus.Frame{ID: canopen.FuncNMT, Flags: flags, Len: n}
		copy(f.Data[:], data)
		return f
	}
	rn, rc := byte(canopen.NMTResetNode), byte(canopen.NMTResetCommunication)
	tests := []struct {
		name string
		f    canbus.Frame
		want bool
	}{
		{"reset node", mk(0, 2, rn, byte(testNode)), true},
		{"reset communication, all", mk(0, 2, rc, 0), true},
		{"reset other node", mk(0, 2, rn, 6), false},
		{"start", mk(0, 2, byte(canopen.NMTStart), byte(testNode)), false},
		{"1 byte", mk(0, 1, rn), false},
		{"3 bytes", mk(0, 3, rn, byte(testNode)), false},
		{"8 bytes", mk(0, 8, rn, byte(testNode)), false},
		{"64 bytes FD", mk(canbus.FlagFD, 64, rn, byte(testNode)), false},
		{"2 bytes FD", mk(canbus.FlagFD, 2, rn, byte(testNode)), false},
		{"remote", mk(canbus.FlagRemote, 2, rn, byte(testNode)), false},
		{"error", mk(canbus.FlagError, 2, rn, byte(testNode)), false},
		{"extended", mk(canbus.FlagExtended, 2, rn, byte(testNode)), false},
		{"node 0x80", mk(0, 2, rn, 0x80), false},
	}
	for _, tt := range tests {
		f := tt.f
		if got := s.resetCommand(&f); got != tt.want {
			t.Errorf("%s: fence %v, want %v", tt.name, got, tt.want)
		}
		// The NMT slave of a classical node must agree.
		cmd, node, err := nmt.ParseCommand(&f)
		slave := err == nil && (cmd == canopen.NMTResetNode || cmd == canopen.NMTResetCommunication) && (node == 0 || node == testNode)
		if slave != tt.want {
			t.Errorf("%s: NMT slave resets %v, want %v", tt.name, slave, tt.want)
		}
	}
}
