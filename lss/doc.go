// SPDX-License-Identifier: MIT

// Package lss implements the CANopen layer setting services (CiA 305):
// an LSS [Master] that inquires and configures the node-ID and the bit
// timing of devices identified by their 128-bit LSS address (the identity
// object 0x1018), and finds unconfigured devices with Fastscan; and an LSS
// [Slave], the LSS state machine of a device.
//
// # Messages
//
// The master sends on 0x7E5, slaves answer on 0x7E4. Every message has 8
// bytes; byte 0 is the command specifier ([Command]), 32-bit values are
// little-endian in bytes 1..4, unused bytes are 0.
//
//	switch state global      04 mode                              (no response)
//	switch state selective   40..43 value  ->  44                 (vendor, product, revision, serial)
//	configure node-ID        11 id         ->  11 code spec
//	configure bit timing     13 sel index  ->  13 code spec
//	activate bit timing      15 delay(u16 ms)                     (no response)
//	store configuration      17            ->  17 code spec
//	identify remote slave    46..4B value  ->  4F                 (vendor, product, revision low/high, serial low/high)
//	identify non-configured  4C            ->  50
//	Fastscan                 51 id(u32) bitcheck lsssub lssnext  ->  4F
//	inquire identity         5A..5D        ->  5A..5D value
//	inquire node-ID          5E            ->  5E id
//
// # States
//
// A slave is in the waiting or the configuration state ([State]). In the
// waiting state it processes switch state global and selective, identify
// remote slave, identify non-configured remote slave and Fastscan; in the
// configuration state switch state global, the configure, store and
// inquire services and the identify services. A device without node-ID
// has node-ID [Unconfigured] (0xFF).
//
// # Fastscan
//
// A Fastscan request asks every unconfigured slave whose current part
// (LSSPos) is LSSSub to compare bits 31..BitCheck of that part with the
// request's IDNumber and to answer 0x4F when they match; a matching slave
// moves to part LSSNext, and a full match of the serial number with
// LSSNext < LSSSub selects it (configuration state). BitCheck 0x80 resets
// LSSPos and makes every candidate answer. Silence is the "no" answer, so
// every query costs a listening window.
//
// The master implements three algorithms on these queries:
//
//   - [Master.FastscanSelect] identifies the lowest address bit by bit and
//     selects that device, as CANopenNode and python canopen do. Unlike
//     them it decides every bit with an explicit acknowledgement (it
//     queries 0, then 1), repeats unanswered idempotent queries, and
//     restarts a failed identification, so that frame loss costs time
//     instead of a wrong address.
//   - [Master.Fastscan] lists every unconfigured device without selecting
//     any: a depth-first walk of the address tree whose queries keep
//     LSSNext = LSSSub (no slave moves on), re-positioning the slaves
//     (reset, then verification of the prefix) for each part.
//   - [Master.AssignAll] selects devices one by one with FastscanSelect
//     and gives each a node-ID. A device that received a node-ID has a
//     pending node-ID and no longer answers Fastscan, so the next round
//     finds the next device; a device that does answer again (Lely answers
//     Fastscan whatever its node-ID) gets no second node-ID: the remaining
//     devices are then listed with Master.Fastscan and configured by
//     switch state selective. Safeguards against duplicate node-IDs: a
//     node-ID once requested is never offered again; before each round all
//     slaves are switched to waiting and "inquire node-ID" must stay
//     unanswered twice; after a selection the whole address is inquired
//     and only the selected device may answer.
//
// # Sources
//
// CiA 305 is a member document. The protocol details here come from public
// sources, compared where they differ:
//
//   - CANopenNode (Apache-2.0), 305/CO_LSS.h, CO_LSSslave.c,
//     CO_LSSmaster.c: command specifiers, error codes, the Fastscan
//     algorithm and the slave's Fastscan rules (unconfigured devices only:
//     active and pending node-ID 0xFF; mask 0xFFFFFFFF << BitCheck;
//     selection when BitCheck = 0 and LSSNext < LSSSub; 0x80 resets),
//     the bit timing table (index 5 reserved), the reset communication of
//     an unconfigured device that leaves the configuration state with a
//     configured node-ID, node-ID range 1..127 or 0xFF, default timeout
//     1 s.
//   - Lely CANopen (Apache-2.0), src/co/lss.c: identify remote slave
//     (vendor and product equal, revision and serial inclusive bounds,
//     answered after 0x4B, in both states), identify non-configured
//     remote slave (answer 0x50), the in-order check of switch state
//     selective, store and bit timing error codes, the "slowscan" binary
//     search over identify remote slave (not implemented here).
//   - python canopen (MIT), canopen/lss.py: command specifiers,
//     little-endian layout, 8-byte messages, the activate bit timing
//     switch delay semantics, 0.5 s timeout, inter-frame delays some
//     devices need ([WithInhibit]).
//   - Wireshark's CANopen dissector (packet-canopen.c) and this module's
//     dissect package: command names.
//
// # Interpretations
//
// Where the sources differ or are silent, this package chooses as
// follows; the handlers and options make each choice adjustable:
//
//   - Switch state selective must arrive in order (Lely); CANopenNode
//     stores the values and compares at the serial number. A repeated
//     frame (bus duplicate) is tolerated.
//   - Fastscan is answered by unconfigured devices only (CANopenNode); Lely
//     answers regardless of the node-ID.
//   - Inquire node-ID answers the active node-ID (CANopenNode); Lely
//     answers the pending one while the device is initialising.
//   - Identify non-configured remote slave is answered when the active
//     and the pending node-ID are 0xFF; Lely also requires the NMT state
//     initialisation, which this package does not know.
//   - Configure bit timing without a handler answers code 1 (Lely);
//     CANopenNode ignores the request. A handler's rejection answers
//     code 1 (Lely); CANopenNode answers 0xFF with specific error 1. A
//     handler can choose either with a [*ConfigError].
//   - Table selectors other than 0 are passed to the slave's handler; their
//     meaning is not established by the sources.
//   - Frames shorter than a service needs are ignored; shorter frames are
//     otherwise accepted (Lely); CANopenNode requires 8 bytes.
//
// # Configuring a selected device
//
// Switch state global has no answer, so a slave that missed it can stay
// in the configuration state and take a configuration meant for another
// device. [Master.Configure] therefore confirms, before it configures,
// that no slave stays selected (inquire node-ID goes unanswered), checks
// after the selection that only the target answers the identity
// inquiries (else [ErrConflict], nothing configured), and leaves the
// configuration state the same confirmed way.
//
// # Concurrency
//
// The master's services run on the caller's goroutine, one at a time. The
// slave runs one goroutine between Start and Close; its handlers run on it
// and must not block for long.
package lss
