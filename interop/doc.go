// SPDX-License-Identifier: MIT

// Package interop tests go-canopen against the independent CANopen stacks
// of the otfabric/canopen-interop images (CANopenNode, Lely CANopen, python
// canopen), through their container contract only:
//
//   - go-canopen as master and SDO client (master, sdo, pdo, emcy) against
//     each stack's device built from the shared interop-device.eds;
//   - each stack's SDO client, NMT master, consumers, PDO tools, SYNC
//     producer and CiA 402 client against a go-canopen node (node, with the
//     dictionary of the same EDS) running in the test process;
//   - each stack's EDS checker against eds.ParseFile and File.Validate;
//   - contract 1.1 (canopen-interop v0.2.0): LSS (go-canopen's LSS master
//     against the stacks' LSS slaves, the stacks' LSS masters against
//     go-canopen LSS nodes), MPDO (DAM and SAM, both ways, with lely),
//     the CiA 302 boot-up (go-canopen's manager booting the stacks'
//     devices, also with its LSS step; lely's boot master booting a
//     go-canopen node), concise DCFs (lely's dcfgen and dictionary dump
//     against package cdcf, byte for byte) and the SYNC window (python
//     canopen's RPDO sender against a go-canopen node, go-canopen's
//     master against the stacks' devices). CANopen FD is recorded as a
//     skip: no image implements it.
//
// The tests carry the interop build tag and need Docker, Linux and a vcan
// interface (CANOPEN_TEST_IFACE, default vcan0):
//
//	make interop
//
// Every scenario ends as pass, fail, skip (a capability the image lacks)
// or known-difference (a documented difference, see knownDifferences);
// CANOPEN_INTEROP_REPORT writes the matrix as JSON. A skip is never a pass,
// and CANOPEN_REQUIRE_INTEROP=1 turns a missing Docker, image or interface
// into a failure.
package interop
