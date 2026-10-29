// SPDX-License-Identifier: MIT

package cia402

import (
	"testing"
	"time"

	"github.com/otfabric/go-canopen/od"
)

// FuzzDrive feeds random controlword, mode, option code, fault and step
// sequences to a drive. The drive must never panic, its statusword must
// always decode to a valid state matching the drive's state, and every
// transition it takes must be a transition of the CiA 402 state diagram
// starting in the state the previous one ended in.
func FuzzDrive(f *testing.F) {
	f.Add([]byte{0, 0x06, 0, 0x07, 0, 0x0F, 2, 10, 1, 1, 0, 0x1F})
	f.Add([]byte{0, 0x06, 0, 0x07, 0, 0x0F, 3, 0, 2, 5, 0, 0x80, 0, 0})
	f.Add([]byte{4, 6, 0, 0x06, 0, 0x0F, 0, 0x0F, 0, 0x02, 2, 3, 0, 0x0F})
	f.Add([]byte{1, 3, 0, 0x06, 0, 0x07, 0, 0x0F, 5, 0x10, 2, 50, 0, 0x07})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 512 {
			ops = ops[:512]
		}
		dict := od.New(1)
		last := NotReadyToSwitchOn
		var bad string
		drive, err := NewDrive(dict, DriveConfig{OnTransition: func(from, to State, n int) {
			if bad == "" && (from != last || !ValidTransition(n, from, to)) {
				bad = from.String() + " -> " + to.String()
			}
			last = to
		}})
		if err != nil {
			t.Fatal(err)
		}
		defer drive.Close()
		for i := 0; i+1 < len(ops); i += 2 {
			arg := ops[i+1]
			switch ops[i] % 8 {
			case 0: // remote controlword, low byte; bit 8 (halt) from bit 0 of the next op
				cw := uint16(arg)
				if i+2 < len(ops) && ops[i+2]&0x80 != 0 {
					cw |= CtrlHalt
				}
				_ = dict.Write(IndexControlword, 0, u16(cw))
			case 1:
				_ = dict.Write(IndexModesOfOperation, 0, []byte{arg % 12})
			case 2:
				drive.Step(time.Duration(arg) * time.Millisecond)
			case 3:
				drive.InjectFault(uint16(arg) << 8)
			case 4:
				_ = dict.Write(IndexQuickStopOptionCode, 0, i16(int16(arg%10)))
			case 5:
				_ = dict.Write(IndexTargetPosition, 0, i32(int32(int8(arg))*100))
				_ = dict.Write(IndexTargetVelocity, 0, i32(int32(int8(arg))*100))
				_ = dict.Write(IndexTargetTorque, 0, i16(int16(int8(arg))))
			case 6: // local controlword write
				_ = dict.Set(IndexControlword, 0, u16(uint16(arg)))
			case 7:
				_ = dict.Write([]uint16{IndexShutdownOptionCode, IndexDisableOperationOptionCode,
					IndexFaultReactionOptionCode, IndexHaltOptionCode}[arg%4], 0, i16(int16(arg>>2)%3))
			}
			b, _ := dict.Read(IndexStatusword, 0)
			sw := uint16(b[0]) | uint16(b[1])<<8
			if s := DecodeStatusword(sw); s == Unknown || s != drive.State() {
				t.Fatalf("op %d: statusword 0x%04X (%v), drive state %v", i/2, sw, s, drive.State())
			}
			if bad != "" {
				t.Fatalf("op %d: transition %s not in the state diagram", i/2, bad)
			}
		}
	})
}
