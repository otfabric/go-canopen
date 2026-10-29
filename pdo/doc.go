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
// bit). A PDO carries at most 64 bits (classical CAN; CAN FD PDOs are not
// supported). [ValidateEntry] decides what can be mapped; in short: an
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
// types 252 and 253 are implemented. The SYNC window (0x1007) is not
// enforced.
//
// Both services transmit and receive only while enabled (the node calls
// SetEnabled on entering and leaving NMT operational) and take SYNC
// through OnSync, which the node wires to its SYNC consumer.
//
// # Master side
//
// [Configure] writes a node's PDO configuration over an [SDO] in the CiA
// 301 order and [ReadConfig] reads it back. A [Decoder] (with a
// [Listener]) turns a node's PDOs into named values, using a dictionary
// that describes the node (for example built from its EDS); an [Encoder]
// (with a [Sender]) produces PDOs for it.
package pdo
