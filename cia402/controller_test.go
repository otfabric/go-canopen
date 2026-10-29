// SPDX-License-Identifier: MIT

package cia402

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// fakeSDO serves SDO transfers from a dictionary in-process. With a drive
// and dt it steps the simulation by dt before every upload, which makes
// the controller tests deterministic in simulated time.
type fakeSDO struct {
	dict  *od.Dictionary
	drive *Drive
	dt    time.Duration

	block       bool  // uploads block until the context ends
	uploadErr   error // returned by every upload
	downloadErr error // returned by every download
	uploads     atomic.Int64
}

func (f *fakeSDO) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	f.uploads.Add(1)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	if f.drive != nil && f.dt > 0 {
		f.drive.Step(f.dt)
	}
	return f.dict.Read(index, sub)
}

func (f *fakeSDO) Download(_ context.Context, index uint16, sub uint8, data []byte) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	return f.dict.Write(index, sub, data)
}

func newRig(t *testing.T, cfg DriveConfig, opts *ControllerOptions) (*testDrive, *fakeSDO, *Controller) {
	t.Helper()
	td := newTestDrive(t, cfg)
	f := &fakeSDO{dict: td.dict, drive: td.drive, dt: time.Millisecond}
	adapted := SDOFuncs{UploadFunc: f.Upload, DownloadFunc: f.Download}
	o := ControllerOptions{PollInterval: time.Microsecond}
	if opts != nil {
		o = *opts
	}
	return td, f, NewController(adapted, &o)
}

func TestControllerGoToFromEveryState(t *testing.T) {
	targets := []struct {
		name string
		to   State
		call func(*Controller, context.Context) error
	}{
		{"enable", OperationEnabled, (*Controller).Enable},
		{"disable operation", SwitchedOn, (*Controller).DisableOperation},
		{"shutdown", ReadyToSwitchOn, (*Controller).Shutdown},
		{"disable", SwitchOnDisabled, (*Controller).Disable},
	}
	for _, from := range allStates {
		for _, tg := range targets {
			t.Run(from.String()+"/"+tg.name, func(t *testing.T) {
				td, _, c := newRig(t, DriveConfig{}, &ControllerOptions{PollInterval: time.Microsecond, ResetFault: true})
				td.reach(from)
				ctx := context.Background()
				if err := c.call(tg.call, ctx); err != nil {
					t.Fatal(err)
				}
				if s := td.state(); s != tg.to {
					t.Fatalf("state %v, want %v", s, tg.to)
				}
				// Every transition taken is one of the diagram.
				td.log.mu.Lock()
				defer td.log.mu.Unlock()
				for _, x := range td.log.log {
					if !ValidTransition(x.Number, x.From, x.To) {
						t.Errorf("invalid transition %+v", x)
					}
				}
			})
		}
	}
}

// call adapts a method expression.
func (c *Controller) call(f func(*Controller, context.Context) error, ctx context.Context) error {
	return f(c, ctx)
}

func TestControllerEnableQuickStopActive(t *testing.T) {
	// Quick stop option 2: the drive passes through Switch on disabled.
	td, _, c := newRig(t, DriveConfig{}, nil)
	td.mustWrite(IndexQuickStopOptionCode, 0, i16(2))
	td.reach(OperationEnabled)
	td.mode(ModePV)
	td.mustWrite(IndexTargetVelocity, 0, i32(10000))
	td.until(1000, func() bool { return td.drive.Velocity() == 10000 })
	td.cw(0x02)
	if td.state() != QuickStopActive {
		t.Fatal(td.state())
	}
	if err := c.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := td.log.numbers(); !containsSeq(got, []int{11, 12, 2, 3, 4}) {
		t.Errorf("transitions %v", got)
	}
}

func containsSeq(a, b []int) bool {
	for i := 0; i+len(b) <= len(a); i++ {
		if equalInts(a[i:i+len(b)], b) {
			return true
		}
	}
	return false
}

func TestControllerFault(t *testing.T) {
	td, _, c := newRig(t, DriveConfig{}, nil)
	td.reach(OperationEnabled)
	td.drive.InjectFault(0x2310)
	ctx := context.Background()
	err := c.Enable(ctx)
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, ErrFault) {
		t.Fatalf("Enable in fault: %v", err)
	}
	if e.State != Fault || e.FaultCode != 0x2310 || e.Op != "enable" {
		t.Errorf("error %+v", e)
	}
	if !strings.Contains(e.Error(), "fault code 0x2310") || !strings.Contains(e.Error(), "Fault") {
		t.Errorf("message %q", e.Error())
	}
	if code, err := c.FaultCode(ctx); err != nil || code != 0x2310 {
		t.Errorf("FaultCode %X %v", code, err)
	}
	if err := c.FaultReset(ctx); err != nil {
		t.Fatal(err)
	}
	if td.state() != SwitchOnDisabled {
		t.Fatal(td.state())
	}
	// No fault: FaultReset does nothing.
	if err := c.FaultReset(ctx); err != nil || td.state() != SwitchOnDisabled {
		t.Errorf("FaultReset without a fault: %v", err)
	}
	// Fault reaction active is waited for.
	td2, _, c2 := newRig(t, DriveConfig{}, nil)
	td2.reach(FaultReactionActive)
	if err := c2.FaultReset(ctx); err != nil || td2.state() != SwitchOnDisabled {
		t.Errorf("FaultReset from FRA: %v %v", err, td2.state())
	}
	// A motion command in fault fails with ErrFault.
	td3, _, c3 := newRig(t, DriveConfig{}, nil)
	td3.reach(Fault)
	if err := c3.MoveTo(ctx, 10, nil); !errors.Is(err, ErrFault) {
		t.Errorf("MoveTo in fault: %v", err)
	}
	// A fault during a move fails the move with the fault code.
	td4, _, c4 := newRig(t, DriveConfig{}, nil)
	td4.reach(OperationEnabled)
	go func() {
		for td4.drive.Position() < 1000 {
			time.Sleep(50 * time.Microsecond)
		}
		td4.drive.InjectFault(0x7300)
	}()
	err = c4.MoveTo(ctx, 100000, &MoveOptions{Wait: true, Timeout: 30 * time.Second})
	if !errors.As(err, &e) || !errors.Is(err, ErrFault) || e.FaultCode != 0x7300 {
		t.Errorf("fault during move: %v", err)
	}
}

func TestControllerQuickStop(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		code int16
		from State
		want State
	}{
		{2, OperationEnabled, SwitchOnDisabled},
		{6, OperationEnabled, QuickStopActive},
		{2, SwitchedOn, SwitchOnDisabled},
		{2, ReadyToSwitchOn, SwitchOnDisabled},
		{2, SwitchOnDisabled, SwitchOnDisabled},
		{2, Fault, Fault},
	} {
		td, _, c := newRig(t, DriveConfig{}, nil)
		td.mustWrite(IndexQuickStopOptionCode, 0, i16(tc.code))
		td.reach(tc.from)
		if err := c.QuickStop(ctx); err != nil {
			t.Fatalf("%v: %v", tc.from, err)
		}
		if tc.code == 2 && tc.from == OperationEnabled {
			// The controller returns on leaving OE (Quick stop active).
			if s := td.state(); s != QuickStopActive && s != SwitchOnDisabled {
				t.Errorf("%v: %v", tc.from, s)
			}
			td.until(1000, func() bool { return td.state() == SwitchOnDisabled })
			continue
		}
		if s := td.state(); s != tc.want {
			t.Errorf("code %d from %v: %v, want %v", tc.code, tc.from, s, tc.want)
		}
	}
}

func TestControllerModes(t *testing.T) {
	td, f, c := newRig(t, DriveConfig{Modes: []Mode{ModePP, ModePV}}, nil)
	ctx := context.Background()
	if err := c.SetMode(ctx, ModePV); err != nil {
		t.Fatal(err)
	}
	if m, _ := c.Mode(ctx); m != ModePV || td.drive.Mode() != ModePV {
		t.Errorf("mode %v", m)
	}
	err := c.SetMode(ctx, ModeCSP)
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, canopen.AbortInvalidValue) || e.Op != "set mode" {
		t.Errorf("unsupported mode: %v", err)
	}
	// Torque mode is not supported: SetTorque fails with the abort.
	if err := c.SetTorque(ctx, 10); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("SetTorque: %v", err)
	}
	// A display that never follows: timeout.
	c2 := NewController(f, &ControllerOptions{PollInterval: time.Microsecond, Timeout: 20 * time.Millisecond})
	td.drive.Close()
	err = c2.SetMode(ctx, ModePP)
	if !errors.Is(err, canopen.ErrTimeout) {
		t.Errorf("mode timeout: %v", err)
	}
}

func TestControllerMoveTo(t *testing.T) {
	td, _, c := newRig(t, DriveConfig{}, nil)
	ctx := context.Background()
	if err := c.MoveTo(ctx, 100, nil); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("MoveTo while disabled: %v", err)
	}
	if err := c.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveTo(ctx, 4000, &MoveOptions{Velocity: 20000, Acceleration: 200000, Deceleration: 200000, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Position(ctx); p != 4000 {
		t.Errorf("absolute: %d", p)
	}
	if v, _ := td.dict.GetUint(IndexProfileVelocity, 0); v != 20000 {
		t.Errorf("profile velocity %d", v)
	}
	if err := c.MoveTo(ctx, -1500, &MoveOptions{Relative: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Position(ctx); p != 2500 {
		t.Errorf("relative: %d", p)
	}
	// Two queued set-points, then wait.
	if err := c.MoveTo(ctx, 3000, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveTo(ctx, 3500, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitTargetReached(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Position(ctx); p != 3500 {
		t.Errorf("queued: %d", p)
	}
	// Immediate change of set-point during a move.
	if err := c.MoveTo(ctx, 50000, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveTo(ctx, 0, &MoveOptions{Immediate: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Position(ctx); p != 0 {
		t.Errorf("immediate: %d", p)
	}
	if c.Controlword()&CtrlNewSetPoint != 0 {
		t.Error("bit 4 left set")
	}
	// A move that cannot finish in time.
	err := c.MoveTo(ctx, 1000000, &MoveOptions{Wait: true, Timeout: 30 * time.Millisecond})
	var e *Error
	if !errors.Is(err, canopen.ErrTimeout) || !errors.As(err, &e) || e.State != OperationEnabled {
		t.Errorf("move timeout: %v", err)
	}
	// Disabled during the wait: ErrNotEnabled.
	if err := c.MoveTo(ctx, -50000, &MoveOptions{Immediate: true}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := td.dict.Watch(func(v *od.Variable, data []byte) {
		if v.Index == IndexPositionActualValue && int32(binary.LittleEndian.Uint32(data)) < -10000 {
			once.Do(func() { _ = td.dict.Write(IndexControlword, 0, u16(0x00)) })
		}
	})
	defer stop()
	if err := c.WaitTargetReached(ctx, 10*time.Second); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("disabled during wait: %v (pos %d sw 0x%04X)", err, td.drive.Position(), td.sw())
	}
}

func TestControllerVelocityTorque(t *testing.T) {
	td, _, c := newRig(t, DriveConfig{}, nil)
	ctx := context.Background()
	if err := c.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SetVelocity(ctx, 2500); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitTargetReached(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if td.drive.Velocity() != 2500 || td.drive.Mode() != ModePV {
		t.Errorf("PV: %d %v", td.drive.Velocity(), td.drive.Mode())
	}
	if err := c.Halt(ctx, true); err != nil {
		t.Fatal(err)
	}
	td.until(1000, func() bool { return td.drive.Velocity() == 0 })
	if err := c.Halt(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetTorque(ctx, 100); err != nil {
		t.Fatal(err)
	}
	td.steps(1000, time.Millisecond)
	if td.drive.Torque() != 100 || td.drive.Velocity() != 1000 || td.drive.Mode() != ModePT {
		t.Errorf("PT: %d %d", td.drive.Torque(), td.drive.Velocity())
	}
}

func TestControllerHome(t *testing.T) {
	td, _, c := newRig(t, DriveConfig{HomeSwitch: 500}, nil)
	ctx := context.Background()
	if err := c.Home(ctx, 1, 0); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Home while disabled: %v", err)
	}
	if err := c.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	td.mustWrite(IndexHomeOffset, 0, i32(-42))
	if err := c.Home(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Position(ctx); p != -42 {
		t.Errorf("homed at %d", p)
	}
	if err := c.Home(ctx, 36, 0); !errors.Is(err, canopen.AbortInvalidValue) {
		t.Errorf("invalid method: %v", err)
	}
	td.mustWrite(IndexHomingSpeeds, 1, u32(0))
	err := c.Home(ctx, 2, 0)
	var e *Error
	if !errors.Is(err, ErrHoming) || !errors.As(err, &e) || e.Statusword&StatusHomingError == 0 {
		t.Errorf("homing error: %v", err)
	}
	// Recovers with a working speed.
	td.mustWrite(IndexHomingSpeeds, 1, u32(10000))
	if err := c.Home(ctx, 2, 0); err != nil {
		t.Fatal(err)
	}
}

func TestControllerTimeouts(t *testing.T) {
	ctx := context.Background()
	// An SDO server that never answers.
	td := newTestDrive(t, DriveConfig{})
	f := &fakeSDO{dict: td.dict, block: true}
	c := NewController(f, &ControllerOptions{Timeout: 20 * time.Millisecond})
	start := time.Now()
	err := c.Enable(ctx)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("Enable against a silent SDO: %v", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c.State(cctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("State: %v", err)
	}
	// A drive that never steps (no simulation): stuck in Not ready.
	f2 := &fakeSDO{dict: td.dict}
	c2 := NewController(f2, &ControllerOptions{Timeout: 20 * time.Millisecond, PollInterval: time.Millisecond})
	err = c2.Enable(ctx)
	var e *Error
	if !errors.Is(err, canopen.ErrTimeout) || !errors.As(err, &e) || e.State != NotReadyToSwitchOn {
		t.Errorf("stuck drive: %v", err)
	}
	// A drive that ignores the controlword (local control).
	td.drive.Step(time.Millisecond)
	td.drive.SetRemote(false)
	err = c2.Enable(ctx)
	if !errors.Is(err, canopen.AbortLocalControl) {
		t.Errorf("local control: %v", err)
	}
	// The caller's context ends first.
	td.drive.SetRemote(true)
	td.drive.Close()
	cctx2, cancel2 := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel2()
	c3 := NewController(f2, &ControllerOptions{Timeout: time.Hour, PollInterval: time.Millisecond})
	if err := c3.Enable(cctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("caller deadline: %v", err)
	}
	// Upload errors are passed on.
	f3 := &fakeSDO{dict: td.dict, uploadErr: canopen.AbortGeneral}
	c4 := NewController(f3, nil)
	for name, fn := range map[string]func() error{
		"enable":      func() error { return c4.Enable(ctx) },
		"fault reset": func() error { return c4.FaultReset(ctx) },
		"quick stop":  func() error { return c4.QuickStop(ctx) },
		"move":        func() error { return c4.MoveTo(ctx, 1, nil) },
		"home":        func() error { return c4.Home(ctx, 1, 0) },
		"velocity":    func() error { return c4.SetVelocity(ctx, 1) },
		"torque":      func() error { return c4.SetTorque(ctx, 1) },
		"mode":        func() error { return c4.SetMode(ctx, ModePP) },
		"wait":        func() error { return c4.WaitTargetReached(ctx, 0) },
	} {
		if err := fn(); !errors.Is(err, canopen.AbortGeneral) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c4.Position(ctx); err == nil {
		t.Error("Position")
	}
	if _, err := c4.FaultCode(ctx); err == nil {
		t.Error("FaultCode")
	}
	if _, err := c4.Mode(ctx); err == nil {
		t.Error("Mode")
	}
}

func TestControllerDownloadErrors(t *testing.T) {
	ctx := context.Background()
	td, f, c := newRig(t, DriveConfig{}, nil)
	td.reach(OperationEnabled)
	td.mode(ModePP)
	f.downloadErr = canopen.AbortHardware
	for name, fn := range map[string]func() error{
		"disable":  func() error { return c.Disable(ctx) },
		"halt":     func() error { return c.Halt(ctx, true) },
		"move":     func() error { return c.MoveTo(ctx, 1, &MoveOptions{Velocity: 5}) },
		"move2":    func() error { return c.MoveTo(ctx, 1, nil) },
		"home":     func() error { return c.Home(ctx, 1, 0) },
		"mode":     func() error { return c.SetMode(ctx, ModePV) },
		"quick":    func() error { return c.QuickStop(ctx) },
		"velocity": func() error { return c.SetVelocity(ctx, 1) },
		"torque":   func() error { return c.SetTorque(ctx, 1) },
	} {
		if err := fn(); !errors.Is(err, canopen.AbortHardware) {
			t.Errorf("%s: %v", name, err)
		}
	}
	td.drive.InjectFault(1)
	td.drive.Step(time.Millisecond)
	c.opts.ResetFault = true
	if err := c.Enable(ctx); !errors.Is(err, canopen.AbortHardware) {
		t.Errorf("fault reset: %v", err)
	}
}

func TestControllerBadStatusword(t *testing.T) {
	d := od.New(1)
	if err := AddObjects(d, 0); err != nil {
		t.Fatal(err)
	}
	_ = d.Set(IndexStatusword, 0, u16(0x0001))
	c := NewController(&fakeSDO{dict: d}, nil)
	if err := c.Enable(context.Background()); !errors.Is(err, ErrStatusword) {
		t.Errorf("unknown statusword: %v", err)
	}
	// Wrong object size.
	d2 := od.New(1)
	_ = d2.Add(od.NewVar(IndexStatusword, "x", canopen.Unsigned8, od.AccessRO, nil))
	c2 := NewController(&fakeSDO{dict: d2}, nil)
	if _, err := c2.State(context.Background()); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("short statusword: %v", err)
	}
}

// chanStatus is a StatusSource fed by statusword watchers, as a TPDO
// listener would be.
type chanStatus struct {
	mu   sync.Mutex
	sw   uint16
	cond *sync.Cond
}

func newChanStatus(d *od.Dictionary) *chanStatus {
	s := &chanStatus{}
	s.cond = sync.NewCond(&s.mu)
	b, _ := d.Get(IndexStatusword, 0)
	s.sw = binary.LittleEndian.Uint16(b)
	d.Watch(func(v *od.Variable, data []byte) {
		if v.Index == IndexStatusword {
			s.mu.Lock()
			s.sw = binary.LittleEndian.Uint16(data)
			s.cond.Broadcast()
			s.mu.Unlock()
		}
	})
	return s
}

func (s *chanStatus) WaitStatusword(ctx context.Context, cond func(uint16) bool) (uint16, error) {
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if cond(s.sw) {
			return s.sw, nil
		}
		if ctx.Err() != nil {
			return s.sw, ctx.Err()
		}
		s.cond.Wait()
	}
}

func TestControllerStatusSource(t *testing.T) {
	td := newTestDrive(t, DriveConfig{})
	f := &fakeSDO{dict: td.dict}
	c := NewController(f, &ControllerOptions{Status: newChanStatus(td.dict), PollInterval: time.Microsecond})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = td.drive.Run(ctx, 200*time.Microsecond) }()
	defer func() { cancel(); wg.Wait() }()
	if err := c.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	before := f.uploads.Load()
	if err := c.MoveTo(ctx, 300, &MoveOptions{Velocity: 100000, Wait: true, Timeout: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if td.drive.Position() != 300 {
		t.Errorf("position %d", td.drive.Position())
	}
	// Only the mode display is read by SDO, not the statusword.
	if n := f.uploads.Load() - before; n > 5 {
		t.Errorf("%d uploads with a status source", n)
	}
}
