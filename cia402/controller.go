// SPDX-License-Identifier: MIT

package cia402

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
)

// SDO is the SDO client a [Controller] uses. Errors are returned unchanged
// inside an [*Error]. The sdo package's client takes extra call options;
// wrap it with [SDOFuncs]:
//
//	cia402.SDOFuncs{
//		UploadFunc: func(ctx context.Context, i uint16, s uint8) ([]byte, error) { return cl.Upload(ctx, i, s) },
//		DownloadFunc: func(ctx context.Context, i uint16, s uint8, b []byte) error { return cl.Download(ctx, i, s, b) },
//	}
type SDO interface {
	Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error)
	Download(ctx context.Context, index uint16, sub uint8, data []byte) error
}

// SDOFuncs adapts two functions to the [SDO] interface.
type SDOFuncs struct {
	UploadFunc   func(ctx context.Context, index uint16, sub uint8) ([]byte, error)
	DownloadFunc func(ctx context.Context, index uint16, sub uint8, data []byte) error
}

// Upload calls UploadFunc.
func (f SDOFuncs) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	return f.UploadFunc(ctx, index, sub)
}

// Download calls DownloadFunc.
func (f SDOFuncs) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	return f.DownloadFunc(ctx, index, sub, data)
}

// StatusSource delivers the statusword of the drive. The default polls
// 0x6041 by SDO; a source fed by a TPDO listener reacts faster and spares
// the bus.
type StatusSource interface {
	// WaitStatusword blocks until cond reports true for a statusword (the
	// latest known one or a new one), or ctx ends. It returns the last
	// statusword seen; on ctx's end with ctx's error.
	WaitStatusword(ctx context.Context, cond func(sw uint16) bool) (uint16, error)
}

// Sentinel errors, carried by [*Error] (test with errors.Is). A per-step
// timeout is reported as [canopen.ErrTimeout], the end of the caller's
// context as the context's error.
var (
	// ErrFault reports a drive in Fault or Fault reaction active.
	ErrFault = errors.New("cia402: drive fault")
	// ErrNotEnabled reports a motion command for a drive that is not (or no
	// longer) in Operation enabled.
	ErrNotEnabled = errors.New("cia402: operation not enabled")
	// ErrHoming reports a homing error (statusword bit 13 in HM).
	ErrHoming = errors.New("cia402: homing error")
	// ErrUnreachable reports a state that cannot be reached by commands.
	ErrUnreachable = errors.New("cia402: state unreachable")
	// ErrStatusword reports a statusword that matches no state.
	ErrStatusword = errors.New("cia402: invalid statusword")
)

// Error is the error of a [Controller] operation: the cause, and the drive
// state the operation ended in.
type Error struct {
	// Op is the operation: "enable", "move", "set mode"...
	Op string
	// State is the last state seen, Unknown if none.
	State State
	// Statusword is the last statusword seen.
	Statusword uint16
	// FaultCode is the error code 0x603F when the drive is in a fault
	// state (0 when it could not be read).
	FaultCode uint16
	// Err is the cause.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	s := fmt.Sprintf("cia402: %s: %v (state %s, statusword 0x%04X", e.Op, e.Err, e.State, e.Statusword)
	if e.FaultCode != 0 {
		s += fmt.Sprintf(", fault code 0x%04X", e.FaultCode)
	}
	return s + ")"
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// ControllerOptions configures a [Controller]; the zero value selects the
// defaults.
type ControllerOptions struct {
	// Timeout bounds every step of an operation (one state transition, a
	// mode change, a set-point handshake, a move); default 2 s.
	Timeout time.Duration
	// PollInterval is the pause between two statusword polls; default
	// 10 ms. Used when Status is nil, and for 0x6061.
	PollInterval time.Duration
	// Status replaces the SDO polling of the statusword.
	Status StatusSource
	// ResetFault lets state changes (Enable, Disable, Shutdown,
	// DisableOperation) leave Fault by a fault reset; otherwise they fail
	// with ErrFault.
	ResetFault bool
}

// MoveOptions are the options of a profile position move.
type MoveOptions struct {
	// Relative moves relative to the preceding target position (bit 6).
	Relative bool
	// Immediate replaces an active set-point instead of queueing behind it
	// (change set immediately, bit 5).
	Immediate bool
	// Velocity, Acceleration and Deceleration are written to 0x6081, 0x6083
	// and 0x6084 when not 0.
	Velocity, Acceleration, Deceleration uint32
	// Wait waits for target reached (bit 10).
	Wait bool
	// Timeout bounds the wait for target reached; 0 means the controller's
	// Timeout.
	Timeout time.Duration
}

// Controller drives a remote CiA 402 drive through its SDO server: the
// state machine (statusword-verified, step by step along [Path]), modes of
// operation, profile position moves with the set-point handshake, profile
// velocity and torque, and homing. Operations may run concurrently (e.g. a
// QuickStop while a MoveTo waits); the controlword is written
// read-modify-write under a lock.
type Controller struct {
	sdo  SDO
	opts ControllerOptions

	mu      sync.Mutex
	cw      uint16
	cwKnown bool
}

// NewController returns a controller; opts may be nil.
func NewController(sdo SDO, opts *ControllerOptions) *Controller {
	c := &Controller{sdo: sdo}
	if opts != nil {
		c.opts = *opts
	}
	if c.opts.Timeout <= 0 {
		c.opts.Timeout = 2 * time.Second
	}
	if c.opts.PollInterval <= 0 {
		c.opts.PollInterval = 10 * time.Millisecond
	}
	return c
}

// op carries the context of one operation for error reporting.
type op struct {
	c    *Controller
	name string
	sw   uint16
	seen bool
}

func (o *op) fail(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		err = canopen.ErrTimeout // a step timeout, not the caller's deadline
	}
	e := &Error{Op: o.name, Err: err}
	if o.seen {
		e.Statusword = o.sw
		e.State = DecodeStatusword(o.sw)
		if e.State == Fault || e.State == FaultReactionActive {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.c.opts.Timeout)
			e.FaultCode, _ = o.c.FaultCode(rctx)
			cancel()
		}
	}
	return e
}

// wait waits, at most for the step timeout, for a statusword satisfying
// cond. A drive falling into a fault state fails with ErrFault; when
// needEnabled, a drive leaving Operation enabled fails with ErrNotEnabled.
func (o *op) wait(ctx context.Context, timeout time.Duration, needEnabled bool, cond func(sw uint16) bool) error {
	if timeout <= 0 {
		timeout = o.c.opts.Timeout
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var abort error
	check := func(sw uint16) bool {
		o.sw, o.seen = sw, true
		switch st := DecodeStatusword(sw); {
		case st == Fault || st == FaultReactionActive:
			abort = ErrFault
		case needEnabled && st != OperationEnabled:
			abort = ErrNotEnabled
		default:
			return cond(sw)
		}
		return true
	}
	_, err := o.c.waitStatusword(sctx, check)
	switch {
	case err == nil:
		if abort != nil {
			return o.fail(ctx, abort)
		}
		return nil
	case ctx.Err() != nil:
		return o.fail(ctx, ctx.Err())
	case sctx.Err() != nil:
		return o.fail(ctx, canopen.ErrTimeout)
	}
	return o.fail(ctx, err)
}

// waitStatusword is the default StatusSource: SDO polling.
func (c *Controller) waitStatusword(ctx context.Context, cond func(uint16) bool) (uint16, error) {
	if c.opts.Status != nil {
		return c.opts.Status.WaitStatusword(ctx, cond)
	}
	for {
		sw, err := c.Statusword(ctx)
		if err != nil {
			return sw, err
		}
		if cond(sw) {
			return sw, nil
		}
		if err := sleep(ctx, c.opts.PollInterval); err != nil {
			return sw, err
		}
	}
}

// current returns the latest statusword, bounded by the step timeout.
func (c *Controller) current(ctx context.Context) (uint16, error) {
	sctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	return c.waitStatusword(sctx, func(uint16) bool { return true })
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// upload reads an object of n bytes, bounded by the step timeout.
func (c *Controller) upload(ctx context.Context, index uint16, sub uint8, n int) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	b, err := c.sdo.Upload(sctx, index, sub)
	if err != nil {
		return nil, err
	}
	if len(b) != n {
		return nil, fmt.Errorf("%w: object 0x%04X:%02X has %d bytes, want %d", canopen.ErrDataType, index, sub, len(b), n)
	}
	return b, nil
}

// download writes an object, bounded by the step timeout.
func (c *Controller) download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	sctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	return c.sdo.Download(sctx, index, sub, data)
}

// Statusword reads the statusword (0x6041) by SDO.
func (c *Controller) Statusword(ctx context.Context) (uint16, error) {
	b, err := c.upload(ctx, IndexStatusword, 0, 2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// State returns the drive's state from the status source (by default an SDO
// read of the statusword).
func (c *Controller) State(ctx context.Context) (State, error) {
	sw, err := c.current(ctx)
	if err != nil {
		return Unknown, err
	}
	return DecodeStatusword(sw), nil
}

// FaultCode reads the error code (0x603F).
func (c *Controller) FaultCode(ctx context.Context) (uint16, error) {
	b, err := c.upload(ctx, IndexErrorCode, 0, 2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// Mode reads the modes of operation display (0x6061).
func (c *Controller) Mode(ctx context.Context) (Mode, error) {
	b, err := c.upload(ctx, IndexModesOfOperationDisplay, 0, 1)
	if err != nil {
		return 0, err
	}
	return Mode(int8(b[0])), nil
}

// Position reads the position actual value (0x6064).
func (c *Controller) Position(ctx context.Context) (int32, error) {
	b, err := c.upload(ctx, IndexPositionActualValue, 0, 4)
	if err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b)), nil
}

// Controlword returns the controlword the controller wrote last.
func (c *Controller) Controlword() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cw
}

// writeCW writes f(last controlword) and remembers it. The first write
// reads the drive's controlword, so that a drive enabled by someone else
// is not disabled by bits the controller does not know.
func (c *Controller) writeCW(ctx context.Context, f func(cw uint16) uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cwKnown {
		b, err := c.upload(ctx, IndexControlword, 0, 2)
		if err != nil {
			return err
		}
		c.cw, c.cwKnown = binary.LittleEndian.Uint16(b), true
	}
	cw := f(c.cw)
	if err := c.download(ctx, IndexControlword, 0, binary.LittleEndian.AppendUint16(nil, cw)); err != nil {
		return err
	}
	c.cw = cw
	return nil
}

// command writes a device control command, clearing the mode-specific bits
// 4..6 (a pending set-point or homing start is not repeated).
func (c *Controller) command(ctx context.Context, cmd Command) error {
	return c.writeCW(ctx, func(cw uint16) uint16 {
		return cmd.Controlword(cw &^ (CtrlOMS4 | CtrlOMS5 | CtrlOMS6))
	})
}

// Enable brings the drive to Operation enabled.
func (c *Controller) Enable(ctx context.Context) error {
	return c.goTo(ctx, "enable", OperationEnabled)
}

// DisableOperation brings the drive to Switched on.
func (c *Controller) DisableOperation(ctx context.Context) error {
	return c.goTo(ctx, "disable operation", SwitchedOn)
}

// Shutdown brings the drive to Ready to switch on.
func (c *Controller) Shutdown(ctx context.Context) error {
	return c.goTo(ctx, "shutdown", ReadyToSwitchOn)
}

// Disable brings the drive to Switch on disabled (power stage off).
func (c *Controller) Disable(ctx context.Context) error {
	return c.goTo(ctx, "disable", SwitchOnDisabled)
}

// goTo walks the shortest path to the target state one transition at a
// time, verifying each through the statusword and recomputing the path
// from the state actually reached.
func (c *Controller) goTo(ctx context.Context, name string, target State) error {
	o := &op{c: c, name: name}
	for range 16 {
		sw, err := c.current(ctx)
		if err != nil {
			return o.fail(ctx, err)
		}
		o.sw, o.seen = sw, true
		st := DecodeStatusword(sw)
		switch st {
		case target:
			return nil
		case Unknown:
			return o.fail(ctx, ErrStatusword)
		case NotReadyToSwitchOn, FaultReactionActive:
			if err := o.waitLeave(ctx, st); err != nil {
				return err
			}
			continue
		case Fault:
			if !c.opts.ResetFault {
				return o.fail(ctx, ErrFault)
			}
			if err := c.faultReset(ctx, o); err != nil {
				return err
			}
			continue
		}
		path := Path(st, target)
		if len(path) == 0 {
			return o.fail(ctx, ErrUnreachable)
		}
		if err := c.command(ctx, path[0]); err != nil {
			return o.fail(ctx, err)
		}
		if err := o.waitLeave(ctx, st); err != nil {
			return err
		}
	}
	return o.fail(ctx, ErrUnreachable)
}

// waitLeave waits for the drive to leave a state.
func (o *op) waitLeave(ctx context.Context, st State) error {
	sctx, cancel := context.WithTimeout(ctx, o.c.opts.Timeout)
	defer cancel()
	sw, err := o.c.waitStatusword(sctx, func(sw uint16) bool { return DecodeStatusword(sw) != st })
	if err == nil {
		o.sw, o.seen = sw, true
		return nil
	}
	if sw != 0 || o.seen {
		o.sw, o.seen = sw, true
	}
	switch {
	case ctx.Err() != nil:
		return o.fail(ctx, ctx.Err())
	case sctx.Err() != nil:
		return o.fail(ctx, canopen.ErrTimeout)
	}
	return o.fail(ctx, err)
}

// FaultReset resets a fault: a rising edge of controlword bit 7, then a
// wait for Switch on disabled. A drive in Fault reaction active is waited
// for first; a drive without a fault is left alone.
func (c *Controller) FaultReset(ctx context.Context) error {
	o := &op{c: c, name: "fault reset"}
	sw, err := c.current(ctx)
	if err != nil {
		return o.fail(ctx, err)
	}
	o.sw, o.seen = sw, true
	st := DecodeStatusword(sw)
	if st == FaultReactionActive {
		if err := o.waitLeave(ctx, st); err != nil {
			return err
		}
		st = DecodeStatusword(o.sw)
	}
	if st != Fault {
		return nil
	}
	return c.faultReset(ctx, o)
}

func (c *Controller) faultReset(ctx context.Context, o *op) error {
	if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw &^ (CtrlCommandMask | CtrlOMS4 | CtrlOMS5 | CtrlOMS6) }); err != nil {
		return o.fail(ctx, err)
	}
	if err := c.command(ctx, FaultReset); err != nil {
		return o.fail(ctx, err)
	}
	return o.waitLeave(ctx, Fault)
}

// QuickStop sends the quick stop command. From Operation enabled it waits
// for Quick stop active or, per the drive's quick stop option code, for
// Switch on disabled; from Ready to switch on and Switched on for Switch on
// disabled. In other states it does nothing.
func (c *Controller) QuickStop(ctx context.Context) error {
	o := &op{c: c, name: "quick stop"}
	sw, err := c.current(ctx)
	if err != nil {
		return o.fail(ctx, err)
	}
	o.sw, o.seen = sw, true
	st := DecodeStatusword(sw)
	if _, _, ok := Next(st, QuickStop); !ok {
		return nil
	}
	if err := c.command(ctx, QuickStop); err != nil {
		return o.fail(ctx, err)
	}
	return o.waitLeave(ctx, st)
}

// Halt sets or clears the halt bit (8) of the controlword.
func (c *Controller) Halt(ctx context.Context, on bool) error {
	err := c.writeCW(ctx, func(cw uint16) uint16 {
		if on {
			return cw | CtrlHalt
		}
		return cw &^ CtrlHalt
	})
	if err != nil {
		return &Error{Op: "halt", Err: err}
	}
	return nil
}

// SetMode writes the modes of operation (0x6060) and waits until the modes
// of operation display (0x6061) shows it.
func (c *Controller) SetMode(ctx context.Context, m Mode) error {
	o := &op{c: c, name: "set mode"}
	if err := c.download(ctx, IndexModesOfOperation, 0, []byte{byte(m)}); err != nil {
		return o.fail(ctx, err)
	}
	sctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	for {
		got, err := c.Mode(sctx)
		if err == nil && got == m {
			return nil
		}
		if err == nil {
			err = sleep(sctx, c.opts.PollInterval)
		}
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return o.fail(ctx, ctx.Err())
			case sctx.Err() != nil:
				return o.fail(ctx, fmt.Errorf("%w: mode %v not displayed", canopen.ErrTimeout, m))
			}
			return o.fail(ctx, err)
		}
	}
}

// ensureMode switches to m unless the drive displays it already.
func (c *Controller) ensureMode(ctx context.Context, o *op, m Mode) error {
	got, err := c.Mode(ctx)
	if err != nil {
		return o.fail(ctx, err)
	}
	if got == m {
		return nil
	}
	if err := c.SetMode(ctx, m); err != nil {
		var e *Error
		if errors.As(err, &e) {
			e.Op = o.name
		}
		return err
	}
	return nil
}

// requireEnabled fails unless the drive is in Operation enabled.
func (c *Controller) requireEnabled(ctx context.Context, o *op) error {
	sw, err := c.current(ctx)
	if err != nil {
		return o.fail(ctx, err)
	}
	o.sw, o.seen = sw, true
	switch DecodeStatusword(sw) {
	case OperationEnabled:
		return nil
	case Fault, FaultReactionActive:
		return o.fail(ctx, ErrFault)
	}
	return o.fail(ctx, ErrNotEnabled)
}

// MoveTo moves to a target position in profile position mode (switching to
// PP when needed): it writes 0x607A and the profile parameters, performs
// the set-point handshake (bit 4 up, wait for set-point acknowledge bit 12,
// bit 4 down) and, with opts.Wait, waits for target reached (bit 10). The
// drive must be in Operation enabled. opts may be nil.
func (c *Controller) MoveTo(ctx context.Context, target int32, opts *MoveOptions) error {
	var mo MoveOptions
	if opts != nil {
		mo = *opts
	}
	o := &op{c: c, name: "move"}
	if err := c.requireEnabled(ctx, o); err != nil {
		return err
	}
	if err := c.ensureMode(ctx, o, ModePP); err != nil {
		return err
	}
	for _, p := range []struct {
		index uint16
		v     uint32
	}{{IndexProfileVelocity, mo.Velocity}, {IndexProfileAcceleration, mo.Acceleration}, {IndexProfileDeceleration, mo.Deceleration}} {
		if p.v == 0 {
			continue
		}
		if err := c.download(ctx, p.index, 0, binary.LittleEndian.AppendUint32(nil, p.v)); err != nil {
			return o.fail(ctx, err)
		}
	}
	// The drive must be ready for a new set-point: bit 4 low, ack low.
	if c.Controlword()&CtrlNewSetPoint != 0 {
		if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw &^ CtrlNewSetPoint }); err != nil {
			return o.fail(ctx, err)
		}
	}
	if err := o.wait(ctx, 0, true, func(sw uint16) bool { return sw&StatusSetPointAcknowledge == 0 }); err != nil {
		return err
	}
	if err := c.download(ctx, IndexTargetPosition, 0, binary.LittleEndian.AppendUint32(nil, uint32(target))); err != nil {
		return o.fail(ctx, err)
	}
	flags := CtrlNewSetPoint
	if mo.Immediate {
		flags |= CtrlChangeSetImmediately
	}
	if mo.Relative {
		flags |= CtrlRelative
	}
	if err := c.writeCW(ctx, func(cw uint16) uint16 {
		return cw&^(CtrlOMS4|CtrlOMS5|CtrlOMS6) | flags
	}); err != nil {
		return o.fail(ctx, err)
	}
	if err := o.wait(ctx, 0, true, func(sw uint16) bool { return sw&StatusSetPointAcknowledge != 0 }); err != nil {
		return err
	}
	if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw &^ CtrlNewSetPoint }); err != nil {
		return o.fail(ctx, err)
	}
	if !mo.Wait {
		return nil
	}
	return o.wait(ctx, mo.Timeout, true, func(sw uint16) bool { return sw&StatusTargetReached != 0 })
}

// WaitTargetReached waits for target reached (bit 10) in Operation enabled,
// at most timeout (0: the controller's Timeout).
func (c *Controller) WaitTargetReached(ctx context.Context, timeout time.Duration) error {
	o := &op{c: c, name: "wait target reached"}
	return o.wait(ctx, timeout, true, func(sw uint16) bool { return sw&StatusTargetReached != 0 })
}

// SetVelocity writes the target velocity (0x60FF) in profile velocity mode,
// switching to PV when needed. It does not wait for the velocity; see
// WaitTargetReached.
func (c *Controller) SetVelocity(ctx context.Context, v int32) error {
	o := &op{c: c, name: "set velocity"}
	if err := c.ensureMode(ctx, o, ModePV); err != nil {
		return err
	}
	if err := c.download(ctx, IndexTargetVelocity, 0, binary.LittleEndian.AppendUint32(nil, uint32(v))); err != nil {
		return o.fail(ctx, err)
	}
	return nil
}

// SetTorque writes the target torque (0x6071, thousandths of the rated
// torque) in profile torque mode, switching to PT when needed.
func (c *Controller) SetTorque(ctx context.Context, t int16) error {
	o := &op{c: c, name: "set torque"}
	if err := c.ensureMode(ctx, o, ModePT); err != nil {
		return err
	}
	if err := c.download(ctx, IndexTargetTorque, 0, binary.LittleEndian.AppendUint16(nil, uint16(t))); err != nil {
		return o.fail(ctx, err)
	}
	return nil
}

// Home runs a homing method (0x6098) in homing mode, switching to HM when
// needed: it starts homing with a rising edge of bit 4 and waits, at most
// timeout (0: the controller's Timeout), for homing attained with target
// reached (bits 12 and 10), failing with ErrHoming on a homing error (bit
// 13). The drive must be in Operation enabled.
func (c *Controller) Home(ctx context.Context, method int8, timeout time.Duration) error {
	o := &op{c: c, name: "home"}
	if err := c.requireEnabled(ctx, o); err != nil {
		return err
	}
	if err := c.download(ctx, IndexHomingMethod, 0, []byte{byte(method)}); err != nil {
		return o.fail(ctx, err)
	}
	if err := c.ensureMode(ctx, o, ModeHM); err != nil {
		return err
	}
	if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw &^ (CtrlOMS4 | CtrlOMS5 | CtrlOMS6) }); err != nil {
		return o.fail(ctx, err)
	}
	if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw | CtrlHomingStart }); err != nil {
		return o.fail(ctx, err)
	}
	const done = StatusHomingAttained | StatusTargetReached
	err := o.wait(ctx, timeout, true, func(sw uint16) bool {
		return sw&StatusHomingError != 0 || sw&done == done
	})
	if err != nil {
		return err
	}
	if o.sw&StatusHomingError != 0 {
		return o.fail(ctx, ErrHoming)
	}
	if err := c.writeCW(ctx, func(cw uint16) uint16 { return cw &^ CtrlHomingStart }); err != nil {
		return o.fail(ctx, err)
	}
	return nil
}
