# go-canopen Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

## v0.1.0

**Date:** 2026-10-29
**Previous release:** none

## Summary

The first release of go-canopen: a pure Go implementation of CANopen
(CiA 301) on top of go-canbus v0.7.0, so it runs on Linux SocketCAN,
slcan adapters and go-canbus's virtual bus. It contains the CiA 301
services, a complete device and a network manager, EDS/DCF support, a
code generator, the CiA 402 drive profile and a dissector. Coverage
97.4 % (all library packages together, macOS; every package
above 90 %). Checked against Wireshark's CANopen dissector; no results
against other CANopen stacks yet (the canopen-interop suite is planned),
and nothing has been tested on real CAN hardware.

## Packages

- **`canopen`**: node-IDs, COB-IDs and the predefined connection set
  (`Classify`), the CiA 301 data types with encoding and EDS literal
  parsing, SDO abort codes (`AbortCode`, `*AbortError`), NMT states and
  commands, the `Bus` interface, sentinel errors.
- **`od`**: the object dictionary: VAR, ARRAY, RECORD and DOMAIN
  objects, access rights, limits, node-ID-relative defaults, remote and
  local access, read and write hooks and watchers (each removable;
  `HookCount` for diagnostics), resets, `od.Standard` for the
  communication profile objects.
- **`sdo`**: client and server with expedited, segmented and block
  transfer (CRC-16, block size 1..127, protocol switch threshold),
  timeouts, retries, typed helpers; documented interpretations where
  CiA 301 leaves room.
- **`nmt`**: NMT commands, the slave state machine with reset node and
  reset communication (pending node-ID), heartbeat producer (0x1017) and
  consumer (0x1016, waits for states and boot-ups, restart), node
  guarding and life guarding.
- **`pdo`**: bit-exact mapping (sub-byte entries), TPDO (synchronous
  cyclic and acyclic, RTR, event-driven with inhibit time and event
  timer, SYNC start value) and RPDO (synchronous buffering, deadline
  monitoring, length errors), the CiA 301 reconfiguration rules after
  CANopenNode v4 (`Protect`, removable), remote configuration in the CiA
  301 order, decoder, encoder, listener and sender.
- **`sync` (package `syncobj`)**, **`emcy`**, **`timestamp`**: SYNC
  producer and consumer with counter and window; emergency producer with
  error register, error history and inhibit queue, and consumer; TIME
  producer and consumer with TIME_OF_DAY and TIME_DIFFERENCE.
- **`node`**: a device built from a dictionary: services chosen from the
  objects present, enabled by NMT state (CiA 301 table 79), the CiA 301
  communication emergencies raised and cleared on their own, the 0x1029
  error behaviour, resets with node-ID change, a CiA 402 drive.
- **`master`**: NMT, heartbeat supervision, EMCY, SDO clients per node,
  scan, identity, configuration from entries or a DCF (PDOs in the CiA 301
  order; EMCY, SYNC and TIME COB-ID changes released first), boot
  procedure, remote PDOs, SYNC and TIME production, CiA 402 control, one
  ordered event stream.
- **`eds`**: CiA 306 EDS and DCF parsing (every section kept), validation
  into diagnostics, deterministic writing (round trip), conversion to and
  from a dictionary.
- **`codegen`**: typed Go from an EDS or DCF: constants, a dictionary
  constructor, local and remote accessors; deterministic, golden-tested.
- **`cia402`**: the CiA 402 state machine and modes, a simulated drive
  (PP, PV, PT, HM, CSP, CSV, CST, quick stop, halt, fault reactions) and a
  controller over SDO.
- **`dissect`**: CAN frames decoded as CANopen messages, with SDO
  transfer tracking and JSON output.

## Verification

- 234 tests, 13 fuzz targets, 7 examples, race detector on every package;
  goroutine leak checks; chaos tests of SDO, heartbeat and PDOs through
  go-canbus's fault-injecting bridge, and a chaos monkey of a master and a
  node (20 seeds per push, 500 nightly).
- End-to-end plant (master, two I/O modules, a CiA 402 drive) on the
  virtual bus and on Linux vcan (amd64, arm64).
- Wireshark's CANopen dissector (tshark 4.6) as oracle for `dissect`,
  required in CI.
- EDS: the fixtures and the sample EDS files of CANopenNode and python
  canopen; a cross-check against python canopen's EDS import that runs
  where python canopen is installed (not in CI).
- Coverage 97.4 % total, gate 90 %.

## Known limitations

No CANopen FD (USDO), LSS, CiA 302 manager features beyond the boot
procedure, MPDO, store/restore of parameters or SYNC window enforcement;
one SDO server channel; segmented SDO cannot detect corruption (as in CiA
301); no interoperability results yet; nothing tested on real hardware;
no CiA conformance claim. See LIMITATIONS.md and ROADMAP.md.

---
