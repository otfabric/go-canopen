// SPDX-License-Identifier: MIT

package cia402

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/otfabric/go-canopen"
	rec "github.com/otfabric/go-canopen/internal/capture"
	"github.com/otfabric/go-canopen/pdo"
)

// recordedTransitions follows the CiA 402 state of every drive of a Copley
// log: the statusword comes from TPDO1, decoded with the mapping the
// master read or wrote over SDO (0x1A00), the command from the last
// controlword the master wrote (0x6040 over SDO, confirmed by the drive).
// Each state change is reported with the transition Next gives for the
// command, or "none" when the CiA 402 diagram has none.
func recordedTransitions(t *testing.T, rel string) []string {
	t.Helper()
	_, recs, err := rec.LoadExcerpt(rel)
	if err != nil {
		t.Fatal(err)
	}
	type drive struct {
		mapping map[uint8]uint32 // 0x1A00 sub-index → value
		pending []byte           // SDO request awaiting its answer
		cw      uint16
		hasCW   bool
		state   State
	}
	drives := map[canopen.NodeID]*drive{}
	get := func(n canopen.NodeID) *drive {
		if drives[n] == nil {
			drives[n] = &drive{mapping: map[uint8]uint32{}}
		}
		return drives[n]
	}
	var out []string
	for _, r := range recs {
		f := r.Frame
		d := f.Payload()
		switch {
		case f.ID > 0x600 && f.ID < 0x680 && len(d) == 8:
			get(canopen.NodeID(f.ID - 0x600)).pending = d
		case f.ID > 0x580 && f.ID < 0x600 && len(d) == 8:
			dr := get(canopen.NodeID(f.ID - 0x580))
			req := dr.pending
			dr.pending = nil
			if req == nil || req[1] != d[1] || req[2] != d[2] || req[3] != d[3] {
				continue
			}
			index := uint16(d[1]) | uint16(d[2])<<8
			val := d[4:8]
			if req[0]&0xE0 == 0x20 { // confirmed download: the request's data
				val = req[4:8]
			}
			v := uint32(val[0]) | uint32(val[1])<<8 | uint32(val[2])<<16 | uint32(val[3])<<24
			switch {
			case index == 0x1A00:
				dr.mapping[d[3]] = v
			case index == 0x6040 && req[0]&0xE0 == 0x20:
				dr.cw, dr.hasCW = uint16(v), true
			}
		case f.ID > 0x180 && f.ID < 0x200 && !f.IsRemote():
			n := canopen.NodeID(f.ID - 0x180)
			dr := get(n)
			var m pdo.Mapping
			for i := uint8(1); i <= uint8(dr.mapping[0]); i++ {
				m = append(m, pdo.ParseEntry(dr.mapping[i]))
			}
			i := slices.IndexFunc(m, func(e pdo.Entry) bool { return e.Index == 0x6041 && e.SubIndex == 0 })
			if i < 0 {
				continue
			}
			vals, err := pdo.Unpack(m, d)
			if err != nil {
				t.Fatalf("%s line %d: %v", rel, r.Line, err)
			}
			st := DecodeStatusword(uint16(vals[i][0]) | uint16(vals[i][1])<<8)
			if st == dr.state {
				continue
			}
			s := fmt.Sprintf("node %d: %v", n, st)
			if dr.state != Unknown {
				cmd := DecodeControlword(dr.cw)
				to, nr, ok := Next(dr.state, cmd)
				switch {
				case st == Fault || st == FaultReactionActive:
					s = fmt.Sprintf("node %d: %v -> %v (error: transitions 13, 14)", n, dr.state, st)
				case !dr.hasCW:
					s = fmt.Sprintf("node %d: %v -> %v (no controlword written)", n, dr.state, st)
				case ok && to == st:
					s = fmt.Sprintf("node %d: %v -[%v]-> %v (transition %d)", n, dr.state, cmd, st, nr)
				case ok:
					s = fmt.Sprintf("node %d: %v -[%v]-> %v (Next: %v)", n, dr.state, cmd, st, to)
				default:
					s = fmt.Sprintf("node %d: %v -[%v]-> %v (none)", n, dr.state, cmd, st)
				}
			}
			dr.state = st
			out = append(out, s)
		}
	}
	return out
}

// TestCaptureCopleyStateMachine compares the CiA 402 state changes of the
// recorded Copley drives with the state diagram. Fault reset is
// transition 15, as in CiA 402. The drives then go from Switch on
// disabled straight to Operation enabled on a single Enable operation
// controlword (0x1F, 0x0F), where the diagram needs Shutdown, Switch on
// and Enable operation (transitions 2, 3, 4): Copley's drives accept the
// shortcut. Next, which follows the diagram, finds no transition for it;
// a Drive of this package stays in Switch on disabled. This is recorded
// vendor behaviour, not a defect of either.
func TestCaptureCopleyStateMachine(t *testing.T) {
	for _, c := range []struct {
		rel  string
		want []string
	}{
		{"copley/absolute-move.excerpt.json", []string{
			"node 1: Fault",
			"node 1: Fault -[Fault reset]-> Switch on disabled (transition 15)",
			"node 1: Switch on disabled -[Enable operation]-> Operation enabled (none)",
			// The master disables the drive, stops guarding, and the drive
			// reports the life guarding error (EMCY 0x8130) and faults.
			"node 1: Operation enabled -[Disable voltage]-> Switch on disabled (transition 9)",
			"node 1: Switch on disabled -> Fault (error: transitions 13, 14)",
		}},
		{"copley/pvt-move.excerpt.json", []string{
			"node 1: Fault",
			"node 1: Fault -[Fault reset]-> Switch on disabled (transition 15)",
			"node 1: Switch on disabled -[Enable operation]-> Operation enabled (none)",
		}},
	} {
		got := recordedTransitions(t, c.rel)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.rel, got, c.want)
		}
	}
}

// TestCaptureDriveParity sends this package's Drive the controlwords the
// recorded master wrote in Switch on disabled (0x1F: Absolute Move line
// 467; 0x0F: PVT Move line 719). The Copley drive accepted them (SDO
// answer 0x60) and went to Operation enabled; the Drive follows the CiA
// 402 diagram: it stays in Switch on disabled, and with StrictControlword
// refuses the write with abort 0x08000022 (device state).
func TestCaptureDriveParity(t *testing.T) {
	for _, cw := range []uint16{0x1F, 0x0F} {
		td := newTestDrive(t, DriveConfig{})
		td.reach(SwitchOnDisabled)
		td.cw(cw)
		if s := td.state(); s != SwitchOnDisabled {
			t.Errorf("cw 0x%02X: %v", cw, s)
		}
		td = newTestDrive(t, DriveConfig{StrictControlword: true})
		td.reach(SwitchOnDisabled)
		if err := td.dict.Write(IndexControlword, 0, u16(cw)); !errors.Is(err, canopen.AbortDeviceState) {
			t.Errorf("strict cw 0x%02X: %v", cw, err)
		}
	}
}
