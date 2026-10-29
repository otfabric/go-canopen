// SPDX-License-Identifier: MIT

package cia402

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Torque model of the simulated load (PT, CST): an inertia with viscous
// friction, dv/dt = TorqueGain·torque − v/TorqueTimeConstant, so a constant
// torque t settles at the velocity TorqueGain·TorqueTimeConstant·t.
const (
	TorqueGain         = 100.0 // increments/s² per thousandth of rated torque
	TorqueTimeConstant = 0.1   // seconds
)

// SimulatedModes are the modes of operation a [Drive] simulates.
var SimulatedModes = []Mode{ModePP, ModePV, ModePT, ModeHM, ModeCSP, ModeCSV, ModeCST}

// DriveConfig configures a [Drive].
type DriveConfig struct {
	// Modes are the supported modes of operation, a subset of
	// [SimulatedModes]; nil means all of them. They are announced in 0x6502
	// when NewDrive adds that object.
	Modes []Mode
	// StrictControlword refuses (with [canopen.AbortDeviceState]) a remote
	// controlword write whose command neither keeps the present state nor
	// triggers a transition of it. By default such commands are accepted and
	// ignored, as most drives do.
	StrictControlword bool
	// HomeSwitch is the position of the simulated home switch the homing
	// methods search for.
	HomeSwitch int32
	// OnError is called with the error code when a fault occurs (see
	// [Drive.InjectFault]) and with 0 when the fault is reset, e.g. to emit
	// an EMCY message (0x5xxx, 0xFF00...) and an error reset. It runs without
	// locks held.
	OnError func(code uint16)
	// OnTransition is called after every state transition with its number;
	// it runs without locks held.
	OnTransition func(from, to State, transition int)
}

// Drive is a CiA 402 device model on an object dictionary with a simple,
// deterministic motion simulation. Writes of the controlword (0x6040) and of
// the modes of operation (0x6060), remote (SDO, RPDO) or local
// ([od.Dictionary.Set]), drive the state machine; the statusword (0x6041),
// the modes of operation display (0x6061), the actual values (0x6062,
// 0x6064, 0x606C, 0x6077) and the error code (0x603F) are kept current.
// The other objects (targets, profile parameters, option codes) are read
// from the dictionary when they are used. The simulation advances with
// [Drive.Step] (or [Drive.Run]).
//
// Remote writes are refused with abort codes: the controlword with
// [canopen.AbortLocalControl] while the drive is not under remote control
// and, with StrictControlword, with [canopen.AbortDeviceState]; an
// unsupported mode, homing method or option code with
// [canopen.AbortInvalidValue]; a target position outside the software
// position limits with [canopen.AbortValueLow] / [canopen.AbortValueHigh].
//
// Behaviour of the simulation:
//   - The drive starts in Not ready to switch on and takes transition 1 at
//     the first Step.
//   - Leaving Operation enabled other than by a quick stop disables the
//     drive function: the motor stops at once. With shutdown option code 1
//     (0x605B) or disable operation option code 1 (0x605C) the drive first
//     ramps down with the profile deceleration and changes state when
//     stopped.
//   - Quick stop (0x605A) and fault reaction (0x605E) ramp down with the
//     profile deceleration (codes 1, 5) or the quick stop deceleration (2,
//     3, 4, 6, 7, 8: current and voltage limit are modelled as the quick
//     stop ramp); code 0 stops at once. Quick stop codes 0..4 then go to
//     Switch on disabled, 5..8 stay in Quick stop active (transition 16
//     possible). The fault reaction ends in Fault once stopped, at the
//     earliest at the next Step.
//   - Halt (bit 8) ramps down per the halt option code (0x605D: 1 profile
//     deceleration, 2..4 quick stop deceleration); releasing it resumes.
//   - PP: trapezoidal profile with 0x6081/0x6083/0x6084; set-point handshake
//     (bit 4 / bit 12) with one buffered set-point, change set immediately
//     (bit 5), relative (bit 6, relative to the preceding target); target
//     reached (bit 10) when stopped at the target.
//   - PV: ramps to 0x60FF with 0x6083/0x6084; bit 10 velocity reached, bit
//     12 speed zero. PT and CST: torque actual follows 0x6071 at once, the
//     load moves by the torque model ([TorqueGain]). CSP and CSV follow
//     0x607A / 0x60FF every Step.
//   - HM: a rising edge of bit 4 starts homing, a falling edge interrupts
//     it. Methods 35 and 37 (current position) complete at once; the others
//     move to [DriveConfig.HomeSwitch] at the homing speed 0x6099:1 with the
//     homing acceleration 0x609A. On arrival the position becomes the home
//     offset 0x607C. A homing speed of 0 is a homing error (bit 13).
//   - Positions are clamped to the software position limits 0x607D (active
//     when min < max), setting internal limit active (bit 11).
//
// A Drive is safe for concurrent use.
type Drive struct {
	dict *od.Dictionary
	cfg  DriveConfig

	mu      sync.Mutex
	state   State
	cw      uint16
	mode    Mode
	modes   map[Mode]bool
	remote  bool
	warning bool
	errCode uint16
	closed  bool

	pos, vel, torque float64
	limit            bool

	// pending is a transition (5 or 8) delayed until the motor stopped.
	pending     *Transition
	ppActive    bool
	ppTarget    float64
	ppBuf       bool
	ppBufTarget float64
	ppAck       bool
	ppClamped   bool
	lastTarget  float64

	homing, homeAttained, homeError bool

	// publication of the outputs
	ver, pubVer uint64
	publishing  bool
	published   outputs
	events      []func()
	unhook      []func() // removes the write hooks and the watcher
}

type outputs struct {
	valid            bool
	sw               uint16
	mode             Mode
	demand, pos, vel int32
	torque           int16
	errCode          uint16
}

// NewDrive adds the CiA 402 objects ([AddObjects]) to the dictionary where
// missing and attaches a drive model to it.
func NewDrive(dict *od.Dictionary, cfg DriveConfig) (*Drive, error) {
	modes := cfg.Modes
	if modes == nil {
		modes = SimulatedModes
	}
	d := &Drive{dict: dict, cfg: cfg, state: NotReadyToSwitchOn, remote: true, modes: map[Mode]bool{}}
	for _, m := range modes {
		ok := false
		for _, s := range SimulatedModes {
			ok = ok || s == m
		}
		if !ok {
			return nil, fmt.Errorf("cia402: mode %v is not simulated", m)
		}
		d.modes[m] = true
	}
	if err := AddObjects(dict, SupportedModes(modes...)); err != nil {
		return nil, err
	}
	for _, o := range Objects(0) {
		for _, want := range o.Subs {
			if v, err := dict.Variable(want.Index, want.SubIndex); err == nil && v.DataType != want.DataType {
				return nil, fmt.Errorf("cia402: object %s is %s, want %s", v.Key(), v.DataType, want.DataType)
			}
		}
	}
	d.cw = uint16(d.getUint(IndexControlword, 0))
	if m := Mode(d.getInt(IndexModesOfOperation, 0)); d.modes[m] {
		d.mode = m
	}
	d.unhook = append(d.unhook,
		dict.OnWrite(IndexControlword, 0, d.checkControlword),
		dict.OnWrite(IndexModesOfOperation, 0, d.checkMode),
		dict.OnWrite(IndexTargetPosition, 0, d.checkTarget),
		dict.OnWrite(IndexHomingMethod, 0, d.checkHomingMethod))
	for _, idx := range []uint16{IndexQuickStopOptionCode, IndexShutdownOptionCode, IndexDisableOperationOptionCode,
		IndexHaltOptionCode, IndexFaultReactionOptionCode} {
		d.unhook = append(d.unhook, dict.OnWrite(idx, 0, d.checkOptionCode))
	}
	d.unhook = append(d.unhook, dict.Watch(d.watch))
	d.mu.Lock()
	d.ver++
	d.mu.Unlock()
	d.flush()
	return d, nil
}

// Dictionary returns the drive's object dictionary.
func (d *Drive) Dictionary() *od.Dictionary { return d.dict }

// Close detaches the drive from the dictionary: its write hooks and its
// watcher are removed, so writes are no longer checked or processed, and
// Step does nothing.
func (d *Drive) Close() {
	d.mu.Lock()
	hs := d.unhook
	d.unhook = nil
	d.closed = true
	d.mu.Unlock()
	for _, rm := range hs {
		rm()
	}
}

// --- dictionary access (no drive lock needed: the dictionary has its own)

func (d *Drive) getInt(index uint16, sub uint8) int64 {
	b, err := d.dict.Get(index, sub)
	if err != nil {
		return 0
	}
	return canopen.DecodeInt(b)
}

func (d *Drive) getUint(index uint16, sub uint8) uint64 {
	b, err := d.dict.Get(index, sub)
	if err != nil {
		return 0
	}
	return canopen.DecodeUint(b)
}

// limits returns the software position limits and whether they are active.
func (d *Drive) limits() (lo, hi float64, ok bool) {
	l, h := d.getInt(IndexSoftwarePositionLimit, 1), d.getInt(IndexSoftwarePositionLimit, 2)
	return float64(l), float64(h), l < h
}

// --- write hooks (remote writes only)

func (d *Drive) checkControlword(_ *od.Variable, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	if !d.remote {
		return canopen.AbortLocalControl
	}
	if !d.cfg.StrictControlword {
		return nil
	}
	cw := binary.LittleEndian.Uint16(data)
	cmd := DecodeControlword(cw)
	if cmd == FaultReset {
		return nil
	}
	if _, _, ok := Next(d.state, cmd); ok {
		return nil
	}
	var hold []Command
	switch d.state {
	case SwitchOnDisabled:
		hold = []Command{DisableVoltage, QuickStop}
	case ReadyToSwitchOn:
		hold = []Command{Shutdown}
	case SwitchedOn:
		hold = []Command{SwitchOn}
	case OperationEnabled:
		hold = []Command{EnableOperation}
	case QuickStopActive:
		hold = []Command{QuickStop}
	default: // automatic states and Fault accept every controlword
		return nil
	}
	for _, h := range hold {
		if h == cmd {
			return nil
		}
	}
	return canopen.AbortDeviceState
}

func (d *Drive) checkMode(_ *od.Variable, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m := Mode(int8(data[0])); !d.closed && m != ModeNone && !d.modes[m] {
		return canopen.AbortInvalidValue
	}
	return nil
}

func (d *Drive) checkTarget(_ *od.Variable, data []byte) error {
	lo, hi, ok := d.limits()
	x := float64(canopen.DecodeInt(data))
	switch {
	case !ok:
		return nil
	case x < lo:
		return canopen.AbortValueLow
	case x > hi:
		return canopen.AbortValueHigh
	}
	return nil
}

func (d *Drive) checkHomingMethod(_ *od.Variable, data []byte) error {
	if m := int8(data[0]); m < 1 || m > 37 || m == 36 {
		return canopen.AbortInvalidValue
	}
	return nil
}

func (d *Drive) checkOptionCode(v *od.Variable, data []byte) error {
	x := int16(binary.LittleEndian.Uint16(data))
	lo, hi := int16(0), int16(4)
	switch v.Index {
	case IndexQuickStopOptionCode:
		hi = 8
	case IndexShutdownOptionCode, IndexDisableOperationOptionCode:
		hi = 1
	case IndexHaltOptionCode:
		lo = 1
	}
	if x < lo || x > hi {
		return canopen.AbortInvalidValue
	}
	return nil
}

// --- watcher (remote and local writes)

func (d *Drive) watch(v *od.Variable, data []byte) {
	if v.SubIndex != 0 || (v.Index != IndexControlword && v.Index != IndexModesOfOperation) {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	if v.Index == IndexControlword {
		d.controlwordLocked(binary.LittleEndian.Uint16(data))
	} else {
		d.modeLocked(Mode(int8(data[0])))
	}
	d.ver++
	d.mu.Unlock()
	d.flush()
}

func (d *Drive) modeLocked(m Mode) {
	if m != ModeNone && !d.modes[m] {
		return
	}
	if m != d.mode {
		d.mode = m
		d.resetMotionLocked()
	}
}

// resetMotionLocked forgets the set-points and stops homing.
func (d *Drive) resetMotionLocked() {
	d.ppActive, d.ppBuf, d.ppAck, d.ppClamped = false, false, false, false
	d.homing = false
	d.lastTarget = d.pos
}

func (d *Drive) controlwordLocked(cw uint16) {
	prev := d.cw
	d.cw = cw
	if !d.remote {
		return
	}
	cmd := DecodeControlword(cw)
	if cmd == FaultReset {
		if d.state == Fault && prev&CtrlFaultReset == 0 {
			d.transitionLocked(SwitchOnDisabled, 15)
			d.errCode = 0
			d.notifyErrorLocked(0)
		}
	} else {
		d.commandLocked(cmd)
	}
	rose := cw&^prev&CtrlOMS4 != 0
	fell := prev&^cw&CtrlOMS4 != 0
	if d.state != OperationEnabled || d.pending != nil {
		return
	}
	switch d.mode {
	case ModePP:
		if rose {
			d.newSetPointLocked(cw)
		}
		if fell && !d.ppBuf {
			d.ppAck = false
		}
	case ModeHM:
		if rose {
			d.startHomingLocked()
		}
		if fell {
			d.homing = false
		}
	default: // no special case
	}
}

func (d *Drive) commandLocked(cmd Command) {
	to, t, ok := Next(d.state, cmd)
	if !ok {
		d.pending = nil
		return
	}
	switch t {
	case 16:
		if qs := d.getInt(IndexQuickStopOptionCode, 0); qs < 5 || qs > 8 {
			return
		}
	case 5, 8:
		code := IndexDisableOperationOptionCode
		if t == 8 {
			code = IndexShutdownOptionCode
		}
		if d.getInt(code, 0) == 1 && d.vel != 0 {
			d.pending = &Transition{Number: t, From: d.state, To: to}
			return
		}
	}
	d.pending = nil
	d.transitionLocked(to, t)
	switch {
	case t == 3 && cmd.canonical() == EnableOperation:
		d.transitionLocked(OperationEnabled, 4)
	case t == 11 && d.getInt(IndexQuickStopOptionCode, 0) == 0:
		d.vel = 0
		d.transitionLocked(SwitchOnDisabled, 12)
	}
}

// transitionLocked changes the state and schedules the callback.
func (d *Drive) transitionLocked(to State, t int) {
	from := d.state
	d.state = to
	if to == OperationEnabled && from != QuickStopActive {
		d.resetMotionLocked()
	}
	if to != OperationEnabled && to != QuickStopActive && to != FaultReactionActive {
		d.vel, d.torque = 0, 0
		d.resetMotionLocked()
	}
	if to != OperationEnabled {
		d.pending = nil
	}
	if cb := d.cfg.OnTransition; cb != nil {
		d.events = append(d.events, func() { cb(from, to, t) })
	}
}

func (d *Drive) notifyErrorLocked(code uint16) {
	if cb := d.cfg.OnError; cb != nil {
		d.events = append(d.events, func() { cb(code) })
	}
}

func (d *Drive) clampLocked(x float64) (float64, bool) {
	lo, hi, ok := d.limits()
	switch {
	case !ok:
		return x, false
	case x < lo:
		return lo, true
	case x > hi:
		return hi, true
	}
	return x, false
}

func (d *Drive) newSetPointLocked(cw uint16) {
	t := float64(d.getInt(IndexTargetPosition, 0))
	if cw&CtrlRelative != 0 {
		t += d.lastTarget
	}
	t, clamped := d.clampLocked(t)
	switch {
	case cw&CtrlChangeSetImmediately != 0 || !d.ppActive:
		d.ppTarget, d.ppActive, d.ppBuf = t, true, false
	case !d.ppBuf:
		d.ppBufTarget, d.ppBuf = t, true
	default:
		return // buffer full: not acknowledged
	}
	d.ppClamped = clamped
	d.lastTarget = t
	d.ppAck = true
}

func (d *Drive) startHomingLocked() {
	d.homing, d.homeAttained, d.homeError = true, false, false
	if m := d.getInt(IndexHomingMethod, 0); m == 35 || m == 37 {
		d.finishHomingLocked()
	}
}

func (d *Drive) finishHomingLocked() {
	d.pos = float64(d.getInt(IndexHomeOffset, 0))
	d.vel = 0
	d.lastTarget = d.pos
	d.homing, d.homeAttained = false, true
}

// --- application interface

// State returns the present state.
func (d *Drive) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// Statusword returns the present statusword.
func (d *Drive) Statusword() uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.statuswordLocked()
}

// Mode returns the active mode of operation (the 0x6061 value).
func (d *Drive) Mode() Mode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mode
}

// Position returns the actual position in increments.
func (d *Drive) Position() int32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return toInt32(d.pos)
}

// Velocity returns the actual velocity in increments/s.
func (d *Drive) Velocity() int32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return toInt32(d.vel)
}

// Torque returns the actual torque in thousandths of the rated torque.
func (d *Drive) Torque() int16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return toInt16(d.torque)
}

// ErrorCode returns the error code of the last fault (0x603F), 0 when
// there is none.
func (d *Drive) ErrorCode() uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.errCode
}

// InjectFault signals a fault with an error code (CiA 301 / CiA 402 error
// codes, e.g. 0x2310 over-current, 0x5441 or 0xFF00 manufacturer): 0x603F
// is set, OnError is called and, unless the drive is already in Fault
// reaction active or Fault, it takes transition 13 to Fault reaction active,
// then (from a Step on, once the fault reaction stopped the motor)
// transition 14 to Fault.
func (d *Drive) InjectFault(code uint16) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.errCode = code
	d.notifyErrorLocked(code)
	if d.state != Fault && d.state != FaultReactionActive {
		d.transitionLocked(FaultReactionActive, 13)
	}
	d.ver++
	d.mu.Unlock()
	d.flush()
}

// SetWarning sets or clears the warning bit (7) of the statusword.
func (d *Drive) SetWarning(on bool) { d.setFlag(&d.warning, on) }

// SetRemote selects remote control (statusword bit 9, the default) or local
// control, under which controlword writes are refused (remote) or ignored
// (local).
func (d *Drive) SetRemote(on bool) { d.setFlag(&d.remote, on) }

func (d *Drive) setFlag(f *bool, on bool) {
	d.mu.Lock()
	*f = on
	d.ver++
	d.mu.Unlock()
	d.flush()
}

// Run calls Step(period) every period until ctx ends; it returns ctx's
// error.
func (d *Drive) Run(ctx context.Context, period time.Duration) error {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			d.Step(period)
		}
	}
}

// Step advances the simulation by dt.
func (d *Drive) Step(dt time.Duration) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.stepLocked(dt.Seconds())
	d.ver++
	d.mu.Unlock()
	d.flush()
}

// ramp moves v towards target by at most rate·s; rate ≤ 0 is immediate.
func ramp(v, target, rate, s float64) float64 {
	if rate <= 0 {
		return target
	}
	switch step := rate * s; {
	case v < target:
		return math.Min(v+step, target)
	case v > target:
		return math.Max(v-step, target)
	}
	return v
}

// stopRate returns the deceleration of a stop option code: 0 immediate, 1
// and 5 profile deceleration, others quick stop deceleration.
func (d *Drive) stopRate(code int64) float64 {
	switch code {
	case 0:
		return 0
	case 1, 5:
		return float64(d.getUint(IndexProfileDeceleration, 0))
	}
	return float64(d.getUint(IndexQuickStopDeceleration, 0))
}

func (d *Drive) stepLocked(s float64) {
	d.limit = false
	switch d.state {
	case NotReadyToSwitchOn:
		d.transitionLocked(SwitchOnDisabled, 1)
		return
	case FaultReactionActive:
		code := d.getInt(IndexFaultReactionOptionCode, 0)
		d.torque = 0
		d.vel = ramp(d.vel, 0, d.stopRate(code), s)
		d.integrateLocked(s)
		if d.vel == 0 {
			d.transitionLocked(Fault, 14)
		}
		return
	case QuickStopActive:
		code := d.getInt(IndexQuickStopOptionCode, 0)
		d.torque = 0
		d.vel = ramp(d.vel, 0, d.stopRate(code), s)
		d.integrateLocked(s)
		if d.vel == 0 && code <= 4 {
			d.transitionLocked(SwitchOnDisabled, 12)
		}
		return
	case OperationEnabled:
	default:
		d.vel, d.torque = 0, 0
		return
	}
	if p := d.pending; p != nil {
		d.torque = 0
		d.vel = ramp(d.vel, 0, d.stopRate(1), s)
		d.integrateLocked(s)
		if d.vel == 0 {
			d.pending = nil
			d.transitionLocked(p.To, p.Number)
		}
		return
	}
	if d.cw&CtrlHalt != 0 {
		d.torque = 0
		d.vel = ramp(d.vel, 0, d.stopRate(d.getInt(IndexHaltOptionCode, 0)), s)
		d.integrateLocked(s)
		return
	}
	switch d.mode {
	case ModePP:
		d.stepPPLocked(s)
	case ModePV:
		d.torque = 0
		t := float64(d.getInt(IndexTargetVelocity, 0))
		rate := float64(d.getUint(IndexProfileAcceleration, 0))
		if math.Abs(t) < math.Abs(d.vel) || t*d.vel < 0 {
			rate = float64(d.getUint(IndexProfileDeceleration, 0))
		}
		d.vel = ramp(d.vel, t, rate, s)
		d.integrateLocked(s)
	case ModePT, ModeCST:
		d.torque = float64(d.getInt(IndexTargetTorque, 0))
		vss := TorqueGain * TorqueTimeConstant * d.torque
		d.vel = vss + (d.vel-vss)*math.Exp(-s/TorqueTimeConstant)
		d.integrateLocked(s)
	case ModeCSP:
		d.torque = 0
		t, clamped := d.clampLocked(float64(d.getInt(IndexTargetPosition, 0)))
		if s > 0 {
			d.vel = (t - d.pos) / s
		}
		d.pos = t
		d.limit = clamped
	case ModeCSV:
		d.torque = 0
		d.vel = float64(d.getInt(IndexTargetVelocity, 0))
		d.integrateLocked(s)
	case ModeHM:
		d.stepHomingLocked(s)
	default:
		d.torque = 0
		d.vel = ramp(d.vel, 0, float64(d.getUint(IndexProfileDeceleration, 0)), s)
		d.integrateLocked(s)
	}
}

// integrateLocked advances the position with the velocity, within the
// software limits.
func (d *Drive) integrateLocked(s float64) {
	p, clamped := d.clampLocked(d.pos + d.vel*s)
	d.pos = p
	if clamped {
		d.vel = 0
		d.limit = true
	}
}

// approach moves towards target with a trapezoidal profile; it reports
// arrival (then the position is target and the velocity 0).
func (d *Drive) approach(target, vmax, acc, dec, s float64) bool {
	dist := target - d.pos
	if dist == 0 && d.vel == 0 {
		return true
	}
	dir := 1.0
	if dist < 0 {
		dir = -1
	}
	if d.vel*dir < 0 { // moving away: brake first
		d.vel = ramp(d.vel, 0, dec, s)
		d.integrateLocked(s)
		return false
	}
	vdes := vmax
	if dec > 0 {
		vdes = math.Min(vmax, math.Sqrt(2*dec*math.Abs(dist)))
	}
	speed := math.Abs(d.vel)
	if speed < vdes {
		speed = ramp(speed, vdes, acc, s)
	} else {
		speed = ramp(speed, vdes, dec, s)
	}
	next := d.pos + dir*speed*s
	if (target-next)*dir <= 0 {
		d.pos, d.vel = target, 0
		return true
	}
	d.vel = dir * speed
	d.integrateLocked(s)
	return false
}

func (d *Drive) stepPPLocked(s float64) {
	d.torque = 0
	d.limit = d.ppClamped
	if !d.ppActive {
		d.vel = ramp(d.vel, 0, float64(d.getUint(IndexProfileDeceleration, 0)), s)
		d.integrateLocked(s)
		return
	}
	v := float64(d.getUint(IndexProfileVelocity, 0))
	acc := float64(d.getUint(IndexProfileAcceleration, 0))
	dec := float64(d.getUint(IndexProfileDeceleration, 0))
	if d.approach(d.ppTarget, v, acc, dec, s) {
		d.ppActive = false
		if d.ppBuf {
			d.ppTarget, d.ppActive, d.ppBuf = d.ppBufTarget, true, false
			if d.cw&CtrlNewSetPoint == 0 {
				d.ppAck = false
			}
		}
	}
}

func (d *Drive) stepHomingLocked(s float64) {
	d.torque = 0
	acc := float64(d.getUint(IndexHomingAcceleration, 0))
	if !d.homing {
		d.vel = ramp(d.vel, 0, acc, s)
		d.integrateLocked(s)
		return
	}
	speed := float64(d.getUint(IndexHomingSpeeds, 1))
	if speed == 0 {
		d.homing, d.homeError = false, true
		return
	}
	if d.approach(float64(d.cfg.HomeSwitch), speed, acc, acc, s) {
		d.finishHomingLocked()
	}
}

// --- outputs

func (d *Drive) statuswordLocked() uint16 {
	sw := StateBits(d.state)
	if d.state == SwitchOnDisabled || d.state == NotReadyToSwitchOn || d.state == Fault {
		if d.cw&CtrlQuickStop != 0 && d.cw&CtrlEnableVoltage != 0 {
			sw |= StatusQuickStop
		}
	}
	if d.warning {
		sw |= StatusWarning
	}
	if d.remote {
		sw |= StatusRemote
	}
	if d.limit {
		sw |= StatusInternalLimitActive
	}
	stopped := d.vel == 0
	halted := d.cw&CtrlHalt != 0
	switch d.mode {
	case ModePP:
		if (!d.ppActive && !d.ppBuf && stopped) || (halted && stopped) {
			sw |= StatusTargetReached
		}
		if d.ppAck {
			sw |= StatusSetPointAcknowledge
		}
	case ModePV:
		t := float64(d.getInt(IndexTargetVelocity, 0))
		if (halted && stopped) || (!halted && d.vel == t) {
			sw |= StatusTargetReached
		}
		if stopped {
			sw |= StatusSpeedZero
		}
	case ModePT:
		if (halted && stopped) || (!halted && d.torque == float64(d.getInt(IndexTargetTorque, 0))) {
			sw |= StatusTargetReached
		}
	case ModeHM:
		if !d.homing && stopped {
			sw |= StatusTargetReached
		}
		if d.homeAttained {
			sw |= StatusHomingAttained
		}
		if d.homeError {
			sw |= StatusHomingError
		}
	case ModeCSP, ModeCSV, ModeCST:
		if d.state == OperationEnabled && !halted {
			sw |= StatusFollowsCommand
		}
	default: // no special case
	}
	return sw
}

func toInt32(x float64) int32 {
	return int32(math.Max(math.MinInt32, math.Min(math.MaxInt32, math.Round(x))))
}

func toInt16(x float64) int16 {
	return int16(math.Max(math.MinInt16, math.Min(math.MaxInt16, math.Round(x))))
}

func (d *Drive) outputsLocked() outputs {
	return outputs{
		valid:   true,
		sw:      d.statuswordLocked(),
		mode:    d.mode,
		demand:  toInt32(d.pos),
		pos:     toInt32(d.pos),
		vel:     toInt32(d.vel),
		torque:  toInt16(d.torque),
		errCode: d.errCode,
	}
}

// flush publishes the latest outputs to the dictionary and runs the
// callbacks, without holding the drive lock (watchers of the published
// objects may write the controlword). One goroutine publishes at a time; a
// concurrent caller leaves the latest state to it.
func (d *Drive) flush() {
	d.mu.Lock()
	if d.publishing {
		d.mu.Unlock()
		return
	}
	d.publishing = true
	for d.pubVer != d.ver || len(d.events) > 0 {
		out, prev := d.outputsLocked(), d.published
		d.published, d.pubVer = out, d.ver
		events := d.events
		d.events = nil
		d.mu.Unlock()
		set := func(changed bool, index uint16, b []byte) {
			if changed || !prev.valid {
				_ = d.dict.Set(index, 0, b)
			}
		}
		set(out.errCode != prev.errCode, IndexErrorCode, binary.LittleEndian.AppendUint16(nil, out.errCode))
		set(out.mode != prev.mode, IndexModesOfOperationDisplay, []byte{byte(out.mode)})
		set(out.demand != prev.demand, IndexPositionDemandValue, binary.LittleEndian.AppendUint32(nil, uint32(out.demand)))
		set(out.pos != prev.pos, IndexPositionActualValue, binary.LittleEndian.AppendUint32(nil, uint32(out.pos)))
		set(out.vel != prev.vel, IndexVelocityActualValue, binary.LittleEndian.AppendUint32(nil, uint32(out.vel)))
		set(out.torque != prev.torque, IndexTorqueActualValue, binary.LittleEndian.AppendUint16(nil, uint16(out.torque)))
		set(out.sw != prev.sw, IndexStatusword, binary.LittleEndian.AppendUint16(nil, out.sw))
		for _, e := range events {
			e()
		}
		d.mu.Lock()
	}
	d.publishing = false
	d.mu.Unlock()
}
