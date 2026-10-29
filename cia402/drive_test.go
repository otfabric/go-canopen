// SPDX-License-Identifier: MIT

package cia402

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

type transitionLog struct {
	mu  sync.Mutex
	log []Transition
}

func (l *transitionLog) add(from, to State, n int) {
	l.mu.Lock()
	l.log = append(l.log, Transition{Number: n, From: from, To: to})
	l.mu.Unlock()
}

func (l *transitionLog) reset() {
	l.mu.Lock()
	l.log = nil
	l.mu.Unlock()
}

func (l *transitionLog) numbers() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []int
	for _, t := range l.log {
		out = append(out, t.Number)
	}
	return out
}

type testDrive struct {
	t      *testing.T
	dict   *od.Dictionary
	drive  *Drive
	log    *transitionLog
	errors []uint16
	mu     sync.Mutex
}

func newTestDrive(t *testing.T, cfg DriveConfig) *testDrive {
	t.Helper()
	td := &testDrive{t: t, dict: od.New(1), log: &transitionLog{}}
	if cfg.OnTransition == nil {
		cfg.OnTransition = td.log.add
	}
	if cfg.OnError == nil {
		cfg.OnError = func(code uint16) {
			td.mu.Lock()
			td.errors = append(td.errors, code)
			td.mu.Unlock()
		}
	}
	d, err := NewDrive(td.dict, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	td.drive = d
	return td
}

func (td *testDrive) write(index uint16, sub uint8, data []byte) error {
	return td.dict.Write(index, sub, data)
}

func (td *testDrive) mustWrite(index uint16, sub uint8, data []byte) {
	td.t.Helper()
	if err := td.write(index, sub, data); err != nil {
		td.t.Fatalf("write 0x%04X:%d: %v", index, sub, err)
	}
}

func u16(x uint16) []byte { return binary.LittleEndian.AppendUint16(nil, x) }
func i16(x int16) []byte  { return u16(uint16(x)) }
func u32(x uint32) []byte { return binary.LittleEndian.AppendUint32(nil, x) }
func i32(x int32) []byte  { return u32(uint32(x)) }

func (td *testDrive) cw(x uint16) { td.t.Helper(); td.mustWrite(IndexControlword, 0, u16(x)) }

func (td *testDrive) mode(m Mode) {
	td.t.Helper()
	td.mustWrite(IndexModesOfOperation, 0, []byte{byte(m)})
}

// sw returns the statusword as published in the dictionary.
func (td *testDrive) sw() uint16 {
	b, err := td.dict.Read(IndexStatusword, 0)
	if err != nil {
		td.t.Fatal(err)
	}
	return binary.LittleEndian.Uint16(b)
}

func (td *testDrive) state() State {
	td.t.Helper()
	s := DecodeStatusword(td.sw())
	if s != td.drive.State() {
		td.t.Fatalf("published state %v, drive state %v", s, td.drive.State())
	}
	return s
}

func (td *testDrive) i32(index uint16) int32 {
	b, _ := td.dict.Read(index, 0)
	return int32(binary.LittleEndian.Uint32(b))
}

// steps advances the simulation n times by dt.
func (td *testDrive) steps(n int, dt time.Duration) {
	for range n {
		td.drive.Step(dt)
	}
}

// until steps by 1 ms until cond holds, failing after max steps.
func (td *testDrive) until(max int, cond func() bool) int {
	td.t.Helper()
	for i := range max {
		if cond() {
			return i
		}
		td.drive.Step(time.Millisecond)
	}
	td.t.Fatalf("condition not reached after %d steps (sw 0x%04X, pos %d)", max, td.sw(), td.drive.Position())
	return 0
}

// reach brings a fresh drive to a state.
func (td *testDrive) reach(s State) {
	td.t.Helper()
	if s == NotReadyToSwitchOn {
		return
	}
	td.drive.Step(time.Millisecond)
	switch s {
	case ReadyToSwitchOn:
		td.cw(0x06)
	case SwitchedOn:
		td.cw(0x06)
		td.cw(0x07)
	case OperationEnabled:
		td.cw(0x06)
		td.cw(0x07)
		td.cw(0x0F)
	case QuickStopActive:
		td.mustWrite(IndexQuickStopOptionCode, 0, i16(6))
		td.cw(0x06)
		td.cw(0x07)
		td.cw(0x0F)
		td.cw(0x02)
	case FaultReactionActive:
		td.drive.InjectFault(0x1234)
	case Fault:
		td.drive.InjectFault(0x1234)
		td.drive.Step(time.Millisecond)
	}
	if got := td.state(); got != s {
		td.t.Fatalf("reach %v: in %v", s, got)
	}
}

func TestDriveStartup(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	if td.state() != NotReadyToSwitchOn {
		t.Fatal(td.state())
	}
	td.drive.Step(time.Millisecond)
	if td.state() != SwitchOnDisabled {
		t.Fatal(td.state())
	}
	if td.sw()&StatusRemote == 0 {
		t.Error("remote bit not set")
	}
	if x, _ := td.dict.GetUint(IndexSupportedDriveModes, 0); uint32(x) != SupportedModes(SimulatedModes...) {
		t.Errorf("0x6502 = 0x%X", x)
	}
	if td.drive.Dictionary() != td.dict {
		t.Error("Dictionary")
	}
}

// TestDriveStateDiagram applies every command in every state and checks the
// state reached and the transitions taken against the CiA 402 diagram.
func TestDriveStateDiagram(t *testing.T) {
	for _, s := range allStates {
		for _, c := range allCommands {
			t.Run(s.String()+"/"+c.String(), func(t *testing.T) {
				td := newTestDrive(t, DriveConfig{})
				td.reach(s)
				td.log.reset()
				td.cw(c.Bits())
				want, wantT := s, []int(nil)
				if d, ok := diagram[s][c]; ok {
					want, wantT = State(d[0]), []int{d[1]}
				}
				if s == ReadyToSwitchOn && (c == SwitchOnAndEnable || c == EnableOperation) {
					want, wantT = OperationEnabled, []int{3, 4}
				}
				if got := td.state(); got != want {
					t.Errorf("state %v, want %v", got, want)
				}
				if got := td.log.numbers(); !equalInts(got, wantT) {
					t.Errorf("transitions %v, want %v", got, wantT)
				}
			})
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDriveLocalControlword(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(SwitchOnDisabled)
	for _, cw := range []uint16{0x06, 0x07, 0x0F} {
		if err := td.dict.Set(IndexControlword, 0, u16(cw)); err != nil {
			t.Fatal(err)
		}
	}
	if td.state() != OperationEnabled {
		t.Fatal(td.state())
	}
	if err := td.dict.SetValue(IndexModesOfOperation, 0, int8(ModePV)); err != nil {
		t.Fatal(err)
	}
	if td.drive.Mode() != ModePV {
		t.Error("local mode write ignored")
	}
	// An unsupported mode written locally is ignored.
	_ = td.dict.SetValue(IndexModesOfOperation, 0, int8(ModeIP))
	if td.drive.Mode() != ModePV {
		t.Error("unsupported local mode accepted")
	}
}

func TestDriveAborts(t *testing.T) {
	td := newTestDrive(t, DriveConfig{Modes: []Mode{ModePP, ModeHM}})
	td.reach(SwitchOnDisabled)
	td.mustWrite(IndexSoftwarePositionLimit, 1, i32(-1000))
	td.mustWrite(IndexSoftwarePositionLimit, 2, i32(1000))
	tests := []struct {
		name  string
		index uint16
		sub   uint8
		data  []byte
		want  error
	}{
		{"mode unsupported", IndexModesOfOperation, 0, []byte{byte(ModePV)}, canopen.AbortInvalidValue},
		{"mode manufacturer", IndexModesOfOperation, 0, []byte{0xFF}, canopen.AbortInvalidValue},
		{"mode none", IndexModesOfOperation, 0, []byte{0}, nil},
		{"mode ok", IndexModesOfOperation, 0, []byte{byte(ModeHM)}, nil},
		{"target low", IndexTargetPosition, 0, i32(-1001), canopen.AbortValueLow},
		{"target high", IndexTargetPosition, 0, i32(1001), canopen.AbortValueHigh},
		{"target ok", IndexTargetPosition, 0, i32(1000), nil},
		{"homing method 0", IndexHomingMethod, 0, []byte{0}, canopen.AbortInvalidValue},
		{"homing method 36", IndexHomingMethod, 0, []byte{36}, canopen.AbortInvalidValue},
		{"homing method 38", IndexHomingMethod, 0, []byte{38}, canopen.AbortInvalidValue},
		{"homing method 37", IndexHomingMethod, 0, []byte{37}, nil},
		{"quick stop 9", IndexQuickStopOptionCode, 0, i16(9), canopen.AbortInvalidValue},
		{"quick stop -1", IndexQuickStopOptionCode, 0, i16(-1), canopen.AbortInvalidValue},
		{"quick stop 8", IndexQuickStopOptionCode, 0, i16(8), nil},
		{"shutdown 2", IndexShutdownOptionCode, 0, i16(2), canopen.AbortInvalidValue},
		{"disable op 1", IndexDisableOperationOptionCode, 0, i16(1), nil},
		{"halt 0", IndexHaltOptionCode, 0, i16(0), canopen.AbortInvalidValue},
		{"halt 4", IndexHaltOptionCode, 0, i16(4), nil},
		{"fault reaction 5", IndexFaultReactionOptionCode, 0, i16(5), canopen.AbortInvalidValue},
		{"statusword", IndexStatusword, 0, u16(0), canopen.AbortReadOnly},
	}
	for _, tt := range tests {
		if err := td.write(tt.index, tt.sub, tt.data); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	// Local control refuses controlword writes and clears the remote bit.
	td.drive.SetRemote(false)
	if err := td.write(IndexControlword, 0, u16(0x06)); !errors.Is(err, canopen.AbortLocalControl) {
		t.Errorf("local control: %v", err)
	}
	if td.sw()&StatusRemote != 0 {
		t.Error("remote bit set")
	}
	_ = td.dict.Set(IndexControlword, 0, u16(0x06))
	if td.state() != SwitchOnDisabled {
		t.Error("controlword processed under local control")
	}
	td.drive.SetRemote(true)
	td.cw(0x06)
	if td.state() != ReadyToSwitchOn {
		t.Error("controlword not processed under remote control")
	}
	td.drive.SetWarning(true)
	if td.sw()&StatusWarning == 0 {
		t.Error("warning bit")
	}
}

func TestDriveStrictControlword(t *testing.T) {
	tests := []struct {
		state State
		cw    uint16
		want  error
	}{
		{SwitchOnDisabled, 0x07, canopen.AbortDeviceState},
		{SwitchOnDisabled, 0x0F, canopen.AbortDeviceState},
		{SwitchOnDisabled, 0x00, nil},
		{SwitchOnDisabled, 0x02, nil},
		{SwitchOnDisabled, 0x06, nil},
		{SwitchOnDisabled, 0x80, nil},
		{ReadyToSwitchOn, 0x06, nil},
		{SwitchedOn, 0x07, nil},
		{OperationEnabled, 0x1F, nil},
		{QuickStopActive, 0x02, nil},
		{QuickStopActive, 0x06, canopen.AbortDeviceState},
		{QuickStopActive, 0x07, canopen.AbortDeviceState},
		{Fault, 0x0F, nil},
		{FaultReactionActive, 0x0F, nil},
		{NotReadyToSwitchOn, 0x0F, nil},
	}
	for _, tt := range tests {
		td := newTestDrive(t, DriveConfig{StrictControlword: true})
		td.reach(tt.state)
		if err := td.write(IndexControlword, 0, u16(tt.cw)); !errors.Is(err, tt.want) {
			t.Errorf("%v cw 0x%02X: %v, want %v", tt.state, tt.cw, err, tt.want)
		}
	}
}

func TestDriveFault(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mode(ModePV)
	td.mustWrite(IndexTargetVelocity, 0, i32(5000))
	td.until(1000, func() bool { return td.drive.Velocity() == 5000 })
	td.log.reset()
	td.drive.InjectFault(0x2310)
	if td.state() != FaultReactionActive {
		t.Fatal(td.state())
	}
	if code, _ := td.dict.GetUint(IndexErrorCode, 0); code != 0x2310 {
		t.Errorf("0x603F = 0x%X", code)
	}
	// Fault reaction 2: quick stop ramp of 1e6 inc/s² stops 5000 inc/s in 5 ms.
	n := td.until(100, func() bool { return td.state() == Fault })
	if n < 5 || n > 7 {
		t.Errorf("fault reaction took %d steps", n)
	}
	// A second fault in Fault keeps the state but updates the code.
	td.drive.InjectFault(0x5441)
	if td.state() != Fault || td.drive.ErrorCode() != 0x5441 {
		t.Error("second fault")
	}
	if got := td.log.numbers(); !equalInts(got, []int{13, 14}) {
		t.Errorf("transitions %v", got)
	}
	// Fault reset needs a rising edge of bit 7.
	td.cw(0x80)
	if td.state() != SwitchOnDisabled {
		t.Fatal("fault reset with rising edge failed")
	}
	if td.drive.ErrorCode() != 0 {
		t.Error("error code not cleared")
	}
	td.drive.InjectFault(0x1000)
	td.drive.Step(time.Millisecond)
	td.cw(0x80) // bit 7 still set: no edge
	if td.state() != Fault {
		t.Fatal("fault reset without an edge")
	}
	td.cw(0x00)
	td.cw(0x80)
	if td.state() != SwitchOnDisabled {
		t.Fatal("fault reset failed")
	}
	td.mu.Lock()
	defer td.mu.Unlock()
	if !equalInts(intsOf(td.errors), []int{0x2310, 0x5441, 0, 0x1000, 0}) {
		t.Errorf("OnError calls %X", td.errors)
	}
}

func intsOf(x []uint16) []int {
	out := make([]int, len(x))
	for i, v := range x {
		out[i] = int(v)
	}
	return out
}

func TestDriveFaultReactionImmediate(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.mustWrite(IndexFaultReactionOptionCode, 0, i16(0))
	td.reach(OperationEnabled)
	td.mode(ModeCSV)
	td.mustWrite(IndexTargetVelocity, 0, i32(1000))
	td.drive.Step(time.Millisecond)
	td.drive.InjectFault(1)
	td.drive.Step(time.Millisecond)
	if td.state() != Fault {
		t.Fatal(td.state())
	}
}

func TestDriveQuickStop(t *testing.T) {
	tests := []struct {
		code      int16
		final     State
		minSteps  int
		maxSteps  int
		canResume bool
	}{
		{0, SwitchOnDisabled, 0, 0, false},
		{1, SwitchOnDisabled, 99, 102, false}, // 10000 inc/s at 1e5 inc/s²: 100 ms
		{2, SwitchOnDisabled, 9, 12, false},   // at 1e6 inc/s²: 10 ms
		{3, SwitchOnDisabled, 9, 12, false},
		{5, QuickStopActive, 0, 0, true},
		{6, QuickStopActive, 0, 0, true},
	}
	for _, tt := range tests {
		td := newTestDrive(t, DriveConfig{})
		td.mustWrite(IndexQuickStopOptionCode, 0, i16(tt.code))
		td.reach(OperationEnabled)
		td.mode(ModePV)
		td.mustWrite(IndexTargetVelocity, 0, i32(10000))
		td.until(1000, func() bool { return td.drive.Velocity() == 10000 })
		td.cw(0x02)
		if tt.code == 0 {
			if td.state() != SwitchOnDisabled || td.drive.Velocity() != 0 {
				t.Errorf("code 0: %v", td.state())
			}
			continue
		}
		if td.state() != QuickStopActive || td.sw()&StatusQuickStop != 0 {
			t.Fatalf("code %d: %v", tt.code, td.state())
		}
		if tt.final == SwitchOnDisabled {
			n := td.until(1000, func() bool { return td.state() == SwitchOnDisabled })
			if n < tt.minSteps || n > tt.maxSteps {
				t.Errorf("code %d: quick stop took %d steps", tt.code, n)
			}
			continue
		}
		td.until(1000, func() bool { return td.drive.Velocity() == 0 })
		td.steps(5, time.Millisecond)
		if td.state() != QuickStopActive {
			t.Errorf("code %d: left quick stop active", tt.code)
		}
		td.cw(0x0F)
		if td.state() != OperationEnabled {
			t.Errorf("code %d: transition 16 not taken", tt.code)
		}
	}
	// Option 2: enable operation during the ramp is ignored.
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.cw(0x02)
	td.cw(0x0F)
	if td.state() != QuickStopActive {
		t.Error("transition 16 taken with option code 2")
	}
}

func TestDrivePendingStop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option uint16
		cw     uint16
		to     State
		n      int
	}{
		{"disable operation", IndexDisableOperationOptionCode, 0x07, SwitchedOn, 5},
		{"shutdown", IndexShutdownOptionCode, 0x06, ReadyToSwitchOn, 8},
	} {
		td := newTestDrive(t, DriveConfig{})
		td.mustWrite(tc.option, 0, i16(1))
		td.reach(OperationEnabled)
		td.mode(ModePV)
		td.mustWrite(IndexTargetVelocity, 0, i32(10000))
		td.until(1000, func() bool { return td.drive.Velocity() == 10000 })
		td.log.reset()
		td.cw(tc.cw)
		if td.state() != OperationEnabled {
			t.Fatalf("%s: left at once", tc.name)
		}
		td.cw(tc.cw) // repeating the command keeps the pending transition
		n := td.until(1000, func() bool { return td.state() == tc.to })
		if n < 99 || n > 102 {
			t.Errorf("%s: took %d steps", tc.name, n)
		}
		if got := td.log.numbers(); !equalInts(got, []int{tc.n}) {
			t.Errorf("%s: transitions %v", tc.name, got)
		}
	}
	// Re-enabling during the ramp cancels the pending transition.
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mode(ModePV)
	td.mustWrite(IndexTargetVelocity, 0, i32(10000))
	td.until(1000, func() bool { return td.drive.Velocity() == 10000 })
	td.cw(0x07)
	td.steps(10, time.Millisecond)
	td.cw(0x0F)
	td.steps(200, time.Millisecond)
	if td.state() != OperationEnabled || td.drive.Velocity() != 10000 {
		t.Errorf("pending stop not cancelled: %v %d", td.state(), td.drive.Velocity())
	}
	// Disable voltage during the ramp acts at once.
	td.cw(0x07)
	td.cw(0x00)
	if td.state() != SwitchOnDisabled || td.drive.Velocity() != 0 {
		t.Errorf("disable voltage: %v", td.state())
	}
}

func TestDrivePP(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mode(ModePP)
	if td.sw()&StatusTargetReached == 0 {
		t.Error("target reached not set at rest")
	}
	// Absolute move of 10000 inc: 0.1 s accel, 0.9 s cruise, 0.1 s decel.
	td.mustWrite(IndexTargetPosition, 0, i32(10000))
	td.cw(0x1F)
	sw := td.sw()
	if sw&StatusSetPointAcknowledge == 0 || sw&StatusTargetReached != 0 {
		t.Fatalf("after new set-point: sw 0x%04X", sw)
	}
	td.cw(0x0F)
	if td.sw()&StatusSetPointAcknowledge != 0 {
		t.Error("ack not cleared")
	}
	n := td.until(5000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if n < 1085 || n > 1110 {
		t.Errorf("move took %d ms", n)
	}
	if td.drive.Position() != 10000 || td.i32(IndexPositionActualValue) != 10000 || td.i32(IndexPositionDemandValue) != 10000 {
		t.Errorf("position %d", td.drive.Position())
	}
	// Relative move: relative to the preceding target.
	td.mustWrite(IndexTargetPosition, 0, i32(-2500))
	td.cw(0x5F)
	td.cw(0x4F)
	td.until(5000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if td.drive.Position() != 7500 {
		t.Errorf("relative: %d", td.drive.Position())
	}
	// Buffered set-point: second target queued behind the first.
	td.mustWrite(IndexTargetPosition, 0, i32(8000))
	td.cw(0x1F)
	td.cw(0x0F)
	td.mustWrite(IndexTargetPosition, 0, i32(9000))
	td.cw(0x1F)
	if td.sw()&StatusSetPointAcknowledge == 0 {
		t.Fatal("buffered set-point not acknowledged")
	}
	// A third one is not accepted while the buffer is full.
	td.cw(0x0F)
	if td.sw()&StatusSetPointAcknowledge == 0 {
		t.Error("ack cleared while the buffer is full")
	}
	td.mustWrite(IndexTargetPosition, 0, i32(-5))
	td.cw(0x1F)
	td.cw(0x0F)
	td.until(5000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if td.drive.Position() != 9000 || td.sw()&StatusSetPointAcknowledge != 0 {
		t.Errorf("buffered: %d sw 0x%04X", td.drive.Position(), td.sw())
	}
	// Change set immediately: reverse during a move.
	td.mustWrite(IndexTargetPosition, 0, i32(20000))
	td.cw(0x1F)
	td.cw(0x0F)
	td.steps(300, time.Millisecond)
	td.mustWrite(IndexTargetPosition, 0, i32(0))
	td.cw(0x3F)
	td.cw(0x2F)
	td.until(5000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if td.drive.Position() != 0 {
		t.Errorf("immediate: %d", td.drive.Position())
	}
	// Halt stops the move; releasing it resumes.
	td.mustWrite(IndexTargetPosition, 0, i32(5000))
	td.cw(0x1F)
	td.cw(0x0F)
	td.steps(200, time.Millisecond)
	td.cw(0x10F)
	td.until(1000, func() bool { return td.drive.Velocity() == 0 })
	if td.sw()&StatusTargetReached == 0 {
		t.Error("halted: target reached not set")
	}
	td.cw(0x0F)
	td.until(5000, func() bool { return td.sw()&StatusTargetReached != 0 && td.drive.Position() == 5000 })
	// Zero acceleration/deceleration: instantaneous profile.
	td.mustWrite(IndexProfileAcceleration, 0, u32(0))
	td.mustWrite(IndexProfileDeceleration, 0, u32(0))
	td.mustWrite(IndexTargetPosition, 0, i32(5100))
	td.cw(0x1F)
	td.cw(0x0F)
	if n := td.until(100, func() bool { return td.sw()&StatusTargetReached != 0 }); n > 11 {
		t.Errorf("instant profile took %d", n)
	}
	// Mode change drops the set-point.
	td.mustWrite(IndexTargetPosition, 0, i32(0))
	td.cw(0x1F)
	td.mode(ModePV)
	td.mode(ModePP)
	if td.sw()&StatusSetPointAcknowledge != 0 {
		t.Error("set-point survived a mode change")
	}
}

func TestDriveSoftwareLimits(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mustWrite(IndexSoftwarePositionLimit, 1, i32(-100))
	td.mustWrite(IndexSoftwarePositionLimit, 2, i32(100))
	td.mode(ModePV)
	td.mustWrite(IndexTargetVelocity, 0, i32(10000))
	td.until(1000, func() bool { return td.drive.Position() == 100 })
	if td.sw()&StatusInternalLimitActive == 0 {
		t.Error("internal limit active not set")
	}
	// Relative PP target beyond the limit is clamped.
	td.mode(ModePP)
	td.mustWrite(IndexTargetPosition, 0, i32(90))
	td.cw(0x5F)
	td.cw(0x0F)
	td.until(1000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if td.drive.Position() != 100 || td.sw()&StatusInternalLimitActive == 0 {
		t.Errorf("PP clamp: %d", td.drive.Position())
	}
	td.mode(ModeCSP)
	td.mustWrite(IndexSoftwarePositionLimit, 1, i32(-10))
	td.mustWrite(IndexSoftwarePositionLimit, 2, i32(10))
	td.drive.Step(time.Millisecond)
	if td.drive.Position() != 10 || td.sw()&StatusInternalLimitActive == 0 {
		t.Errorf("CSP clamp: %d", td.drive.Position())
	}
}

func TestDrivePV(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mode(ModePV)
	if td.sw()&StatusSpeedZero == 0 || td.sw()&StatusTargetReached == 0 {
		t.Errorf("at rest: 0x%04X", td.sw())
	}
	td.mustWrite(IndexTargetVelocity, 0, i32(-3000))
	td.drive.Step(time.Millisecond)
	if td.sw()&(StatusSpeedZero|StatusTargetReached) != 0 {
		t.Errorf("ramping: 0x%04X", td.sw())
	}
	n := td.until(1000, func() bool { return td.sw()&StatusTargetReached != 0 })
	if n < 29 || n > 31 {
		t.Errorf("ramp took %d", n)
	}
	if td.i32(IndexVelocityActualValue) != -3000 || td.drive.Position() >= 0 {
		t.Errorf("velocity %d position %d", td.i32(IndexVelocityActualValue), td.drive.Position())
	}
	// Reversal uses the deceleration.
	td.mustWrite(IndexTargetVelocity, 0, i32(3000))
	td.until(1000, func() bool { return td.drive.Velocity() == 3000 })
	// Halt with option 2 stops on the quick stop ramp.
	td.mustWrite(IndexHaltOptionCode, 0, i16(2))
	td.cw(0x10F)
	if n := td.until(100, func() bool { return td.drive.Velocity() == 0 }); n > 4 {
		t.Errorf("halt took %d", n)
	}
	if td.sw()&StatusTargetReached == 0 {
		t.Error("halted: target reached")
	}
}

func TestDriveTorqueModes(t *testing.T) {
	for _, m := range []Mode{ModePT, ModeCST} {
		td := newTestDrive(t, DriveConfig{})
		td.reach(OperationEnabled)
		td.mode(m)
		td.mustWrite(IndexTargetTorque, 0, i16(-50))
		td.steps(2000, time.Millisecond)
		if td.drive.Torque() != -50 {
			t.Errorf("%v: torque %d", m, td.drive.Torque())
		}
		b, _ := td.dict.Read(IndexTorqueActualValue, 0)
		if int16(binary.LittleEndian.Uint16(b)) != -50 {
			t.Errorf("%v: 0x6077", m)
		}
		// Steady state: TorqueGain·τ·t = 10·t.
		if v := td.drive.Velocity(); v != -500 {
			t.Errorf("%v: velocity %d", m, v)
		}
		sw := td.sw()
		if m == ModePT && sw&StatusTargetReached == 0 {
			t.Errorf("PT: target reached 0x%04X", sw)
		}
		if m == ModeCST && sw&StatusFollowsCommand == 0 {
			t.Errorf("CST: follows command 0x%04X", sw)
		}
		td.cw(0x10F)
		td.until(1000, func() bool { return td.drive.Velocity() == 0 })
		if m == ModePT && td.sw()&StatusTargetReached == 0 {
			t.Error("PT halted: target reached")
		}
	}
}

func TestDriveCyclicModes(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	td.reach(OperationEnabled)
	td.mode(ModeCSP)
	for i := int32(1); i <= 10; i++ {
		td.mustWrite(IndexTargetPosition, 0, i32(i*10))
		td.drive.Step(time.Millisecond)
		if td.drive.Position() != i*10 || td.drive.Velocity() != 10000 {
			t.Fatalf("CSP: %d %d", td.drive.Position(), td.drive.Velocity())
		}
	}
	if td.sw()&StatusFollowsCommand == 0 {
		t.Error("CSP follows command")
	}
	td.mode(ModeCSV)
	td.mustWrite(IndexTargetVelocity, 0, i32(-2000))
	td.steps(10, time.Millisecond)
	if td.drive.Position() != 80 || td.drive.Velocity() != -2000 {
		t.Errorf("CSV: %d %d", td.drive.Position(), td.drive.Velocity())
	}
	// No mode: the drive ramps down and holds.
	td.mode(ModeNone)
	td.steps(100, time.Millisecond)
	if td.drive.Velocity() != 0 {
		t.Error("no mode: still moving")
	}
}

func TestDriveHoming(t *testing.T) {
	td := newTestDrive(t, DriveConfig{HomeSwitch: -2000})
	td.reach(OperationEnabled)
	td.mode(ModeHM)
	if td.sw()&(StatusHomingAttained|StatusHomingError) != 0 || td.sw()&StatusTargetReached == 0 {
		t.Errorf("before homing: 0x%04X", td.sw())
	}
	td.mustWrite(IndexHomeOffset, 0, i32(123))
	td.mustWrite(IndexHomingMethod, 0, []byte{17})
	td.cw(0x1F)
	if td.sw()&(StatusTargetReached|StatusHomingAttained) != 0 {
		t.Errorf("homing: 0x%04X", td.sw())
	}
	n := td.until(5000, func() bool { return td.sw()&StatusHomingAttained != 0 })
	if n < 400 || n > 500 { // 2000 inc at 5000 inc/s
		t.Errorf("homing took %d", n)
	}
	if td.drive.Position() != 123 || td.sw()&StatusTargetReached == 0 {
		t.Errorf("homed at %d", td.drive.Position())
	}
	// Interrupt: falling edge of bit 4.
	td.cw(0x0F)
	td.cw(0x1F)
	td.steps(50, time.Millisecond)
	td.cw(0x0F)
	td.until(1000, func() bool { return td.drive.Velocity() == 0 })
	if sw := td.sw(); sw&StatusHomingAttained != 0 || sw&StatusTargetReached == 0 {
		t.Errorf("interrupted: 0x%04X", sw)
	}
	// Current position method completes at once.
	td.mustWrite(IndexHomingMethod, 0, []byte{37})
	td.cw(0x1F)
	if td.drive.Position() != 123 || td.sw()&StatusHomingAttained == 0 {
		t.Error("method 37")
	}
	// Homing speed 0: homing error.
	td.cw(0x0F)
	td.mustWrite(IndexHomingMethod, 0, []byte{1})
	td.mustWrite(IndexHomingSpeeds, 1, u32(0))
	td.cw(0x1F)
	td.drive.Step(time.Millisecond)
	if td.sw()&StatusHomingError == 0 {
		t.Errorf("homing error: 0x%04X", td.sw())
	}
}

func TestDriveRunAndClose(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- td.drive.Run(ctx, time.Millisecond) }()
	deadline := time.Now().Add(5 * time.Second)
	for td.drive.State() != SwitchOnDisabled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run: %v", err)
	}
	if td.drive.State() != SwitchOnDisabled {
		t.Fatal("Run did not step")
	}
	td.drive.Close()
	td.drive.Close()
	td.cw(0x06)
	td.drive.Step(time.Millisecond)
	td.drive.InjectFault(1)
	if td.drive.State() != SwitchOnDisabled {
		t.Error("closed drive processed input")
	}
	if err := td.write(IndexModesOfOperation, 0, []byte{0x7F}); err != nil {
		t.Errorf("closed drive refused a write: %v", err)
	}
}

func TestNewDriveErrors(t *testing.T) {
	if _, err := NewDrive(od.New(1), DriveConfig{Modes: []Mode{ModeIP}}); err == nil {
		t.Error("IP accepted")
	}
	d := od.New(1)
	_ = d.Add(od.NewVar(IndexStatusword, "bad", canopen.Unsigned8, od.AccessRO, nil))
	if _, err := NewDrive(d, DriveConfig{}); err == nil {
		t.Error("statusword of the wrong type accepted")
	}
	d2 := od.New(1)
	if err := AddObjects(d2, 0); err != nil {
		t.Fatal(err)
	}
	d2.Remove(IndexControlword)
	d2.Remove(IndexModesOfOperation)
	_ = d2.Add(od.NewVar(IndexModesOfOperation, "m", canopen.Integer8, od.AccessRW, int8(ModePV)))
	drv, err := NewDrive(d2, DriveConfig{})
	if err != nil {
		// 0x6040 is re-added by AddObjects.
		t.Fatal(err)
	}
	if drv.Mode() != ModePV {
		t.Errorf("initial mode %v", drv.Mode())
	}
	drv.Close()
}

// TestDriveConcurrent exercises the drive from several goroutines; run
// with -race.
func TestDriveConcurrent(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	// A watcher reacting to statusword changes by writing the controlword
	// must not deadlock.
	cancel := td.dict.Watch(func(v *od.Variable, data []byte) {
		if v.Index == IndexStatusword && DecodeStatusword(binary.LittleEndian.Uint16(data)) == Fault {
			_ = td.dict.Write(IndexControlword, 0, u16(0x00))
		}
	})
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = td.drive.Run(ctx, 100*time.Microsecond) }()
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				switch (i + g) % 5 {
				case 0:
					_ = td.dict.Write(IndexControlword, 0, u16([]uint16{0x06, 0x07, 0x0F, 0x1F, 0x80, 0x02}[i%6]))
				case 1:
					_ = td.dict.Write(IndexModesOfOperation, 0, []byte{byte(SimulatedModes[i%len(SimulatedModes)])})
				case 2:
					td.drive.Step(time.Millisecond)
				case 3:
					if i%50 == 0 {
						td.drive.InjectFault(0xFF00)
					}
				case 4:
					_ = td.drive.Statusword()
					_, _ = td.dict.Read(IndexStatusword, 0)
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	stop()
	wg.Wait()
	td.drive.Step(time.Millisecond)
	if DecodeStatusword(td.sw()) == Unknown {
		t.Errorf("statusword 0x%04X", td.sw())
	}
}
