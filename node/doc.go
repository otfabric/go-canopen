// SPDX-License-Identifier: MIT

// Package node is a complete CANopen device (CiA 301) built from an object
// dictionary: [New] chooses the communication services from the objects
// the dictionary has, [Node.Start] starts them and sends the boot-up
// message, and the NMT state machine enables and disables them.
//
//	d, _ := od.Standard(od.Config{NodeID: 5, HeartbeatMs: 100, RPDOs: 4, TPDOs: 4})
//	// add the application objects, map them into the PDOs...
//	n, _ := node.New(bus, d)
//	_ = n.Start()
//	defer n.Close()
//
// # Services
//
//   - NMT slave: boot-up, the state machine and the resets. A reset node
//     restores 0x2000..0x9FFF and 0x1000..0x1FFF, a reset communication
//     0x1000..0x1FFF, to their defaults (a pending node-ID, see
//     [Node.SetNodeID], applies first, so node-ID-relative COB-IDs follow
//     it).
//   - SDO server on 0x1200 (or the default COB-IDs).
//   - Heartbeat producer (0x1017) and consumer (0x1016); node guarding
//     responder with life guarding (0x100C, 0x100D).
//   - EMCY producer (0x1014, 0x1015, error register 0x1001, error history
//     0x1003), available to the application as [Node.EMCY].
//   - SYNC consumer (0x1005), which drives the synchronous PDOs, and SYNC
//     producer (with 0x1006 and 0x1019), which runs while 0x1005 bit 30 is
//     set and a period (0x1006) is configured; the node's own SYNC drives
//     its PDOs too.
//   - TIME consumer and producer (0x1012 bits 31 and 30).
//   - Every RPDO and TPDO whose communication and mapping objects exist,
//     with the reconfiguration rules of CiA 301 installed (pdo.Protect).
//
// The services follow writes of their objects while they run (an SDO
// write of 0x1017 changes the heartbeat period at once, for example).
//
// # NMT states
//
// As CiA 301 table 79 requires: in Pre-operational the SDO server
// answers and the PDOs are disabled; in Operational both run; in Stopped
// only NMT and error control (heartbeat, node guarding) remain, the SDO
// server ignores requests, and EMCY, SYNC and TIME messages are not sent
// (they are counted in [Stats].Suppressed; an error raised while stopped
// is still recorded in 0x1001 and 0x1003).
//
// # Resets
//
// After an NMT reset the services continue with the restored dictionary:
// they follow the restored values like any other change (PDO COB-IDs,
// heartbeat period...), the heartbeat producer and the guarding responder
// take the new node-ID, the heartbeat consumer restarts its supervision
// (each node supervised by 0x1016 waits for its first heartbeat again),
// and the SDO server is renewed on the COB-IDs of
// 0x1200 (its counters restart). The error state restarts as well: the
// active errors and the history are cleared without an emergency message
// (the boot-up message tells the network about the reset); errors whose
// cause persists must be raised again. The drive of [WithDrive] does so
// for a drive fault after the boot-up.
//
// # Errors
//
// The node raises these emergencies on its own, each cleared (with the
// error reset message once no error remains) when its cause disappears:
//
//   - 0x8130 heartbeat or life guarding loss, while any node supervised by
//     0x1016 or the life guarding is lost (MSEF byte 0: the node-ID, 0 for
//     life guarding). The error behaviour object 0x1029 sub-index 1, when
//     present, then selects the reaction: 0 Pre-operational (from
//     Operational), 1 none, 2 Stopped. Without 0x1029 the state does not
//     change. A resumption clears the error but does not restore the
//     state: the NMT master decides that.
//   - 0x8210 PDO length error, 0x8220 PDO length exceeded, 0x8250 RPDO
//     timeout, reported by the RPDOs (MSEF bytes 0..1: the PDO number).
//     Leaving Operational clears them, as the disabled RPDOs forget them.
//   - 0x8240 unexpected SYNC length.
//   - the drive's error codes with [WithDrive].
//
// # Store and restore
//
// With [WithStore] the node keeps its parameters (the sub-objects with
// access rw, see store.Parameter) in a store.Store: the saved values are
// loaded at New and after every NMT reset (after the defaults of the
// reset areas were restored: so a reset node yields the saved values of
// every area, a reset communication those of 0x1000..0x1FFF). Writing
// "save" to 0x1010 sub-index 1..4 saves a group (all, communication,
// application 0x6000..0x9FFF, manufacturer 0x2000..0x5FFF; [Node.SaveParameters]),
// writing "load" to 0x1011 deletes it so that the defaults apply from the
// next reset ([Node.RestoreDefaults]). A wrong signature is refused with
// the abort "data cannot be transferred or stored" (CiA 301). store.AddObjects
// adds 0x1010, 0x1011 and the verify configuration 0x1020.
//
// # Program download
//
// With [WithProgram] the node serves the program download objects of CiA
// 302-3 (0x1F50 program data, 0x1F51 program control, 0x1F56
// application software identification, 0x1F57 flash status) and leaves
// the flashing to a program.Handler; see package program.
//
// [Node.Close] closes every service, and the services remove the hooks
// and watchers they installed on the dictionary: a dictionary may be
// given to successive nodes (see [od.Dictionary.HookCount]).
//
// Callbacks (the options' handlers) run on the goroutines of the
// services and must not block for long.
package node
