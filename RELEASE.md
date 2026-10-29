# go-canopen Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## vX.Y.Z` and closed by a line `---`, before running it.

## v0.3.1

**Date:** 2026-10-30
**Previous release:** v0.3.0

Fix of the CiA 302 network boot-up in `master`.

- A boot slave process that was running when a network boot-up started
  (it began at a slave's boot-up message that arrived just before) went
  on through the network boot-up's reset communication. The reset undid
  the configuration it had written (for example the heartbeat producer
  time from a concise DCF), and the process then failed with status K
  ("no heartbeat received when starting the error control"), which
  halted the network boot-up for a mandatory slave. The network boot-up
  now cancels such processes and waits for them before its reset; the
  slave boots again at its boot-up message after the reset. Found by CI
  (Windows) after v0.3.0; regression test
  `TestBootNetworkCancelsRunningBoot`.
- A software update in progress (stop, clear, download, start) is not
  interrupted by that cancellation: stopped after the download, the slave
  kept the new program stopped, and the next boot slave process found it
  up to date. Only the master's Close stops an update. Regression test
  `TestBootNetworkCompletesSoftwareUpdate`.
- Tests: the store tests give the SDO client 2 s (a file store fsyncs
  at every store and restore, longer than 150 ms on a loaded CI runner).
- Test: `TestManagerKeepAlive` lets the nodes' start-up boot-up messages
  pass before the manager starts, so that its first boot events are the
  network boot-up's.

Verification: `go test -race ./...`; `go test -race -count=5 -cpu 1,2,4
./master ./e2e`; coverage 96.9 % total (gate 90 %); golangci-lint clean.

---

## v0.3.0

**Date:** 2026-10-29
**Previous release:** v0.2.0

## Summary

go-canopen grows from CiA 301 into the rest of a CANopen network: LSS
(CiA 305) to give unconfigured devices their node-IDs, multiplexed PDOs,
the synchronous window, store and restore of parameters, the CiA 302
manager (network boot-up, configuration from concise DCFs, program
download) and CANopen FD (CiA 1301: the USDO, 64-byte PDOs and 20-byte
emergencies). The device (`node`) and the manager (`master`) integrate
every one of them. The new features are implemented from public sources
and tested against go-canopen itself (unit, fuzz, chaos, end-to-end on
the virtual bus and Linux vcan) and, through canopen-interop v0.2.0
(contract 1.1), against CANopenNode, Lely CANopen and python canopen:
LSS, MPDOs, the CiA 302 boot-up with concise DCFs and the SYNC window.
CANopen FD has no independent oracle yet. The public API of v0.2.0 is
unchanged.

## New packages

- **`lss`**: LSS master (switch state global and selective, configure
  node-ID and bit timing, activate bit timing, store, inquire identity and
  node-ID, identify remote and non-configured slaves, Fastscan of one or
  all unconfigured devices, `AssignAll` with safeguards against duplicate
  node-IDs) and LSS slave; interpretations where CANopenNode, Lely and
  python canopen differ are documented.
- **`mpdo`**: MPDO producer (DAM `SendDAM`, SAM `Send` checked against the
  object scanner lists) and consumer (DAM writes, SAM through the object
  dispatcher lists, EMCY 0x8230), list objects, validation hooks and the
  MPDO reconfiguration rules, after Lely CANopen.
- **`usdo`**: USDO client and server of CANopen FD: unicast, group and
  broadcast addressing, several sessions, expedited, segmented and bulk
  transfers, one-byte abort codes mapped to and from the SDO codes.
  Segmented layouts are inferred (the public sources show bulk and
  expedited only); remote (routed) services are not supported.
- **`store`**: a `Store` (memory, file holding a concise DCF, replaced
  atomically), parameter snapshots per group and the objects 0x1010,
  0x1011, 0x1020.
- **`cdcf`**: the concise DCF of CiA 302-3: encode, decode, from an EDS/DCF,
  PDO sequencing.
- **`program`**: the program download objects of CiA 302-3 on the device
  side (0x1F50, 0x1F51, 0x1F56, 0x1F57) with a pluggable flash handler.

## Changes in existing packages

- **`pdo`**: CANopen FD PDOs (`WithFD`, `PackFD`, `UnpackFD`, up to 512
  bits, no RTR, `ProtectFD`); the synchronous window 0x1007 enforced
  (`OnSyncWindow`, `TPDOStats.WindowMissed`, `RPDOStats.OutsideWindow`);
  MPDO mappings (0xFE, 0xFF) accepted and treated as inactive.
- **`sync`**, **`emcy`**: `WithFD` (SYNC in CAN FD frames; the 20-byte
  CANopen FD emergency with logical device, CiA specification, status and
  time, `WithFDLayout` because the public sources disagree on its layout).
- **`nmt`**: `WithFD` (commands, boot-up and heartbeats in CAN FD frames,
  CAN FD frames accepted), `ParseCommandFD`, `ParseErrorControlFD`,
  `ToFD`; a slave can start with node-ID `Unconfigured` (0xFF): no boot-up
  until a reset applies a pending node-ID. Fixed: a data race between
  concurrent subscriptions (a shared `canbus.WithFilters` option, which
  writes to its captured filter set).
- **`node`**: `WithLSS` (LSS slave wired to NMT: pending node-ID, reset
  communication of an unconfigured device, node-ID persisted with
  `WithStore` under the pseudo index 0x0000 and applied at `New`,
  `StoredLSS`; bit timing handlers for the application; dictionary
  node-ID 0xFF starts the node unconfigured and silent), MPDO producers and
  consumers for every PDO configured as an MPDO (also when a mapping
  becomes one later; `MPDOProducer`, `MPDOConsumer`, gated by NMT,
  emergencies), the SYNC window from each received SYNC, `WithFD` (every
  service in CAN FD frames, a USDO server instead of the SDO server,
  `USDOServer`, `WithUSDOServerOptions`, `WithEMCYOptions`), `WithStore`
  (`SaveParameters`, `RestoreDefaults`), `WithProgram`; `Stats` gains
  `mpdoProducer`, `mpdoConsumer`, `lss`, `usdo`.
- **`master`**: CiA 302 manager with `WithDictionary`: objects
  0x1F80..0x1F89, network boot-up and boot slave process (error status
  letters A..O, `BootError`), configuration date check, restore, concise
  DCF (`ConfigureConcise`), store after configuration, program download
  (`DownloadProgram`), `StoreParameters`, `RestoreDefaults`. New: `LSS()`
  (an LSS master started and closed with the master, `WithLSSOptions`),
  `WithLSSBoot` (LSS step of the network boot-up: Fastscan, node-ID by
  expected identity, `EventLSSAssigned`), `ConfigureMPDO`,
  `WriteScannerList`, `WriteDispatcherList`, `WithFD` (every procedure
  over the master's USDO client, USDO aborts reported as SDO aborts, FD
  heartbeat and EMCY consumers, FD PDO listeners and senders),
  `WithUSDOClient`, `USDO()`, `ErrNoUSDO`; `Stats` gains `lss` and `usdo`.
- **`sdo`**: `WithResetFence` (server): the server also receives the NMT
  commands and stops answering at a reset of its node, in the order the
  frames arrived, and stays silent until closed; `WithFenced` starts a
  server that ignores every request until `Server.Fence`, which ignores
  the requests received before it (by receive timestamp). `node` uses
  them: its SDO server answers neither a
  request received after a reset command nor one received before its
  boot-up message.
- **`nmt`**: `WithBootUpHandler` (slave): runs just before every boot-up
  message.
- **`dissect`**: USDO requests and responses and 20-byte FD emergencies in
  CAN FD frames; LSS command names from `lss.Command.String` (cosmetic:
  "product code" instead of "product-code").

## Verification

- 402 tests, 25 fuzz targets (12 new: CANopen FD emergency and SYNC,
  MPDO messages, entries and consumer, LSS slave and responses, USDO
  messages, server and client, concise DCF, dissector in CAN FD),
  13 examples (3 new: lss-assign, canopen-fd, network-boot), race
  detector on every package, goroutine leak checks;
  `go test -race -count=10 ./node ./master ./e2e` without failure.
- End-to-end scenarios on the virtual bus and on Linux vcan (amd64,
  arm64; CAN FD with MTU 72): LSS Fastscan and AssignAll on three
  unconfigured nodes followed by the CiA 302 network boot-up from concise
  DCFs; MPDO DAM and SAM between nodes; a CANopen FD network (master and
  nodes with USDO, 64-byte PDOs, FD emergencies); the SYNC window; store
  and restore across a node restart; program download.
- Chaos: USDO through a CAN FD chaos bridge; LSS Fastscan and AssignAll;
  `TestMonkeyLSSBoot` (the network boot-up of unconfigured devices with
  the LSS step through random frame loss, duplication and delay).
- Coverage 96.9 % total (11,283 statements, macOS), gate 90 %; every
  package at least 90 % (lowest: `node` 92.6 %, `master` 94.4 %).
- Interoperability: the suite runs canopen-interop v0.2.0 (contract 1.1,
  images pinned by digest): 1,620 scenarios, 0 failures locally (Lima
  VM, arm64, Linux 7.0). New: LSS (go-canopen's master against
  CANopenNode's and Lely's slaves: identification, selection, inquiries,
  bit timing, node-ID, store, Fastscan, `AssignAll`; the three stacks'
  masters against go-canopen LSS nodes, CANopenNode's `assign-all`
  included), MPDO DAM and SAM with Lely in both directions
  (`ConfigureMPDO`, scanner and dispatcher lists), the CiA 302 boot-up
  (the manager boots each stack's device from a concise DCF, reports
  error status M for a wrong product code and assigns node-IDs with the
  LSS step; Lely's boot master boots a go-canopen node), concise DCFs
  byte-exact against Lely's dcfgen and `co_dev_write_dcf`, and the SYNC
  window (python canopen's `rpdo-send` against a node: late RPDOs
  discarded; go-canopen's master against Lely's and CANopenNode's
  devices). New known differences: CANopenNode's LSS slave lacks the
  identify services; Lely's applies a node-ID only at reset
  communication and answers Fastscan when configured. CANopen FD is a
  recorded skip: no reference stack implements it.
- Fixed, found by the interop suite (each with a regression test):
  `lss.Master.AssignAll` failed with `ErrNoNodeID` when the last device
  took node-ID 127, and gave every node-ID to the first of several Lely
  devices (they answer Fastscan with a pending node-ID; the remaining
  devices are now listed and selected one by one); a node answered an
  SDO request that arrived right after an NMT reset command from its
  state before the reset and lost the next request (Lely's boot master
  then reported error status M); the SYNC window ran on the wall clock
  and broke when the clock was stepped (it now uses the monotonic
  clock).
- Fixed, found by stress runs (race detector, `-cpu 1,2,4`, chaos), each
  with a regression test: `lss.Master.Configure` (and so
  `ConfigureNodeID` and the LSS step of the boot-up) could configure a
  second slave left in the configuration state by a lost "switch state
  global", giving it the node-ID of another device; it now checks with
  "inquire node-ID" that no slave stayed selected and, after the
  selection, that the selected slave answers the identity inquiries
  alone (`ErrConflict` otherwise). The heartbeat consumer timed events by
  when it processed a frame, not when the frame arrived, so a late
  boot-up started a second boot slave process; events now carry the
  receive timestamp. The boot slave process failed with a wrong status
  (D, M, N, O, J) when one SDO request of the identity check, the
  configuration date check, the restore, a concise DCF entry or the store
  timed out once; these requests are now repeated on timeout like the
  read of 0x1000. A `BootNetwork` call superseded by another network
  boot-up (a second call, or the automatic one at the master's boot-up,
  for example when 0x1F80 is set after Start) returned "context
  canceled"; it now returns the result of the boot-up that replaced it.

## Compatibility

No API removed or changed; additions only. Behaviour changes: a node
enforces the synchronous window when 0x1007 is set (it was only
reported); a node's SDO server answers no request received after an NMT
reset command addressed to it (until the server of the reset answers);
`syncobj.Event.Time` carries a monotonic clock reading;
`nmt.HeartbeatEvent.Time` is the frame's receive timestamp;
`lss.Master.Configure` checks the selection (a few Fastscan windows
longer); `nmt.NewSlave` and `Slave.SetNodeID` accept `Unconfigured`;
`dissect` LSS names use spaces. Go 1.23 or later; go-canbus v0.7.0.

## Known limitations

No flying master or SDO manager (CiA 302-5), no 0x1F20 DCF storage, no
0x1F52..0x1F54; USDO segmented layouts inferred, remote USDO unsupported;
the CANopen FD emergency layout is selectable because the public sources
conflict; MPDOs and TIME stay classical on CANopen FD; nothing tested on
real hardware; no CiA conformance claim. See LIMITATIONS.md and
ROADMAP.md.

---

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
- Result, identical locally (Lima VM, arm64, Linux 7.0) and in CI (amd64
  and arm64 runners): 0 failures; per adapter
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
