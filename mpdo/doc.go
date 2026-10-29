// SPDX-License-Identifier: MIT

// Package mpdo implements CANopen multiplexed PDOs (MPDOs, CiA 301
// 7.2.2.4 and the object scanner and dispatcher lists 0x1FA0..0x1FFF):
// PDOs that carry one value of up to 4 bytes together with the object it
// belongs to, so that one COB-ID can transport many objects.
//
// # Frame
//
// An MPDO is always 8 bytes ([Message]):
//
//	byte 0     bit 7: address mode (0 SAM, 1 DAM); bits 0..6: node-ID
//	bytes 1..2 object index (little-endian)
//	byte 3     object sub-index
//	bytes 4..7 the value, padded with zeros
//
// In destination address mode (DAM) the node-ID is the consumer's (0: all
// consumers) and the object is the consumer's object the value is written
// to. In source address mode (SAM) the node-ID is the producer's and the
// object is the producer's own; each consumer finds the local object in
// its object dispatcher list.
//
// # Configuration
//
// A PDO is an MPDO when sub-index 00h of its mapping parameter is 0xFE
// ([MappingSAM]) or 0xFF ([MappingDAM]), with an event-driven transmission
// type (254, 255). The COB-ID, transmission type and inhibit time are the
// usual communication parameters (0x1400.., 0x1800..). As in Lely CANopen
// (lely-core src/co/rpdo.c, pdo.c co_mpdo_dn), the value has the same
// meaning on the receive side: an RPDO with 0xFE consumes SAM-MPDOs, one
// with 0xFF DAM-MPDOs, and an MPDO of the other mode is ignored.
//
// A SAM producer may only send objects listed in its object scanner list
// (0x1FA0..0x1FCF, [ScannerEntry]); a SAM consumer writes only values that
// match an entry of its object dispatcher list (0x1FD0..0x1FFF,
// [DispatcherEntry]). Both entries describe a block of consecutive
// sub-indices: block size n covers sub-index s..s+n-1 (0 counts as 1,
// as Lely does), and a dispatcher entry maps the k-th sub-index of the
// producer's block to the k-th of the local block.
//
// # Services
//
// [Producer] (a TPDO whose mapping sub 00h is 0xFE or 0xFF) sends DAM-MPDOs
// ([Producer.SendDAM]) or SAM-MPDOs ([Producer.Send], checked against the
// scanner list; the value is read with a remote read so read hooks
// apply). [Consumer] (an RPDO whose mapping sub 00h is 0xFE or 0xFF)
// writes received values with remote writes ([od.Dictionary.Write]), so
// access rights, limits, write hooks and watchers apply; the local object
// must exist, be PDO-mappable and writable. The value written has the
// object's data type size when that is at most 4 bytes, otherwise 4 bytes
// (Lely co_mpdo_dn): a larger fixed-size object is refused by the
// dictionary.
//
// Both follow their parameters in the dictionary, and send or receive only
// while enabled ([Producer.SetEnabled], NMT operational) and active
// (COB-ID valid, MPDO mapping, event-driven type).
//
// # Errors and emergencies
//
// The consumer reports two emergencies through [WithErrorHandler] as
// pdo.ErrorEvent values (so a node can feed them to the same handler as
// its RPDOs):
//
//   - 0x8230 ([EMCYDAMNoObject], CiA 301 table 21 "DAM MPDO not
//     processed, destination object not available"; Wireshark's
//     packet-canopen.c lists the same text): a DAM-MPDO addressed to this
//     node (or to all) names an object or sub-index the dictionary lacks.
//     Lely raises it in the same case (co_rpdo_read_frame).
//   - 0x8210 (pdo.EMCYPDOLength): an MPDO shorter than 8 bytes, as Lely
//     reports a PDO length error for it.
//
// Each is reported once when it appears and once when it is resolved:
// 0x8230 by the next DAM-MPDO for this node that is written, 0x8210 by the
// next MPDO of 8 bytes. (The resolution rule is this package's choice;
// Lely reports every occurrence.) Other refusals — an object that is not
// mappable or not writable, a write the dictionary refuses, a SAM-MPDO
// without a dispatcher entry, a DAM-MPDO for another node — are ignored
// and counted in [ConsumerStats], as in Lely. Frames longer than 8 bytes
// (CAN FD) are used up to their 8th byte, as Lely does.
//
// # Reconfiguration rules
//
// [NewProducer] and [NewConsumer] install write hooks on their PDO's
// mapping and communication parameters (see [ProtectPDO]); [ProtectLists]
// installs the hooks that validate scanner and dispatcher entries. Both
// complement pdo.Protect, which a node installs on every PDO.
//
// # Sources
//
// Frame layout, SAM/DAM semantics, the 0xFE/0xFF mapping values, the
// scanner entry layout (bits 0..7 sub-index, 8..23 index, 24..31 block
// size), the block matching and dispatch arithmetic, the value size rule
// and the emergencies follow Lely CANopen (lely-core include/lely/co/pdo.h,
// src/co/pdo.c co_dev_chk_sam_mpdo, co_dev_map_sam_mpdo, co_sam_mpdo_up,
// co_mpdo_dn; src/co/tpdo.c co_dam_mpdo_event, co_sam_mpdo_event;
// src/co/rpdo.c). The dispatcher entry layout (bits 0..7 producer node-ID,
// 8..15 producer sub-index, 16..31 producer index, 32..39 local
// sub-index, 40..55 local index, 56..63 block size) is the one documented
// by embedded-communication.com ("CANopen MPDO - An introduction") and
// used by Lely for every field but the block size, which Lely reads at
// bit 54 (apparently a slip: bits 54..55 belong to the local index); this
// package uses bits 56..63. The object ranges 0x1FA0..0x1FCF and
// 0x1FD0..0x1FFF are those of CiA 301 (the CiA CANopen poster, CANopenNode
// doc/objectDictionary.md, Wireshark). "Event-driven only" is CiA 301 as
// quoted by embedded-communication.com; Lely likewise sends nothing for a
// synchronous MPDO.
package mpdo
