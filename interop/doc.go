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
//   - each stack's EDS checker against eds.ParseFile and File.Validate.
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
