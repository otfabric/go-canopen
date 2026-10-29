// SPDX-License-Identifier: MIT

// Package canopen is the shared core of go-canopen, a CANopen (CiA 301)
// implementation on top of go-canbus: node identifiers, COB-IDs and the
// predefined connection set, the CANopen data types and their encoding, SDO
// abort codes and the NMT states and commands.
//
// The services live in subpackages and all run on a [canbus.Bus], so they
// work on every go-canbus transport (Linux SocketCAN, slcan adapters, the
// virtual bus):
//
//   - od: the object dictionary, with access rules, limits and hooks
//   - sdo: SDO client and server (expedited, segmented and block transfer)
//   - nmt: NMT master commands, the NMT slave state machine, heartbeat
//     producer and consumer, node guarding
//   - pdo: PDO mapping, transmit and receive PDOs
//   - sync, emcy, timestamp: the SYNC, EMCY and TIME objects
//   - node: a complete CANopen device built from an object dictionary
//   - master: a network manager (NMT, heartbeat supervision, SDO clients,
//     network scan, configuration from a DCF, remote PDOs)
//   - eds: EDS and DCF (CiA 306) reader, validator and writer
//   - codegen: typed Go code from an EDS
//   - cia402: the CiA 402 drive profile (state machine, modes of operation)
//   - dissect: decoding of CAN frames as CANopen messages
//
// [canbus.Bus]: https://pkg.go.dev/github.com/otfabric/go-canbus#Bus
package canopen
