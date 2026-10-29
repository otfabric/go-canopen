// SPDX-License-Identifier: MIT

package cia402

import (
	"slices"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

var allStates = []State{NotReadyToSwitchOn, SwitchOnDisabled, ReadyToSwitchOn, SwitchedOn,
	OperationEnabled, QuickStopActive, FaultReactionActive, Fault}

var allCommands = []Command{Shutdown, SwitchOn, SwitchOnAndEnable, DisableVoltage, QuickStop,
	DisableOperation, EnableOperation, FaultReset}

func TestDecodeStatusword(t *testing.T) {
	tests := []struct {
		sw   uint16
		want State
	}{
		{0x0000, NotReadyToSwitchOn},
		{0x0020, NotReadyToSwitchOn},
		{0xFFB0 &^ 0x40, NotReadyToSwitchOn},
		{0x0040, SwitchOnDisabled},
		{0x0060, SwitchOnDisabled},
		{0x0021, ReadyToSwitchOn},
		{0x0031, ReadyToSwitchOn},
		{0x0023, SwitchedOn},
		{0x0027, OperationEnabled},
		{0x1637, OperationEnabled},
		{0x0007, QuickStopActive},
		{0x0017, QuickStopActive},
		{0x000F, FaultReactionActive},
		{0x002F, FaultReactionActive},
		{0x0008, Fault},
		{0x0028, Fault},
		{0x0001, Unknown},
		{0x0041, Unknown},
		{0x0003, Unknown},
	}
	for _, tt := range tests {
		if got := DecodeStatusword(tt.sw); got != tt.want {
			t.Errorf("DecodeStatusword(0x%04X) = %v, want %v", tt.sw, got, tt.want)
		}
	}
	for _, s := range allStates {
		if got := DecodeStatusword(StateBits(s)); got != s {
			t.Errorf("StateBits(%v) = 0x%04X decodes as %v", s, StateBits(s), got)
		}
		if got := DecodeStatusword(StateBits(s) | 0xFF80); got != s {
			t.Errorf("StateBits(%v) with high bits decodes as %v", s, got)
		}
	}
	if StateBits(Unknown) != 0 {
		t.Error("StateBits(Unknown) != 0")
	}
}

func TestStrings(t *testing.T) {
	if NotReadyToSwitchOn.String() != "Not ready to switch on" || State(99).String() != "State(99)" {
		t.Error("State.String")
	}
	if SwitchOnAndEnable.String() != "Switch on + enable operation" || Command(0).String() != "None" || Command(99).String() != "Command(99)" {
		t.Error("Command.String")
	}
	for _, s := range allStates {
		if strings.HasPrefix(s.String(), "State(") {
			t.Errorf("state %d has no name", s)
		}
	}
	for _, c := range allCommands {
		if strings.HasPrefix(c.String(), "Command(") {
			t.Errorf("command %d has no name", c)
		}
	}
}

func TestControlwordEncoding(t *testing.T) {
	tests := []struct {
		c    Command
		bits uint16
		dec  Command
	}{
		{Shutdown, 0x06, Shutdown},
		{SwitchOn, 0x07, SwitchOn},
		{SwitchOnAndEnable, 0x0F, EnableOperation},
		{DisableVoltage, 0x00, DisableVoltage},
		{QuickStop, 0x02, QuickStop},
		{DisableOperation, 0x07, SwitchOn},
		{EnableOperation, 0x0F, EnableOperation},
		{FaultReset, 0x80, FaultReset},
		{0, 0, DisableVoltage},
	}
	for _, tt := range tests {
		if got := tt.c.Bits(); got != tt.bits {
			t.Errorf("%v.Bits() = 0x%02X, want 0x%02X", tt.c, got, tt.bits)
		}
		cw := tt.c.Controlword(0xFFFF)
		if cw&^CtrlCommandMask != 0xFFFF&^CtrlCommandMask || cw&CtrlCommandMask != tt.bits {
			t.Errorf("%v.Controlword(0xFFFF) = 0x%04X", tt.c, cw)
		}
		if got := DecodeControlword(tt.bits | CtrlHalt | CtrlOMS4); got != tt.dec {
			t.Errorf("DecodeControlword(0x%04X) = %v, want %v", tt.bits, got, tt.dec)
		}
	}
	// Patterns with the don't-care bits set.
	for cw, want := range map[uint16]Command{0x05: DisableVoltage, 0x0D: DisableVoltage, 0x0B: QuickStop, 0x0E: Shutdown, 0x8F: FaultReset} {
		if got := DecodeControlword(cw); got != want {
			t.Errorf("DecodeControlword(0x%02X) = %v, want %v", cw, got, want)
		}
	}
}

// diagram is the CiA 402 state diagram written out independently of the
// transitions table: state × command → (state, transition).
var diagram = map[State]map[Command][2]int{
	SwitchOnDisabled: {Shutdown: {int(ReadyToSwitchOn), 2}},
	ReadyToSwitchOn: {SwitchOn: {int(SwitchedOn), 3}, DisableOperation: {int(SwitchedOn), 3},
		SwitchOnAndEnable: {int(SwitchedOn), 3}, EnableOperation: {int(SwitchedOn), 3},
		DisableVoltage: {int(SwitchOnDisabled), 7}, QuickStop: {int(SwitchOnDisabled), 7}},
	SwitchedOn: {EnableOperation: {int(OperationEnabled), 4}, SwitchOnAndEnable: {int(OperationEnabled), 4},
		Shutdown: {int(ReadyToSwitchOn), 6}, DisableVoltage: {int(SwitchOnDisabled), 10}, QuickStop: {int(SwitchOnDisabled), 10}},
	OperationEnabled: {DisableOperation: {int(SwitchedOn), 5}, SwitchOn: {int(SwitchedOn), 5},
		Shutdown: {int(ReadyToSwitchOn), 8}, DisableVoltage: {int(SwitchOnDisabled), 9}, QuickStop: {int(QuickStopActive), 11}},
	QuickStopActive: {DisableVoltage: {int(SwitchOnDisabled), 12}, EnableOperation: {int(OperationEnabled), 16},
		SwitchOnAndEnable: {int(OperationEnabled), 16}},
	Fault: {FaultReset: {int(SwitchOnDisabled), 15}},
}

func TestNext(t *testing.T) {
	for _, s := range append(allStates, Unknown) {
		for _, c := range append(allCommands, 0) {
			to, n, ok := Next(s, c)
			want, wok := diagram[s][c]
			if ok != wok || (ok && (int(to) != want[0] || n != want[1])) || (!ok && to != s) {
				t.Errorf("Next(%v, %v) = %v, %d, %v; want %v, %d, %v", s, c, to, n, ok, State(want[0]), want[1], wok)
			}
			if ok && !ValidTransition(n, s, to) {
				t.Errorf("ValidTransition(%d, %v, %v) = false", n, s, to)
			}
		}
	}
}

func TestValidTransition(t *testing.T) {
	tests := []struct {
		n        int
		from, to State
		want     bool
	}{
		{0, Unknown, NotReadyToSwitchOn, true},
		{1, NotReadyToSwitchOn, SwitchOnDisabled, true},
		{12, QuickStopActive, SwitchOnDisabled, true},
		{13, OperationEnabled, FaultReactionActive, true},
		{13, NotReadyToSwitchOn, FaultReactionActive, true},
		{13, Fault, FaultReactionActive, false},
		{13, FaultReactionActive, FaultReactionActive, false},
		{14, FaultReactionActive, Fault, true},
		{14, OperationEnabled, Fault, false},
		{4, ReadyToSwitchOn, OperationEnabled, false},
		{17, Fault, SwitchOnDisabled, false},
	}
	for _, tt := range tests {
		if got := ValidTransition(tt.n, tt.from, tt.to); got != tt.want {
			t.Errorf("ValidTransition(%d, %v, %v) = %v", tt.n, tt.from, tt.to, got)
		}
	}
	tr := Transitions()
	tr[0].Number = 99
	if Transitions()[0].Number != 0 {
		t.Error("Transitions returned the internal table")
	}
	seen := map[int]bool{}
	for _, x := range Transitions() {
		seen[x.Number] = true
	}
	for n := 0; n <= 16; n++ {
		if !seen[n] {
			t.Errorf("transition %d missing", n)
		}
	}
}

func TestPath(t *testing.T) {
	for _, from := range append(allStates, Unknown) {
		for _, to := range append(allStates, Unknown) {
			p := Path(from, to)
			start := from
			switch from {
			case NotReadyToSwitchOn:
				start = SwitchOnDisabled
			case FaultReactionActive:
				start = Fault
			default: // no special case
			}
			reachable := to != NotReadyToSwitchOn && to != FaultReactionActive && to != Unknown &&
				(to != Fault || start == Fault) && start != Unknown
			if start == Unknown && to == Unknown {
				reachable = true
			}
			if !reachable {
				if p != nil {
					t.Errorf("Path(%v, %v) = %v, want nil", from, to, p)
				}
				continue
			}
			if p == nil {
				t.Errorf("Path(%v, %v) = nil", from, to)
				continue
			}
			s := start
			for _, c := range p {
				next, _, ok := Next(s, c)
				if !ok {
					t.Fatalf("Path(%v, %v) = %v: %v invalid in %v", from, to, p, c, s)
				}
				s = next
			}
			if s != to {
				t.Errorf("Path(%v, %v) = %v ends in %v", from, to, p, s)
			}
		}
	}
	tests := []struct {
		from, to State
		want     []Command
	}{
		{SwitchOnDisabled, OperationEnabled, []Command{Shutdown, SwitchOn, EnableOperation}},
		{NotReadyToSwitchOn, OperationEnabled, []Command{Shutdown, SwitchOn, EnableOperation}},
		{FaultReactionActive, OperationEnabled, []Command{FaultReset, Shutdown, SwitchOn, EnableOperation}},
		{OperationEnabled, SwitchedOn, []Command{DisableOperation}},
		{OperationEnabled, ReadyToSwitchOn, []Command{Shutdown}},
		{OperationEnabled, SwitchOnDisabled, []Command{DisableVoltage}},
		{OperationEnabled, QuickStopActive, []Command{QuickStop}},
		{QuickStopActive, OperationEnabled, []Command{EnableOperation}},
		{QuickStopActive, SwitchedOn, []Command{EnableOperation, DisableOperation}},
		{Fault, SwitchOnDisabled, []Command{FaultReset}},
		{SwitchedOn, SwitchedOn, []Command{}},
	}
	for _, tt := range tests {
		if got := Path(tt.from, tt.to); !slices.Equal(got, tt.want) {
			t.Errorf("Path(%v, %v) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestModes(t *testing.T) {
	tests := []struct {
		m          Mode
		str, name  string
		bit        uint32
		manufactur bool
	}{
		{ModeNone, "none", "No mode", 0, false},
		{ModePP, "PP", "Profile position", 1, false},
		{ModeVL, "VL", "Velocity", 2, false},
		{ModePV, "PV", "Profile velocity", 4, false},
		{ModePT, "PT", "Profile torque", 8, false},
		{ModeHM, "HM", "Homing", 0x20, false},
		{ModeIP, "IP", "Interpolated position", 0x40, false},
		{ModeCSP, "CSP", "Cyclic synchronous position", 0x80, false},
		{ModeCSV, "CSV", "Cyclic synchronous velocity", 0x100, false},
		{ModeCST, "CST", "Cyclic synchronous torque", 0x200, false},
		{Mode(5), "Mode(5)", "Mode(5)", 0, false},
		{Mode(-3), "manufacturer(-3)", "manufacturer(-3)", 0, true},
	}
	for _, tt := range tests {
		if tt.m.String() != tt.str || tt.m.Name() != tt.name || tt.m.SupportedBit() != tt.bit || tt.m.Manufacturer() != tt.manufactur {
			t.Errorf("mode %d: %q %q 0x%X %v", tt.m, tt.m.String(), tt.m.Name(), tt.m.SupportedBit(), tt.m.Manufacturer())
		}
	}
	x := SupportedModes(ModePP, ModeCSP, Mode(-1), ModeHM)
	if x != 0xA1 {
		t.Errorf("SupportedModes = 0x%X", x)
	}
	if got := DecodeSupportedModes(x | 1<<20); !slices.Equal(got, []Mode{ModePP, ModeHM, ModeCSP}) {
		t.Errorf("DecodeSupportedModes = %v", got)
	}
}

func TestObjects(t *testing.T) {
	d := od.New(1)
	custom := od.NewVar(IndexTargetVelocity, "Mine", canopen.Integer32, od.AccessRW, int32(7))
	if err := d.Add(custom); err != nil {
		t.Fatal(err)
	}
	if err := AddObjects(d, SupportedModes(ModePP)); err != nil {
		t.Fatal(err)
	}
	if d.Object(IndexTargetVelocity) != custom {
		t.Error("AddObjects replaced an existing object")
	}
	tests := []struct {
		index uint16
		sub   uint8
		dt    canopen.DataType
		acc   od.Access
		pdo   bool
	}{
		{IndexErrorCode, 0, canopen.Unsigned16, od.AccessRO, true},
		{IndexControlword, 0, canopen.Unsigned16, od.AccessRWW, true},
		{IndexStatusword, 0, canopen.Unsigned16, od.AccessRO, true},
		{IndexQuickStopOptionCode, 0, canopen.Integer16, od.AccessRW, false},
		{IndexFaultReactionOptionCode, 0, canopen.Integer16, od.AccessRW, false},
		{IndexModesOfOperation, 0, canopen.Integer8, od.AccessRWW, true},
		{IndexModesOfOperationDisplay, 0, canopen.Integer8, od.AccessRO, true},
		{IndexPositionActualValue, 0, canopen.Integer32, od.AccessRO, true},
		{IndexVelocityActualValue, 0, canopen.Integer32, od.AccessRO, true},
		{IndexTargetTorque, 0, canopen.Integer16, od.AccessRWW, true},
		{IndexTorqueActualValue, 0, canopen.Integer16, od.AccessRO, true},
		{IndexTargetPosition, 0, canopen.Integer32, od.AccessRWW, true},
		{IndexSoftwarePositionLimit, 0, canopen.Unsigned8, od.AccessConst, false},
		{IndexSoftwarePositionLimit, 2, canopen.Integer32, od.AccessRW, true},
		{IndexProfileVelocity, 0, canopen.Unsigned32, od.AccessRWW, true},
		{IndexHomingMethod, 0, canopen.Integer8, od.AccessRWW, true},
		{IndexHomingSpeeds, 1, canopen.Unsigned32, od.AccessRWW, true},
		{IndexSupportedDriveModes, 0, canopen.Unsigned32, od.AccessConst, false},
	}
	for _, tt := range tests {
		v, err := d.Variable(tt.index, tt.sub)
		if err != nil {
			t.Fatalf("0x%04X:%d: %v", tt.index, tt.sub, err)
		}
		if v.DataType != tt.dt || v.Access != tt.acc || v.PDOMappable != tt.pdo {
			t.Errorf("%s: %v %v pdo=%v", v.Key(), v.DataType, v.Access, v.PDOMappable)
		}
	}
	if x, _ := d.GetUint(IndexSupportedDriveModes, 0); x != 1 {
		t.Errorf("0x6502 = %d", x)
	}
	if x, _ := d.GetValue(IndexQuickStopOptionCode, 0); x != int64(DefaultQuickStopOptionCode) {
		t.Errorf("0x605A = %v", x)
	}
	if len(Objects(0)) < 28 {
		t.Errorf("Objects: %d", len(Objects(0)))
	}
}
