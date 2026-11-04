// SPDX-License-Identifier: MIT

package master_test

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/sdo"
)

// fuzzFrames decodes fuzz input into raw CAN frames: records of a flags
// byte (bit 0 remote, bit 1 extended, bit 2 CAN FD), an 11-bit
// identifier (little endian), a length byte taken as is (a classical
// frame may claim up to 255 bytes) and min(length, 64) data bytes. At
// most 64 frames are decoded.
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

func fuzzRecord(flags byte, id canbus.ID, data ...byte) []byte {
	return append([]byte{flags, byte(id), byte(id >> 8), byte(len(data))}, data...)
}

// FuzzMasterFrames feeds arbitrary frame sequences into a running master
// (heartbeat supervision of two nodes, an event handler that calls back
// into the master) with a real node on the bus: nothing may panic, the
// master tracks at most 127 nodes, it still reads the node over SDO, and
// it closes promptly.
func FuzzMasterFrames(f *testing.F) {
	cat := func(rs ...[]byte) []byte {
		var b []byte
		for _, r := range rs {
			b = append(b, r...)
		}
		return b
	}
	for _, s := range [][]byte{
		nil,
		// Boot-up, heartbeats of every state, a state change of node 5.
		cat(fuzzRecord(0, 0x706, 0x00), fuzzRecord(0, 0x706, 0x7F), fuzzRecord(0, 0x706, 0x05), fuzzRecord(0, 0x706, 0x04)),
		cat(fuzzRecord(0, 0x705, 0x05), fuzzRecord(0, 0x77F, 0x00), fuzzRecord(0, 0x701, 0x85)),
		// Emergencies: classical, short, long, from every node.
		cat(fuzzRecord(0, 0x86, 0x00, 0x10, 0x01, 1, 2, 3, 4, 5), fuzzRecord(0, 0x86, 0x00), fuzzRecord(0, 0xFF, make([]byte, 64)...)),
		// SDO responses nobody asked for, aborts, segments.
		cat(fuzzRecord(0, 0x585, 0x43, 0, 0x10, 0, 1, 2, 3, 4), fuzzRecord(0, 0x585, 0x80, 0, 0x10, 0, 0, 0, 0, 8), fuzzRecord(0, 0x585, 0x01)),
		// LSS answers, SYNC, TIME, NMT for node 5 and all.
		cat(fuzzRecord(0, 0x7E4, 0x5A, 1, 0, 0, 0), fuzzRecord(0, 0x7E4, 0x4F), fuzzRecord(0, 0x80), fuzzRecord(0, 0x100, make([]byte, 6)...)),
		cat(fuzzRecord(0, 0, 0x02, 5), fuzzRecord(0, 0, 0x81, 0), fuzzRecord(0, 0, 0x01, 0)),
		// Remote, extended and CAN FD frames on the master's identifiers.
		cat(fuzzRecord(1, 0x706), fuzzRecord(2, 0x706, 0x05), fuzzRecord(4, 0x86, make([]byte, 20)...), fuzzRecord(1, 0x585)),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		frames := fuzzFrames(in)
		vb := virtual.New(virtual.Config{Name: "fuzz-master"})
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
		mb, nb := open(), open()
		defer mb.Close()
		defer nb.Close()
		n, err := node.New(nb, nettest.Dict(t, 5, nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Start(); err != nil {
			t.Fatal(err)
		}
		defer n.Close()
		var m *master.Master
		m, err = master.New(mb,
			master.WithSDOOptions(sdo.WithTimeout(50*time.Millisecond), sdo.WithRetries(2)),
			master.WithEventHandler(func(ev master.Event) {
				_ = m.State(ev.Node)
				_, _ = m.LastEMCY(ev.Node)
			}))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = m.Close()
			}
		}()
		for _, id := range []canopen.NodeID{5, 6} {
			if err := m.Monitor(id, 30*time.Millisecond); err != nil {
				t.Fatal(err)
			}
		}
		ctx := context.Background()
		for _, fr := range frames {
			if err := vb.InjectRaw(ctx, fr); err != nil {
				t.Fatal(err)
			}
		}
		if got := len(m.Nodes()); got > int(canopen.MaxNodeID) {
			t.Fatalf("master tracks %d nodes", got)
		}
		c, err := m.SDO(5)
		if err != nil {
			t.Fatal(err)
		}
		var v uint64
		for range 5 {
			// The input may have stopped or reset the node: bring it back.
			if err = m.PreOperational(ctx, 5); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
			if n.State() != canopen.StatePreOperational {
				err = errNotReady
				continue
			}
			if v, err = c.ReadUint(ctx, 0x1000, 0); err == nil {
				break
			}
		}
		if err != nil || uint32(v) != nettest.DeviceType {
			t.Fatalf("master cannot read the node after %d hostile frames: 0x%X, %v", len(frames), v, err)
		}
		done := make(chan struct{})
		go func() { _ = m.Close(); close(done) }()
		select {
		case <-done:
			closed = true
		case <-time.After(2 * time.Second):
			t.Fatal("master.Close hangs after hostile frames")
		}
	})
}

var errNotReady = errors.New("node not pre-operational yet")
