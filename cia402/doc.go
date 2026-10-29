// SPDX-License-Identifier: MIT

// Package cia402 implements the CiA 402 drive profile (IEC 61800-7-201):
// the power drive state machine, the modes of operation and the common
// 0x6xxx objects, a device-side drive model and a master-side controller.
//
// # Profile
//
// [State] and [DecodeStatusword] read the statusword (0x6041) with the CiA
// 402 patterns; [Command] encodes the device control commands into the
// controlword (0x6040) and [DecodeControlword] decodes them. [Next] is the
// state diagram (transitions 0..16, also listed by [Transitions]) and [Path]
// the shortest command sequence between two states. [Mode] names the modes
// of operation (0x6060/0x6061) and their bits in the supported drive modes
// (0x6502). [Objects] and [AddObjects] define the common objects with their
// data types, access rights and PDO mappability.
//
// # Device side
//
// [Drive] is a CiA 402 device model on an [od.Dictionary]. Controlword and
// mode writes, by SDO or RPDO (remote dictionary writes) or by the
// application (local writes), drive its state machine; refused writes are
// answered with SDO abort codes. A deterministic simulation advanced by
// [Drive.Step] moves a virtual axis in profile position (trapezoidal
// profile, set-point handshake), profile velocity, profile torque, homing
// and the cyclic synchronous modes, and implements quick stop, halt and
// fault reactions per their option codes. [Drive.InjectFault] simulates a
// fault and reports it through [DriveConfig.OnError], where a node emits its
// EMCY message.
//
// # Master side
//
// [Controller] operates a remote drive through any [SDO] client (the sdo
// package's client through [SDOFuncs]): [Controller.Enable] walks the state diagram
// step by step, verifying each state through the statusword, and resets a
// fault when [ControllerOptions.ResetFault] allows it; [Controller.MoveTo]
// performs profile position moves with the set-point handshake;
// [Controller.SetVelocity], [Controller.SetTorque] and [Controller.Home]
// cover PV, PT and HM. Failures are [*Error] values carrying the state
// reached and the fault code. The statusword is polled by SDO unless a
// [StatusSource] (e.g. fed by a TPDO) provides it.
//
// [od.Dictionary]: https://pkg.go.dev/github.com/otfabric/go-canopen/od#Dictionary
package cia402
