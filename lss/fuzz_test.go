// SPDX-License-Identifier: MIT

package lss

import (
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// FuzzSlave feeds frame sequences (9 bytes each: length, then data) to a
// slave: it never panics, answers only with 8-byte frames on the slave
// identifier, stays in a valid state, never takes an invalid node-ID, and
// answers Fastscan only while unconfigured and waiting.
func FuzzSlave(f *testing.F) {
	seq := func(frames ...canbus.Frame) []byte {
		var b []byte
		for _, fr := range frames {
			b = append(b, fr.Len)
			b = append(b, fr.Data[:8]...)
		}
		return b
	}
	f.Add(seq(selectFrames(testAddr)...), uint8(0xFF))
	f.Add(seq(req(CmdSwitchGlobal, 1), req(CmdConfigureNodeID, 5), req(CmdConfigureBitTiming, 0, 2),
		req(CmdStore), req(CmdActivateBitTiming, 1, 0), req(CmdInquireSerial), req(CmdSwitchGlobal, 0)), uint8(0xFF))
	f.Add(seq(fsReq(0, 0x80, 0, 0), fsReq(testAddr.Vendor, 0, 0, 1), fsReq(testAddr.Product, 0, 1, 2),
		fsReq(testAddr.Revision, 0, 2, 3), fsReq(testAddr.Serial, 0, 3, 0)), uint8(0xFF))
	f.Add(seq(req32(CmdIdentifyVendor, testAddr.Vendor), req(CmdIdentifyNonConfig)), uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, node uint8) {
		n := canopen.NodeID(node)
		if !validNodeID(n) {
			n = Unconfigured
		}
		s, err := NewSlave(nil, testAddr, n,
			WithBitTimingHandler(func(sel uint8, b BitTiming) error {
				if b == BitTiming800 {
					return errors.New("no")
				}
				return nil
			}),
			WithStoreHandler(func(st Settings) error {
				if !validNodeID(st.NodeID) {
					t.Fatalf("store of invalid node-ID %d", st.NodeID)
				}
				return nil
			}),
			WithActivateHandler(func(Settings, time.Duration) {}),
			WithNodeIDHandler(func(id canopen.NodeID) error {
				if id == 100 {
					return &ConfigError{Code: CodeImplementSpec, Spec: 1}
				}
				return nil
			}))
		if err != nil {
			t.Fatal(err)
		}
		for len(data) > 0 {
			var fr canbus.Frame
			fr.ID = MasterID
			fr.Len = data[0] % 10 // 0..9: includes invalid lengths
			data = data[1:]
			c := copy(fr.Data[:8], data)
			data = data[c:]
			before := s.State()
			unconf := s.NodeID() == Unconfigured && s.Pending().NodeID == Unconfigured
			r, ok := s.handle(&fr)
			if ok {
				if r.ID != SlaveID || r.Len != 8 || r.Flags != 0 {
					t.Fatalf("response %v", r)
				}
				if Command(r.Data[0]) == CmdIdentifySlave && fr.Data[0] == byte(CmdFastscan) && (!unconf || before != Waiting) {
					t.Fatalf("fastscan answered: unconfigured %v, state %v", unconf, before)
				}
			}
			if st := s.State(); st != Waiting && st != Configuration {
				t.Fatalf("state %d", st)
			}
			if !validNodeID(s.NodeID()) || !validNodeID(s.Pending().NodeID) {
				t.Fatalf("node-ID %d pending %d", s.NodeID(), s.Pending().NodeID)
			}
		}
	})
}

// FuzzResponse feeds arbitrary frames to the master's response decoding:
// matchResponse and configResult never panic, and a match always has the
// requested command.
func FuzzResponse(f *testing.F) {
	f.Add(uint32(SlaveID), uint8(0), []byte{0x11, 1, 0, 0, 0, 0, 0, 0}, uint8(0x11))
	f.Add(uint32(SlaveID), uint8(0), []byte{0x5A, 1, 2, 3, 4}, uint8(0x5A))
	f.Add(uint32(SlaveID), uint8(0), []byte{0x17, 0xFF}, uint8(0x17))
	f.Add(uint32(MasterID), uint8(2), []byte{}, uint8(0x44))
	f.Fuzz(func(t *testing.T, id uint32, flags uint8, data []byte, want uint8) {
		var fr canbus.Frame
		fr.ID, fr.Flags = canbus.ID(id), canbus.Flags(flags)
		fr.Len = uint8(copy(fr.Data[:], data))
		if !matchResponse(&fr, Command(want)) {
			return
		}
		if fr.Data[0] != want || fr.ID != SlaveID {
			t.Fatalf("matched %v for %#x", fr, want)
		}
		err := configResult(&fr)
		var ce *ConfigError
		if err != nil && (!errors.As(err, &ce) || ce.Code == CodeOK || err.Error() == "") {
			t.Fatalf("%v", err)
		}
		_ = u32(&fr)
	})
}
