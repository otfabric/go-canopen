// SPDX-License-Identifier: MIT

// Package pdo implements CANopen process data objects (CiA 301 7.2.2,
// 7.5.2.35..38): mapping entries and the bit-exact packing of mapped
// values, the transmit and receive PDOs of a device, and a master's tools
// to configure a node's PDOs over SDO and to decode and produce PDOs.
//
// # Mapping
//
// A mapping entry ([Entry]) is the UNSIGNED32 index<<16 | sub-index<<8 |
// length in bits. Mapped values are packed little-endian, least
// significant bit first, one after the other with no padding ([Pack],
// [Unpack]); lengths need not be multiples of 8 (a BOOLEAN takes one
// bit). A classical PDO carries at most 64 bits; a CANopen FD PDO up to
// 512 (below). [ValidateEntry] decides what can be mapped; in short: an
// existing, PDO-mappable object, readable for a TPDO and writable for an
// RPDO, mapped with its data type's length (a BOOLEAN with 1 or 8 bits),
// or in an RPDO a dummy entry (data types 0x0001..0x0007).
//
// # Device side
//
// [TPDO] and [RPDO] bind to a dictionary built with od.Standard (or any
// dictionary with the 0x1400/0x1600/0x1800/0x1A00 objects): they read
// their configuration from it, follow its changes, and install write hooks
// ([Protect]) that enforce the reconfiguration rules, mirroring
// CANopenNode v4 (CO_PDO.c):
//
//   - a PDO is active when its COB-ID is valid and something is mapped;
//     a valid COB-ID cannot be written while nothing is mapped.
//   - the mapping can only change while the PDO is not active, entries
//     only while sub 00h is 0: otherwise abort 0x06010000.
//   - entries are checked when written and again when sub 00h is set.
//   - the identifier of an active PDO cannot change (0x06090030), nor its
//     inhibit time or SYNC start value; reserved transmission types are
//     refused (0x06090030).
//
// Deviations from CANopenNode: 29-bit COB-IDs are accepted (bit 29);
// sub-bytes mappings (a BOOLEAN with 1 bit, any non-byte-aligned layout)
// are supported where CANopenNode only maps whole bytes; mapping lengths
// shorter than the data type (CANopenNode allows them) are refused; dummy
// entries are refused in TPDOs (CiA 301 defines them for RPDOs); TPDO
// types 252 and 253 are implemented; RPDOs enforce the SYNC window
// (below).
//
// A mapping sub-index 00h of FEh or FFh marks a multiplexed PDO (MPDO,
// CiA 301): [Load] reports it in [Config.MPDO] with no entries, the write
// hooks accept it while the PDO is not active, and the classical services
// treat the PDO as inactive (the mpdo package serves it).
//
// Both services transmit and receive only while enabled (the node calls
// SetEnabled on entering and leaving NMT operational) and take SYNC
// through OnSync, which the node wires to its SYNC consumer.
//
// # SYNC window
//
// The synchronous window length (0x1007, µs; 0 disables it) bounds
// synchronous PDOs after each SYNC. CiA 301's description of 0x1007 (as
// device manuals quote it, e.g. Nanotec's object 1007h page: synchronous
// TPDOs are rejected once the window expired, RPDOs until the next SYNC)
// is implemented as follows:
//
//   - TPDO (types 0..240): the data are sampled at the SYNC and sent
//     within the window, the send being bounded by it; a PDO not sent
//     before the window closes is discarded ([TPDOStats.WindowMissed]).
//     CANopenNode does the same by clearing pending synchronous TPDOs
//     when CO_SYNC_process reports CO_SYNC_PASSED_WINDOW
//     (CANopen.c: CO_CANclearPendingSyncPDOs).
//   - RPDO (types 0..240): a PDO received after the window of the last
//     SYNC closed is discarded ([RPDOStats.OutsideWindow]) until the next
//     SYNC; one received within it is applied on the next SYNC as usual.
//     CANopenNode does not check the window for RPDOs.
//
// OnSync reads 0x1007 from the dictionary and measures the window from the
// call; OnSyncWindow takes the window end from the caller (the
// syncobj.Event's WindowEnd, measured from the frame's receive time).
// [WithClock] replaces the clock for tests.
//
// # CANopen FD
//
// With [WithFD] a PDO is a CANopen FD PDO (CiA 1301): sent in a CAN FD
// frame (BRS selectable) with up to 64 data bytes, its mapping limited to
// [MaxBitsFD] (512) bits instead of [MaxBits] — still at most 64 entries
// (sub-indices 01h..40h) — and validated so by the write hooks
// ([ProtectFD]). Public sources (CiA's CANopen FD pages and poster, the
// HMS/Ixxat CANopen FD whitepaper, emotas, Kvaser, MicroControl) establish
// that CANopen FD PDOs carry 1 to 64 bytes in CAN FD data frames, that
// the PDO communication and mapping objects keep their indices and
// sub-indices (01h COB-ID, 02h transmission type, 03h inhibit time, 05h
// event timer, 06h SYNC start value), and that CANopen FD has no remote
// frames. They describe no per-PDO FD or BRS flag (CiA states that
// CANopen FD uses the CAN FD frame format for all its protocols, without
// distinguishing bit rate switching), so the choice is an option.
// Consequences, which are interpretations of this library:
//
//   - CAN FD lengths above 8 come in steps (12, 16, 20, 24, 32, 48, 64):
//     a TPDO is padded with zeros to the next one, and an RPDO accepts a
//     frame up to that length without reporting "length exceeded"
//     (0x8220).
//   - no remote transmission requests: an FD TPDO does not subscribe to
//     them; types 252/253 sample but never transmit; [Listener.Request]
//     fails with [ErrNoRTR]. COB-ID bit 30 is kept but has no effect.
//
// Classical PDOs (without the option) behave exactly as before.
//
// # Master side
//
// [Configure] writes a node's PDO configuration over an [SDO] in the CiA
// 301 order and [ReadConfig] reads it back. A [Decoder] (with a
// [Listener]) turns a node's PDOs into named values, using a dictionary
// that describes the node (for example built from its EDS); an [Encoder]
// (with a [Sender]) produces PDOs for it.
package pdo
