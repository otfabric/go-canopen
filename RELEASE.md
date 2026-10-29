# go-canopen Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

## v0.2.0

**Date:** 2026-10-29
**Previous release:** v0.1.0

## Summary

go-canopen is now tested against three independent CANopen stacks,
CANopenNode v4, Lely CANopen 2.3.5 and python canopen 2.4.1, through the
containers of canopen-interop v0.1.0 (pinned by digest) on Linux vcan, in
both directions, on amd64 and arm64 in CI. The suite found one bug, in
SDO block transfers of empty data, fixed in this release. No API change.

## Interoperability suite

- **go-canopen as master and client** of each stack's device (the shared
  `interop-device.eds`, read with `eds.ParseFile`): SDO upload and download,
  normal and block with CRC, of every object of the EDS, the DOMAIN with
  0 to 4096 bytes, aborts (missing object and sub-index, access, length,
  limits), scan and identity, NMT commands with boot-ups and heartbeat
  states seen by the master, heartbeat loss, TPDOs decoded with
  `pdo.Listener` after `pdo.ReadConfig`, a TPDO remapped with
  `pdo.Configure`, a synchronous TPDO driven by the master's SYNC, an RPDO
  to the device, EMCY decoded by `emcy.Consumer`, `ConfigureFromDCF`; the
  values written are checked with the device's own dump.
- **Each stack as master and client** of a go-canopen `node` in the test
  process: SDO upload and download (normal and block) checked byte for
  byte in the node's dictionary, aborts, NMT commands and both resets,
  `listen` (boot-up, heartbeat, EMCY), `pdo-receive`, `pdo-configure`,
  python canopen's SYNC producer driving a synchronous TPDO, and python
  canopen's `BaseNode402` enabling, disabling and fault-resetting a
  go-canopen `cia402.Drive`.
- **EDS**: Lely's dcf-tools and python canopen's importer against
  `eds.Validate` and the dictionary on 12 EDS/DCF files: the verdict and
  the normalized object list.
- Every scenario is pass, fail, skip (a capability the image lacks, with
  the reason) or a documented known difference (with the reason); a skip
  or a known difference never counts as a pass. `make interop` writes
  `bin/interop-report.json`; the Interop workflow uploads it.
- Local result (Lima VM, arm64, Linux 7.0): 0 failures; per adapter
  (pass / known / skip): CANopenNode 435 / 21 / 34, Lely 455 / 22 / 13,
  python canopen 303 / 10 / 177. The differences (empty DOMAIN and unused
  error history entries in the stacks' SDO servers, python canopen's
  LocalNode ignoring the sub-index of a VAR, CANopenNode's string buffers,
  Lely's client skipping a 0-byte block download, EDS files the Python
  checkers reject or accept unvalidated) are listed with their reasons in
  INTEROPERABILITY.md.

## Fixes

- **sdo**: an SDO block transfer of empty data may end without a segment:
  Lely CANopen's server sends the end message right after the start of a
  block upload of an empty object, and python canopen's client does the
  same in a block download of 0 bytes. go-canopen's client and server took
  that message for an out-of-sequence segment and waited for the timeout
  (the client then repeated its start, which Lely aborted with 0x05040001).
  Both now accept the end of a transfer before any segment (a message no
  segment can look like: the first one has sequence number 1); a transfer
  ending this way with a size indicated other than 0 is aborted with
  0x06070013.

## Compatibility

No API change. Go 1.23 or later; go-canbus v0.7.0.

---

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
