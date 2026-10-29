// SPDX-License-Identifier: MIT

package cia402

import (
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Indices of the common CiA 402 objects.
const (
	IndexErrorCode                  uint16 = 0x603F
	IndexControlword                uint16 = 0x6040
	IndexStatusword                 uint16 = 0x6041
	IndexQuickStopOptionCode        uint16 = 0x605A
	IndexShutdownOptionCode         uint16 = 0x605B
	IndexDisableOperationOptionCode uint16 = 0x605C
	IndexHaltOptionCode             uint16 = 0x605D
	IndexFaultReactionOptionCode    uint16 = 0x605E
	IndexModesOfOperation           uint16 = 0x6060
	IndexModesOfOperationDisplay    uint16 = 0x6061
	IndexPositionDemandValue        uint16 = 0x6062
	IndexPositionActualValue        uint16 = 0x6064
	IndexFollowingErrorWindow       uint16 = 0x6065
	IndexVelocityActualValue        uint16 = 0x606C
	IndexTargetTorque               uint16 = 0x6071
	IndexTorqueActualValue          uint16 = 0x6077
	IndexTargetPosition             uint16 = 0x607A
	IndexHomeOffset                 uint16 = 0x607C
	IndexSoftwarePositionLimit      uint16 = 0x607D // sub 1 minimum, sub 2 maximum
	IndexProfileVelocity            uint16 = 0x6081
	IndexProfileAcceleration        uint16 = 0x6083
	IndexProfileDeceleration        uint16 = 0x6084
	IndexQuickStopDeceleration      uint16 = 0x6085
	IndexHomingMethod               uint16 = 0x6098
	IndexHomingSpeeds               uint16 = 0x6099 // sub 1 search for switch, sub 2 search for zero
	IndexHomingAcceleration         uint16 = 0x609A
	IndexTargetVelocity             uint16 = 0x60FF
	IndexSupportedDriveModes        uint16 = 0x6502
)

// Default values the objects of [Objects] start with. Positions are in
// increments, velocities in increments/s, accelerations in increments/s²
// and torques in thousandths of the rated torque.
const (
	DefaultProfileVelocity       = 10000
	DefaultProfileAcceleration   = 100000
	DefaultProfileDeceleration   = 100000
	DefaultQuickStopDeceleration = 1000000
	DefaultHomingSpeed           = 5000
	DefaultHomingAcceleration    = 100000
	DefaultQuickStopOptionCode   = 2 // slow down on quick stop ramp, then switch on disabled
	DefaultShutdownOptionCode    = 0 // disable drive function
	DefaultDisableOperationCode  = 1 // slow down with the slow down ramp, then disable
	DefaultHaltOptionCode        = 1 // slow down on the slow down ramp
	DefaultFaultReactionCode     = 2 // slow down on quick stop ramp
)

// Objects returns the common CiA 402 objects with their CiA 402 data types,
// access rights and PDO mappability. supported is the value of 0x6502 (see
// [SupportedModes]).
func Objects(supported uint32) []*od.Object {
	i8, i16, i32 := canopen.Integer8, canopen.Integer16, canopen.Integer32
	u16, u32 := canopen.Unsigned16, canopen.Unsigned32
	pdo := func(o *od.Object) *od.Object {
		for _, v := range o.Subs {
			if v.SubIndex != 0 || o.Type == od.ObjectVar {
				v.PDOMappable = true
			}
		}
		return o
	}
	return []*od.Object{
		pdo(od.NewVar(IndexErrorCode, "Error code", u16, od.AccessRO, nil)),
		pdo(od.NewVar(IndexControlword, "Controlword", u16, od.AccessRWW, nil)),
		pdo(od.NewVar(IndexStatusword, "Statusword", u16, od.AccessRO, nil)),
		od.NewVar(IndexQuickStopOptionCode, "Quick stop option code", i16, od.AccessRW, int16(DefaultQuickStopOptionCode)),
		od.NewVar(IndexShutdownOptionCode, "Shutdown option code", i16, od.AccessRW, int16(DefaultShutdownOptionCode)),
		od.NewVar(IndexDisableOperationOptionCode, "Disable operation option code", i16, od.AccessRW, int16(DefaultDisableOperationCode)),
		od.NewVar(IndexHaltOptionCode, "Halt option code", i16, od.AccessRW, int16(DefaultHaltOptionCode)),
		od.NewVar(IndexFaultReactionOptionCode, "Fault reaction option code", i16, od.AccessRW, int16(DefaultFaultReactionCode)),
		pdo(od.NewVar(IndexModesOfOperation, "Modes of operation", i8, od.AccessRWW, nil)),
		pdo(od.NewVar(IndexModesOfOperationDisplay, "Modes of operation display", i8, od.AccessRO, nil)),
		pdo(od.NewVar(IndexPositionDemandValue, "Position demand value", i32, od.AccessRO, nil)),
		pdo(od.NewVar(IndexPositionActualValue, "Position actual value", i32, od.AccessRO, nil)),
		pdo(od.NewVar(IndexFollowingErrorWindow, "Following error window", u32, od.AccessRW, nil)),
		pdo(od.NewVar(IndexVelocityActualValue, "Velocity actual value", i32, od.AccessRO, nil)),
		pdo(od.NewVar(IndexTargetTorque, "Target torque", i16, od.AccessRWW, nil)),
		pdo(od.NewVar(IndexTorqueActualValue, "Torque actual value", i16, od.AccessRO, nil)),
		pdo(od.NewVar(IndexTargetPosition, "Target position", i32, od.AccessRWW, nil)),
		pdo(od.NewVar(IndexHomeOffset, "Home offset", i32, od.AccessRW, nil)),
		pdo(od.NewRecord(IndexSoftwarePositionLimit, "Software position limit",
			od.Sub(1, "Min position limit", i32, od.AccessRW, nil),
			od.Sub(2, "Max position limit", i32, od.AccessRW, nil))),
		pdo(od.NewVar(IndexProfileVelocity, "Profile velocity", u32, od.AccessRWW, uint32(DefaultProfileVelocity))),
		pdo(od.NewVar(IndexProfileAcceleration, "Profile acceleration", u32, od.AccessRWW, uint32(DefaultProfileAcceleration))),
		pdo(od.NewVar(IndexProfileDeceleration, "Profile deceleration", u32, od.AccessRWW, uint32(DefaultProfileDeceleration))),
		pdo(od.NewVar(IndexQuickStopDeceleration, "Quick stop deceleration", u32, od.AccessRWW, uint32(DefaultQuickStopDeceleration))),
		pdo(od.NewVar(IndexHomingMethod, "Homing method", i8, od.AccessRWW, nil)),
		pdo(od.NewRecord(IndexHomingSpeeds, "Homing speeds",
			od.Sub(1, "Speed during search for switch", u32, od.AccessRWW, uint32(DefaultHomingSpeed)),
			od.Sub(2, "Speed during search for zero", u32, od.AccessRWW, uint32(DefaultHomingSpeed/10)))),
		pdo(od.NewVar(IndexHomingAcceleration, "Homing acceleration", u32, od.AccessRWW, uint32(DefaultHomingAcceleration))),
		pdo(od.NewVar(IndexTargetVelocity, "Target velocity", i32, od.AccessRWW, nil)),
		od.NewVar(IndexSupportedDriveModes, "Supported drive modes", u32, od.AccessConst, supported),
	}
}

// AddObjects adds the objects of [Objects] to a dictionary. Objects the
// dictionary already has are left as they are, so an application can define
// some of them itself (other limits, defaults or mappability) first.
func AddObjects(d *od.Dictionary, supported uint32) error {
	for _, o := range Objects(supported) {
		if d.Object(o.Index) != nil {
			continue
		}
		if err := d.Add(o); err != nil {
			return err
		}
	}
	return nil
}
