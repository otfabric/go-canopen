// SPDX-License-Identifier: MIT

// Package e2e holds the end-to-end tests of go-canopen: a master and
// several nodes (I/O modules and a CiA 402 drive) running the whole stack
// (NMT, boot procedure, SDO, PDO, SYNC, heartbeat, EMCY, resets) over the
// virtual bus, and on Linux also over SocketCAN (vcan0, or the interface
// CANOPEN_TEST_IFACE names). Without a usable interface the SocketCAN
// tests skip, unless CANOPEN_REQUIRE_VCAN=1 makes that a failure.
package e2e
