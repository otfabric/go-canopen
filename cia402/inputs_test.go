// SPDX-License-Identifier: MIT

package cia402

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestDriveRunPeriod: Run refuses a period that is not positive with an
// error, at once; a positive period runs until the context ends.
func TestDriveRunPeriod(t *testing.T) {
	for _, period := range []time.Duration{0, -1, -time.Millisecond, math.MinInt64} {
		td := newTestDrive(t, DriveConfig{})
		ctx, cancel := context.WithCancel(context.Background())
		err := td.drive.Run(ctx, period)
		cancel()
		if err == nil {
			t.Errorf("Run with period %v = nil, want an error", period)
		}
	}
	td := newTestDrive(t, DriveConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := td.drive.Run(ctx, time.Nanosecond); err != context.Canceled {
		t.Errorf("Run with period 1ns and an ended context = %v, want context.Canceled", err)
	}
}

// TestDriveStepNegative: a negative dt leaves the simulation as it is;
// dt 0 and a positive dt advance it as before.
func TestDriveStepNegative(t *testing.T) {
	for _, dt := range []time.Duration{-1, -time.Millisecond, -10 * time.Second, math.MinInt64} {
		td := newTestDrive(t, DriveConfig{})
		td.reach(OperationEnabled)
		td.mode(ModePT)
		td.mustWrite(IndexTargetTorque, 0, i16(-50))
		td.steps(20, time.Millisecond) // accelerating, not at the steady state
		pos, vel, torque, sw := td.drive.Position(), td.drive.Velocity(), td.drive.Torque(), td.sw()
		if vel == 0 {
			t.Fatal("the drive does not move")
		}
		td.drive.Step(dt)
		if p, v, tq, s := td.drive.Position(), td.drive.Velocity(), td.drive.Torque(), td.sw(); p != pos || v != vel || tq != torque || s != sw {
			t.Errorf("Step(%v): position %d velocity %d torque %d sw 0x%04X, want %d %d %d 0x%04X",
				dt, p, v, tq, s, pos, vel, torque, sw)
		}
		td.drive.Step(0)
		if v := td.drive.Velocity(); v != vel {
			t.Errorf("Step(0) after Step(%v): velocity %d, want %d", dt, v, vel)
		}
		td.drive.Step(time.Millisecond)
		if v := td.drive.Velocity(); v >= vel {
			t.Errorf("Step(1ms) after Step(%v): velocity %d, want below %d", dt, v, vel)
		}
	}
}

// TestFloatToInt: the conversions round to the nearest value, saturate at
// the limits of the type and give 0 for NaN.
func TestFloatToInt(t *testing.T) {
	for _, tc := range []struct {
		x   float64
		i32 int32
		i16 int16
	}{
		{0, 0, 0},
		{1.4, 1, 1},
		{-1.5, -2, -2},
		{math.MaxInt16 - 1, math.MaxInt16 - 1, math.MaxInt16 - 1},
		{math.MaxInt16, math.MaxInt16, math.MaxInt16},
		{math.MaxInt16 + 1, math.MaxInt16 + 1, math.MaxInt16},
		{math.MinInt16 - 1, math.MinInt16 - 1, math.MinInt16},
		{math.MaxInt32 - 1, math.MaxInt32 - 1, math.MaxInt16},
		{math.MaxInt32, math.MaxInt32, math.MaxInt16},
		{math.MaxInt32 + 1, math.MaxInt32, math.MaxInt16},
		{math.MinInt32, math.MinInt32, math.MinInt16},
		{math.MinInt32 - 1, math.MinInt32, math.MinInt16},
		{math.MaxFloat64, math.MaxInt32, math.MaxInt16},
		{math.Inf(1), math.MaxInt32, math.MaxInt16},
		{math.Inf(-1), math.MinInt32, math.MinInt16},
		{math.NaN(), 0, 0},
	} {
		if got := toInt32(tc.x); got != tc.i32 {
			t.Errorf("toInt32(%v) = %d, want %d", tc.x, got, tc.i32)
		}
		if got := toInt16(tc.x); got != tc.i16 {
			t.Errorf("toInt16(%v) = %d, want %d", tc.x, got, tc.i16)
		}
	}
}
