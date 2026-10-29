// SPDX-License-Identifier: MIT

// Package usdo implements the universal service data object (USDO) of
// CANopen FD (CiA 1301), which replaces the SDO: a [Client] reads and
// writes the object dictionaries of [Server]s over CAN FD frames, to one
// node, to a group or to all nodes, with several transfers at once.
//
// CiA 1301 is a member document. This package implements what public
// sources establish and says where it had to interpret; the details below
// name their source.
//
// # Addressing (established)
//
// A client sends on CAN-ID 600h + its own node-ID, a server answers on
// 580h + its node-ID: "CAN-ID identifies the sender", the destination is
// coded in the data (CiA's CANopen FD poster, 2026 edition, "Universal
// service data object" and "Pre-defined CAN-IDs"; a MicroControl CANopen
// FD master trace shows a request of client 7Fh on 67Fh). Byte 0 is the
// destination address: 00h broadcast, 01h..7Fh a node-ID, 80h..FFh a
// group (poster). A server answers with the client's node-ID as
// destination. Every client can address every server ("USDO mesh",
// Kvaser; HMS whitepaper). How a server learns its groups is not public
// ([WithGroups]).
//
// # Message layouts
//
// Byte 1 is the command specifier, byte 2 the session-ID, a transaction
// number with which a client tells its transfers apart (CiA "USDO
// protocol" page; poster). The command specifiers are those of the HMS/
// Ixxat "Basics of the CANopen and CANopen FD protocol" whitepaper
// (local 01h..7Fh, remote 81h..FFh = local | 80h; see the Cmd constants).
// Layouts, bytes, with the confidence of each:
//
//	upload request 11h, download expedited response 21h:
//	  dest, cmd, session, sub-index, index (LE)                  established (MicroControl trace; HMS figure)
//	download expedited request 01h, upload expedited response 31h:
//	  … index, data type, size, data (8..63)                     established (poster; HMS figure)
//	abort 7Fh: … index, abort code (1 byte)                      established (MicroControl trace "01 7F 01 01 00 10 12")
//	bulk initiate request 06h: … index, data type, reserved,
//	  size (8..11, LE)                                           established (poster; HMS figure)
//	bulk initiate response 26h: … index, t_proc (6..7)           established, unit unknown ([WithTProcUnit])
//	bulk segment 07h: dest, cmd, session, counter, data (4..63)  established (poster)
//	bulk final 08h: dest, cmd, session, size of the last
//	  segment, data (4..)                                        established (poster)
//	bulk final response 28h: dest, cmd, session                  established (poster)
//	segmented download/upload 02h/22h, 03h/23h, 04h/24h,
//	  upload 32h, 13h, 33h, 34h                                  INFERRED, see below
//
// The data type byte carries the low byte of the CANopen data type index
// (0: not given, an interpretation); the poster says size and data type
// let the receiver check consistency, so a server refuses a download
// whose non-zero data type differs from the object's with [AbortLength].
// Multi-byte fields are little-endian as in all of CANopen (the trace
// shows the index so). Frames are padded with zeros to the next valid CAN
// FD length; receivers use the size fields.
//
// # Inferred: segmented transfers and counters
//
// The public sources list the segmented command specifiers but show only
// the bulk layouts. This package uses, by analogy: the segmented initiate
// request 02h and upload initiate response 32h laid out as the bulk
// initiate request (size in bytes 8..11, no data); the initiate response
// 22h as 21h; segments 03h and 33h as 07h, last segments 04h and 34h as
// 08h; the end response 24h as 28h; the segment confirmation 23h and the
// upload segment request 13h as dest, cmd, session, counter (the segment
// confirmed or requested). Counters number the segments from 1, modulo
// 256 (the sources only say there is a counter, with abort 14h "invalid
// value in counter byte"). A repeated segment or segment request (the
// previous counter) is answered again rather than aborted. A client
// uploads segment by segment: request 13h with counter n, answer 33h with
// counter n, or 34h for the last. These are interpretations: they make
// this client and server interoperate, not necessarily other stacks.
//
// # Abort codes (established)
//
// USDO abort codes are one byte (poster table "USDO abort codes";
// MicroControl trace with 12h, timeout) — the HMS whitepaper's "4-byte
// abort code" contradicts both. [FromSDO] and [AbortCode.SDO] map them to
// and from the classical codes with the same description; an
// [AbortError] unwraps to both.
//
// # Not implemented
//
// Remote (routed, 81h..FEh) services: their layout (network-IDs) is not
// public; a server ignores them ([ServerStats.Remote]). Transfers of
// several objects in one service (announced for a later CiA 1301
// edition). Segmented transfers to several servers: [Client.DownloadMulti]
// uses bulk transfer above [MaxExpedited] bytes, as CiA's example does.
// The client's timeout default (1 s) is this library's; MicroControl's
// stack uses 50 ms.
//
// # Services
//
// [Server] and [Client] follow the conventions of the other services:
// constructor with options ([WithLogger] ...), Start (subscribes before
// it returns), Close, Stats. A server runs one goroutine and any number of
// sessions (up to [WithMaxSessions]); a client runs one dispatcher
// goroutine and transfers on the callers' goroutines.
package usdo
