// SPDX-License-Identifier: MIT

package node_test

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/sdo"
)

// fuzzFrames decodes fuzz input into raw CAN frames, as a hostile node or
// a broken controller may put them on the bus: records of a flags byte
// (bit 0 remote, bit 1 extended, bit 2 CAN FD), an 11-bit identifier
// (little endian), a length byte taken as is (up to 255: a classical
// frame may claim more than 8 bytes) and min(length, 64) data bytes.
// At most 64 frames are decoded.
func fuzzFrames(in []byte) []canbus.Frame {
	var out []canbus.Frame
	for len(in) >= 4 && len(out) < 64 {
		var f canbus.Frame
		fl := in[0]
		f.ID = canbus.ID(binary.LittleEndian.Uint16(in[1:])) & canbus.MaxStandardID
		if fl&1 != 0 {
			f.Flags |= canbus.FlagRemote
		}
		if fl&2 != 0 {
			f.Flags |= canbus.FlagExtended
		}
		if fl&4 != 0 {
			f.Flags |= canbus.FlagFD
		}
		f.Len = in[3]
		in = in[4:]
		n := min(int(f.Len), canbus.MaxFDLen, len(in))
		copy(f.Data[:], in[:n])
		in = in[n:]
		out = append(out, f)
	}
	return out
}

// fuzzRecord encodes one frame of fuzzFrames' input.
func fuzzRecord(flags byte, id canbus.ID, data ...byte) []byte {
	b := []byte{flags, byte(id), byte(id >> 8), byte(len(data))}
	return append(b, data...)
}

// fuzzSDO is an SDO request to node 5.
func fuzzSDO(b0 byte, index uint16, sub uint8, v uint32) []byte {
	d := make([]byte, 8)
	d[0] = b0
	binary.LittleEndian.PutUint16(d[1:], index)
	d[3] = sub
	binary.LittleEndian.PutUint32(d[4:], v)
	return fuzzRecord(0, 0x605, d...)
}

func fuzzSeeds() [][]byte {
	cat := func(rs ...[]byte) []byte {
		var b []byte
		for _, r := range rs {
			b = append(b, r...)
		}
		return b
	}
	seg := func(b0 byte) []byte { return fuzzRecord(0, 0x605, b0, 1, 2, 3, 4, 5, 6, 7) }
	return [][]byte{
		nil,
		// Expedited read and write, a write of the heartbeat time.
		fuzzSDO(0x40, 0x1000, 0, 0),
		fuzzSDO(0x23, nettest.IdxParam, 0, 0xCAFEBABE),
		fuzzSDO(0x2B, 0x1017, 0, 10),
		// A segmented download, then a segmented upload.
		cat(fuzzSDO(0x21, nettest.IdxDomain, 0, 14), seg(0x00), seg(0x11)),
		cat(fuzzSDO(0x40, 0x1008, 0, 0), fuzzRecord(0, 0x605, 0x60), fuzzRecord(0, 0x605, 0x70)),
		// A block download and a block upload.
		cat(fuzzSDO(0xC6, nettest.IdxDomain, 0, 7), seg(0x81), fuzzRecord(0, 0x605, 0xC5, 0, 0)),
		cat(fuzzSDO(0xA4, 0x1008, 0, 0x7F), fuzzRecord(0, 0x605, 0xA3), fuzzRecord(0, 0x605, 0xA2, 1, 0x7F)),
		// NMT: start, stop, pre-operational, reset communication, all nodes.
		cat(fuzzRecord(0, 0, 0x01, 5), fuzzRecord(0, 0, 0x02, 0), fuzzRecord(0, 0, 0x80, 5), fuzzRecord(0, 0, 0x82, 0)),
		// SYNC, TIME, an RPDO, a heartbeat, node guarding, an EMCY.
		cat(fuzzRecord(0, 0x80), fuzzRecord(0, 0x80, 7), fuzzRecord(0, 0x100, 1, 2, 3, 4, 5, 6),
			fuzzRecord(0, 0x205, 1, 2, 3, 4), fuzzRecord(0, 0x701, 5), fuzzRecord(1, 0x705), fuzzRecord(0, 0x81, 0, 0x10, 1)),
		// LSS switch state global and an inquiry.
		cat(fuzzRecord(0, 0x7E5, 0x04, 1), fuzzRecord(0, 0x7E5, 0x5E)),
		// A classical frame claiming 64 bytes, remote and extended SDO frames.
		cat(fuzzRecord(0, 0x605, make([]byte, 64)...), fuzzRecord(1, 0x605), fuzzRecord(2, 0x605, 0x40, 0, 0x10, 0, 0, 0, 0, 0)),
		// An abort storm and a CAN FD frame on the SDO identifier.
		cat(fuzzSDO(0x80, 0, 0, 0), fuzzSDO(0x80, 0x1000, 0, 0x08000000), fuzzRecord(4, 0x605, make([]byte, 12)...)),
	}
}

// FuzzNodeFrames feeds arbitrary frame sequences to a started node with a
// real dictionary: nothing may panic, and afterwards (once the node is
// brought back to pre-operational) its SDO server must answer a
// well-formed read and the node must close promptly.
func FuzzNodeFrames(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		frames := fuzzFrames(in)
		vb := virtual.New(virtual.Config{Name: "fuzz-node"})
		defer vb.Close()
		open := func() *canbus.Bus {
			p, err := vb.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := canbus.NewBus(p)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		nb, cb := open(), open()
		defer nb.Close()
		defer cb.Close()
		d := nettest.Dict(t, 5, nil)
		n, err := node.New(nb, d, node.WithSDOServerOptions(sdo.WithTimeout(20*time.Millisecond)))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Start(); err != nil {
			t.Fatal(err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = n.Close()
			}
		}()
		ctx := context.Background()
		for _, fr := range frames {
			if err := vb.InjectRaw(ctx, fr); err != nil {
				t.Fatal(err)
			}
		}
		c, err := sdo.NewClient(cb, 5, sdo.WithTimeout(50*time.Millisecond), sdo.WithRetries(2))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var v uint64
		for range 5 {
			fuzzRecover(t, vb, n)
			if v, err = c.ReadUint(ctx, 0x1000, 0); err == nil {
				break
			}
		}
		if err != nil || uint32(v) != nettest.DeviceType {
			t.Fatalf("SDO server unusable after %d hostile frames: 0x%X, %v", len(frames), v, err)
		}
		done := make(chan struct{})
		go func() { _ = n.Close(); close(done) }()
		select {
		case <-done:
			closed = true
		case <-time.After(2 * time.Second):
			t.Fatal("node.Close hangs after hostile frames")
		}
	})
}

// fuzzRecover brings the node back to pre-operational (the fuzz input
// may have stopped or reset it) with an NMT command to it, sent after
// the input's frames (the NMT slave handles its frames in order).
func fuzzRecover(t *testing.T, vb *virtual.Bus, n *node.Node) {
	t.Helper()
	var f canbus.Frame
	f.SetPayload([]byte{byte(canopen.NMTEnterPreOperational), 5})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := vb.InjectRaw(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if n.State() == canopen.StatePreOperational {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node stuck in %v", n.State())
		}
	}
}
