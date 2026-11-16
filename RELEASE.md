# go-canopen Releases

Releases are made with the Release workflow (Actions → Release → bump):
it computes the version, tags it and publishes this file's section of
that version as the release notes. Add the section, headed exactly
`## v0.6.3

**Date:** 2026-11-15
**Previous release:** v0.6.2

### Changed

- Requires go-canbus v0.11.0 (was v0.10.1). go-canopen does not use the kernel ISO-TP connection, so its behaviour is unchanged.

---

## vX.Y.Z` and closed by a line `---`, before running it.

## v0.6.4

**Date:** 2026-11-16
**Previous release:** v0.6.3

No library code changes.

### Changed

- Built on go-canbus v0.11.1 (was v0.11.0) (go-canbus RELEASE.md, v0.11.1).

---

## v0.6.2

**Date:** 2026-11-15
**Previous release:** v0.6.1

No library code changes.

### Changed

- Built on go-canbus v0.10.1 (was v0.10.0) and go-serial v0.4.1 (was v0.4.0) (go-canbus RELEASE.md, v0.10.1; go-serial RELEASE.md, v0.4.1). A `Send` on a bus that waits for another `Send` follows its context and `Close`; metrics callbacks of the bus that do not return normally are logged and the bus goes on; message databases and captures are read within size bounds.

---

## v0.6.1

**Date:** 2026-11-14
**Previous release:** v0.6.0

### Summary

Fixes from the policy 1.6 review of the library, on top of the bounds of
v0.6.0: further input bounds for transfers, files and values, NMT state
and service handling in `node`, the Close contract of every service, and
the callbacks the library calls. No exported identifier is added, removed
or changed. Some calls now return an error, or another error, where they
did something else before: see "Changed".

### Changed

- `usdo.Client`: a transfer on a closed client fails with
  `canopen.ErrClosed`, not `ErrNotStarted` (which remains for a client
  that was never started).
- `master.BootNetwork`, `BootSlave` and `Scan` in progress at `Close`
  return an error that matches `canopen.ErrClosed`; it still matches
  `context.Canceled`, which `BootNetwork` returned before.
- `master.BootSlave` can return an error that matches `context.Canceled`
  without its own context having ended (a network boot-up started).
- `lss.Master`: a service called while `Configure`, `ConfigureNodeID`,
  `AssignAll` or `InquireAddress` runs on another goroutine waits for it,
  or for its own context. The services a `Configure` function calls
  belong to it when they run on its goroutine or with its context.
- A callback that does not return normally no longer ends the process.
- `nmt.Slave.Start` and `Node.Start` return `ErrClosed` when a handler
  they called closed the slave or node.
- The inputs listed under "Input bounds" below are refused.

### Fixed

Input bounds:

- `sdo` client: more than four responses in a row that move a transfer no
  further (upload segments without data that are not the last, block
  acknowledgements of no segment) end it with `canopen.ErrProtocol`.
- `usdo` client: a bulk download waits at most the response timeout
  between two segments, whatever processing time (t_proc) a server
  reports (`DownloadMulti` included).
- `od`: `Dictionary.Add` refuses a NaN limit of a float object;
  `canopen.Encode` and `ParseValue` refuse a finite value beyond the
  range of REAL32; `eds` reports a NaN limit as an error (the build
  skips the sub-object with a warning in lenient mode).
- `sdo` server: the buffer of a download is sized from the indicated
  size alone, whatever `WithMaxSize` allows, on 32-bit platforms too.
- `eds`: `Parse`, `ParseFile` and `ParseBytes` refuse a file larger than
  16 MiB, and a dictionary is built with at most 65536 sub-objects,
  compact storage counted expanded (`BuildDictionary`, `cdcf.Entries`,
  `cdcf.FromFile`, `codegen.Generate`), in lenient mode too. Both bounds
  are fixed; `BuildOptions.MaxCompactBytes` is unchanged.
- `master` request objects 0x1F82 and 0x1F83 and the `program` control
  0x1F51: a dictionary that declares them with a type without fixed size
  gets `AbortLengthLow` or `AbortLengthHigh` for a value that is not one
  byte.
- `master.Overlay` refuses a base mapping of more than 64 entries
  (`AbortPDOLength`); `cia402.Drive.Run` refuses a period that is not
  positive, `Drive.Step` ignores a negative time step, and the drive's
  outputs are 0 for a value that is not a number; `codegen` writes
  `Options.SourceSHA256` into the header comment as it writes `Source`
  (control characters become spaces, Unicode format characters are
  removed).

NMT state of a node:

- `node`: the SDO or USDO server and the PDOs are enabled for the NMT
  state the node is in. When two goroutines changed the state (an NMT
  command and the error behaviour of 0x1029, or `SetState`), the services
  followed whichever state handler ran last, so a node could be Stopped
  and still answer SDO, or Operational with its PDOs off.
- `node`: the error behaviour of 0x1029 changes the state from
  Pre-operational or Operational only. A node that was Initialising
  (waiting for a node-ID after LSS removed it, or resetting) went to
  Stopped when a supervised node was lost, and its heartbeat producer
  then sent on its former node-ID.
- `node` with `WithFD`: the USDO server answers nothing before the node's
  boot-up message, as the SDO server of a classical node.

LSS:

- `lss.Master`: `Configure`, `ConfigureNodeID`, `AssignAll` and
  `InquireAddress` are one sequence each. A service called from another
  goroutine could run between their steps, switch the selection, and a
  node-ID then went to another device than the one `Configure` named,
  with no error.

Close:

- Every `Close` returns only when the service's goroutine has ended, on
  every call: a second `Close`, concurrent or later, used to return at
  once while the first was still waiting for a handler or hook (`node`,
  `master`, `sdo.Server`, `usdo`, `lss.Slave`, `emcy`, `sync`,
  `timestamp`, `nmt`, `pdo`, `mpdo`).
- A handler, or a dictionary hook run by a service, may call the `Close`
  of that service, of its node or of the master: `Close` returns, and
  the goroutine ends when the callback has returned. It used to wait for
  itself. A `Close` on another goroutine still waits for the callback.
- After `Close` the services send nothing and their operations fail with
  `canopen.ErrClosed`: `Node.SetState`, `Node.Reset` and `nmt.Slave` (a
  reset in progress sends no boot-up message any more); `pdo.TPDO`
  (`Transmit`, SYNC, events) and `RPDO` (a buffered PDO is dropped), also
  when `SetEnabled` is called afterwards; `emcy.Producer` (`Raise`,
  `Clear`, `ClearAll`, `Send`, `SendFD`: nothing is queued or recorded);
  `syncobj.Producer.Send` and `timestamp.Producer.Send`;
  `mpdo.Producer` (an MPDO waiting for its inhibit time or for its turn
  is not sent; one waiting for its turn also ends with its context);
  `usdo.Client` (transfers in progress end at once instead of timing
  out); `pdo.Listener.Request`; `master`: `NMT` and the commands built on
  it, `Scan`, `Sender` and the senders it returned, `BootSlave`.
- `master.BootNetwork` after `Close` did reset every node on the bus and
  then waited for its context; it now fails with `ErrClosed` and sends
  nothing. One in progress when the master is closed ends with
  `ErrClosed`.
- `master.BootSlave` is cancelled by a network boot-up that starts while
  it runs, which then boots the slave again. Before, the reset of that
  boot-up undid the configuration `BootSlave` was writing, and both
  reported the slave as booted.
- `node.Close` closes the SYNC services before the PDOs, so no SYNC
  reaches a PDO that is being closed.
- `pdo.TPDO`: when a mapped object cannot be read at an event timer
  expiry, the event timer starts again. It used to stay stopped, and a
  cyclic PDO whose value did not change was not sent any more.

Callbacks:

- Application code the library calls is stopped where it is called when
  it does not return normally, logged at error level with its stack
  (`callback did not return normally`, fields `callback`, `value`,
  `stack`), and the service goes on: the handlers of every service,
  `node` and `master`; the dictionary's hooks and watchers when a service
  accesses the dictionary (SDO and USDO servers: the transfer is aborted
  with `AbortGeneral`; RPDO, MPDO consumer: a write error; TPDO: a read
  error; the restore of an NMT reset); state sources and clocks given to
  `nmt`, `emcy` and `timestamp`; the log handler (the entry is lost).
  Before, the process ended. LIMITATIONS.md lists what is not covered.
- `emcy.Consumer`: the event given to the handler and the one `Last`
  returns each have a CANopen FD message of their own; they shared one.

### Documentation

- Every handler option, the dictionary hooks (`od.ReadHook`, `WriteHook`,
  `Watcher`) and every `Close` state their contract: the goroutine,
  what a callback may call, `Close` from a callback, a callback that
  does not return normally, and "safe to call more than once and from
  several goroutines". The ownership of hook data, and of the entries
  `cdcf.PDOSequence`, `store.Merge` and `store.Without` return, is
  stated.
- TESTING.md lists the long-lived types and the lifecycle contract tests
  (L1–L16) that cover them.
- `master` events go through a bounded queue for the bus-driven events
  (the package documentation said "unbounded"); a second `Start` of a
  `pdo` TPDO, RPDO or Listener returns nil (ARCHITECTURE.md said
  `ErrBusy`); `pdo.Protect` accepts 0xFE and 0xFF in a mapping's
  sub-index 0 (`mpdo.ProtectPDO` said it refused them).
- LIMITATIONS.md: a failed SDO or USDO download can still be applied
  later on a bus that reorders frames; a node answers node guarding
  while it produces a heartbeat; what `Close` does not stop.

### Tests

- A regression test for every fix, with `testing/synctest` where the
  test must show that something does not happen while a callback runs;
  617 `Test` functions (512 in v0.6.0).
- Aliasing tests for the values the library hands out or keeps (`od`,
  `pdo`, `mpdo`, `usdo`, `master`, `cdcf`), and an allocation budget for
  `cdcf.Decode`.
- `usdo` `TestChaos` accepts only the data of an earlier, failed download
  in place of the data just downloaded; `pdo` `FuzzUnpack` compares the
  repacked bits; `codegen` `FuzzGenerate` type-checks the generated code.
- `internal/callback`, `internal/goid` and `internal/lifecycle` count
  towards the coverage gate.

### Deferred to a later release

- `Done()`/`Err()` on the node and the services that read the bus.
- Options for the bounds above (`eds` file size and sub-objects, the USDO
  pace), and an SDO-retry setting for the boot slave process that turns
  the retries off.
- The guarding responder silent while 0x1017 is not 0; the data-type
  check of existing request objects in `master.New` and `program.Attach`.
- The lifecycle tests that still wait with sleeps, one leak helper for
  every package, a nightly job with long fuzzing and linux/386 tests.

---

## v0.6.0

**Date:** 2026-11-14
**Previous release:** v0.5.3

### Summary

Tighter bounds on input from the bus and from EDS/DCF files, and a
network boot-up that no longer keeps replaced runs waiting.

### Added

- `eds`: `BuildOptions.MaxCompactBytes` and `DefaultMaxCompactBytes`
  (1 MiB) bound the string and domain value bytes that the
  `CompactSubObj` expansion copies into the elements of an array. A file
  over the bound fails to build; in lenient mode the remaining elements
  of that object are skipped with a warning. A negative value removes the
  bound.

### Changed

- `od`: a NaN written to a REAL32 or REAL64 object that has a
  `LowLimit` or `HighLimit` is refused with abort 0609 0030h (invalid
  value) before the write hooks run. Objects without limits accept NaN as
  before.
- `lss`: a Fastscan listing ends with `ErrFastscan` when more than 127
  values of one address part answer over the listing, even if no address
  completes.
- `master`: the boot slave process checks the length of the uploaded
  0x1000, 0x1F56:01 and 0x1020:01 values and fails with `ErrDataType` on
  malformed data; a malformed configuration date counts as not up to
  date, so the slave is configured again.
- `codegen`: Unicode format characters (such as U+202E and U+FEFF) are
  removed from object names in generated comments.

### Fixed

- `sdo`: block downloads (server) and block uploads (client) without a
  size indication above `WithMaxSize` are aborted with
  `AbortOutOfMemory`, as segmented transfers are; before, up to 7 bytes
  more were accepted.
- `master`: a network boot-up replaced by a newer one at the manager's
  own boot-up ends at once instead of waiting for its successor;
  `BootNetwork` still returns the newest run's result.
- `usdo`: a segmented upload or download times out when the server only
  repeats its previous segment or confirmation; repeats no longer extend
  the response timeout.
- `usdo`: `Server.Close` no longer waits for a blocked bus while sending
  timeout aborts for expired transfers.

---

## v0.5.3

**Date:** 2026-11-14
**Previous release:** v0.5.2

### Summary

Dependency update; no change to go-canopen's own code.

### Dependencies

- go-canbus v0.10.0: input bounds and output encoding in its database,
  capture and transport packages. go-canopen uses its `socketcan`,
  `virtual`, `chaos` and `capture` packages; the changes that reach it are
  `chaos` corruption clamped to the frame payload and the candump writer
  refusing interface names with spaces or control bytes.

---

## v0.5.2

**Date:** 2026-11-14
**Previous release:** v0.5.1

### Fixed

- `sdo`: on a bus that repeats or delays frames, a transfer could fail
  with an abort meant for another request (go-canopen#26). A repeated or
  late segment of an earlier transfer reached the server after that
  transfer had ended; the server answered it with an abort for another
  multiplexer (`0000:00` from an idle server, or the object that the
  segment's bytes named), and the client took that abort as the end of
  the transfer it had just started. One such frame could fail the next
  transfers in a row: `TestChaosBlock` saw 17 of 24 transfers fail on a
  3-CPU macOS runner. The client now ignores an abort for another object
  while it waits for an initiate response, as it already ignored
  responses for another object; `Stats.Ignored` counts them. An abort
  for the transfer's own object, and any abort after the initiate, still
  ends the transfer. A server that aborts an initiate with a multiplexer
  other than the request's (CiA 301 gives the request's) now makes the
  client time out, and retry with `WithRetries`, instead of failing at
  once.

---

## v0.5.1

**Date:** 2026-11-13
**Previous release:** v0.5.0

### Summary

Dependency and interop pin update; no change to go-canopen's own code.
Interoperability against canopen-interop v0.4.0 (contract 1.2), amd64 and
arm64 in CI: 1,620 scenarios, 1,276 passed, 57 known differences, 287
skipped, 0 failed, per adapter and direction the same as before.

### Dependencies

- go-canbus v0.9.1 (go-serial v0.3.0, canbus-interop v0.7.0).
- canopen-interop v0.4.0 images, pinned by release and digest (contract
  1.2 adds the optional `upstream` block of the capability document; the
  three reference images and their operations are unchanged).
  `make interop-images` builds the same tag (`INTEROP_REF`, was `main`).

### Documentation

INTEROPERABILITY.md, TESTING.md, LIMITATIONS.md, docs/FEATURE-STATUS.md,
docs/WEBSITE-INTEGRATION.md.

---

## v0.5.0

**Date:** 2026-11-08
**Previous release:** v0.4.1

### Summary

go-canopen is now tested against traffic recorded from real CANopen
devices: the four message logs Copley Controls publishes of its drives
(16,527 frames; three drives, five axes, identified in their 0x1018
answers) and the drives' EDS, plus the Wireshark wiki's CANopen sample
capture ([testdata/captures](testdata/captures/README.md)). The client
sends every recorded SDO request byte for byte as the recorded master did
and accepts every answer; the server answers every recorded request as
the drive did; the guard master accepts every recorded node guarding
answer. The recordings showed two decoder defects in `dissect.Tracker`
and an EDS validator that called the vendor's EDS invalid.

### Fixed

- `dissect.Tracker`: a node guarding answer with the toggle bit clear was
  decoded as a heartbeat, so half of the recorded drives' 460 answers
  were heartbeats (`701 05` after `RTR 701`). The tracker now remembers
  the request and decodes the answer as `KindGuardResponse` whatever its
  toggle bit. `Decode`, which sees one frame, cannot tell them apart
  (LIMITATIONS.md).
- `eds.File.Validate`: a mappable VISIBLE_STRING, OCTET_STRING or
  UNICODE_STRING was an error, so the Copley Accelnet EDS (15 such
  objects) failed `HasErrors`. CiA 306 does not forbid it; it is now a
  warning that this library cannot map the object. A mappable DOMAIN
  stays an error.

### Added

- `dissect.Tracker` learns PDO COB-IDs from the SDO transfers it sees
  (0x1400..0x15FF and 0x1800..0x19FF sub-index 1, expedited, downloads
  once confirmed): frames on a learned COB-ID decode as that node's PDO,
  with the new `dissect.PDO.Configured` set. The recorded drives send
  their TPDO5 (the high-resolution time stamp 0x1013) on 0x180, which the
  predefined connection set leaves unknown; when a TPDO and another
  node's RPDO share a COB-ID, the frame is the TPDO. A COB-ID with bit 31
  set forgets the PDO's identifier.
- `testdata/captures`: the four Copley logs and their EDS and the
  Wireshark sample capture, byte-identical under clean names
  (`<client-or-peer>_<device>_<operation>.<ext>`), with SHA256SUMS and
  `manifest.json` (original names and URLs, evidence classes, devices,
  golden cases). Every capture test runs on all 16,527 frames in every
  test run.

### Dependencies

- go-canbus v0.9.0 (from v0.8.0): its trace writers keep each
  interface's channel, the candump writer can write direction tokens, and
  an ISO-TP consecutive frame queued when N_Cr expires is no longer
  dropped. go-canopen uses none of the changed functions; no change was
  needed.

### Tests

- `dissect`: every recorded frame decodes without error; SDO answers
  belong to their requests; guard answers to guard requests; configured
  PDOs; identity; emergencies; all 16,527 frames equal Wireshark 4.6's
  CANopen dissector.
- `sdo`: client and server replay of all 1,101 recorded exchanges (the
  server's types from the Copley EDS).
- `nmt`: guard master against the recorded answers (first toggle bit 1);
  the responder's life time against the drive's life guarding error
  (600 ms).
- `cia402`: the recorded state changes against the state diagram: fault
  reset is transition 15; the drives' shortcut from Switch on disabled to
  Operation enabled, which `Next` and `Drive` do not take, is pinned as
  vendor behaviour.
- `eds`: the Copley EDS validates and matches every recorded transfer's
  object, access and size.
- Fuzz seeds from recorded frames for `dissect`, `sdo`, `nmt` and `emcy`.

### SAST

gosec and opengrep on the release branch, every severity and confidence:
two new findings (G115 in the test harness's CANview reader: the time
stamp and hex bytes are now parsed without conversions), fixed; 0 open,
the two recorded false positives unchanged.

### Interoperability

Recorded evidence covers expedited SDO, node guarding, PDOs, EMCY, SYNC
and CiA 402 only; segmented and block SDO, aborts, heartbeat, LSS and
MPDO remain verified against the software stacks of canopen-interop
alone (INTEROPERABILITY.md). The Wireshark sample's writer left 0x7F in
the FD flags byte of classical CANopen frames, so go-canbus and Wireshark
read them as CAN FD as the link type defines; the tests read the file as
classical frames with their own reader, since go-canopen decodes CAN FD
frames on the SDO identifiers as USDO.

---

## v0.4.1

**Date:** 2026-11-04
**Previous release:** v0.4.0

### Fixed

- `sdo`: a segmented upload could return wrong data of the right length
  after a retry (`WithRetries`) on a bus that delays or reorders frames.
  A segment request of the timed-out attempt, delivered after the new
  attempt's initiate, made the server send a segment the client had not
  asked for yet; the client took it as the answer to its next request,
  and the one-bit toggle let a shift by two segments through
  (canopenctl's chaos test read `OTF IO-401 8DIOTF IO-I/2AO`). The
  client now discards responses received before it sends a segment
  request or a download segment; the transfer then ends with a toggle
  abort, which a retry repeats, instead of wrong data. An abort among
  the discarded responses still ends the transfer. `Stats.Ignored`
  counts the discarded responses.

---

## v0.4.0

**Date:** 2026-11-04
**Previous release:** v0.3.3

Hardening after a full SAST review (gosec and opengrep, every severity
and confidence): 216 findings to 0 open, two of them false positives
recorded in otfabric/sast-triage. Most integer conversions were already
bounded and now show it; the ones below were real defects. New hostile-peer,
fuzz and chaos tests found four more.

### Breaking

- Go 1.26 or later is required (`go fix` applied).
- `usdo`: `WithMaxSize` now limits client uploads too, with the server's
  default of 1 MiB. A USDO upload larger than that fails with
  `AbortOutOfMemory` unless the client raises the limit or sets 0 (no
  limit), as the `sdo` client already did.
- `pdo.CommIndex` and `pdo.MappingIndex` return 0 for a PDO number
  outside 1..512; `CommIndex(false, 513)` returned RPDO1's mapping index.
- `master`: the event queue is bounded (see Fixed).
- `od`: `Dictionary.Add` refuses a node-ID-relative default that does
  not fit its type for node-ID 127 (UNSIGNED8 0x81 and above); it
  wrapped at the type's width (0xF0 on node 0x20 gave 0x10). EDS and DCF
  input refused such values only for the node-ID they were loaded with.

### Fixed

- `sdo`: one malformed frame on ID 0 (remote, 3 or 8 bytes, Len 64, FD on
  a classical node) fenced the node's SDO server until the next real
  reset. The server now takes exactly the reset frames the NMT slave
  executes (`nmt.ParseCommand`).
- `usdo`: a server indicating an upload of 0xFFFFFFFF made the client
  request segments until its context ended. On 32-bit platforms a size
  of 2 GiB or more made client and server panic. Data of 4 GiB or more was
  downloaded with a truncated size. Values of 4 GiB or more are refused
  with `AbortOutOfMemory` (SDO: 0x05040005) instead of being announced
  with a wrapped size; a server `WithMaxSize` of 4 GiB or more no longer
  wraps into a small limit.
- `lss`: `Fastscan` against a slave that acknowledges every query never
  ended before its context; it now stops at `Max` and fails with
  `ErrFastscan` beyond 127 devices.
- `lss`: an LSS answer later than the Fastscan window (a busy device or
  host) made `FastscanSelect` and `AssignAll` fail with `ErrFastscan`
  and made `Fastscan` and `AssignAll` miss devices. A restarted
  identification and a listing step whose silence contradicts the
  acknowledged prefix are repeated with the window doubled each time,
  up to 8 times `WithFastscanTimeout`, which then applies again. A
  healthy bus costs nothing more.
- `od`: with a node-ID above 127 (0xFF, LSS unconfigured) a
  node-ID-relative default no longer wraps; it keeps its plain value.
- `master`: an EMCY or heartbeat flood with a slow event handler grew
  the event queue without limit. Frame-driven events (EMCY, boot-up,
  state change) are queued up to `DefaultEventQueue` (4096, option
  `WithEventQueue`); the oldest are dropped and counted in
  `Stats.EventsDropped`. Boot results, heartbeat and guarding events are
  never dropped.
- `master`: a concise DCF or `Overlay` entry with a mapping count of
  0xFF.. panicked; values wider than their PDO field are refused with
  `AbortPDOLength` / `AbortValueHigh` instead of being truncated.
  Identity (0x1000, 0x1018), program identification (0x1F56) and
  date/time answers wider than 32 bits no longer match on their low 32
  bits; `Scan` and `Identity` return `ErrDataType`.
- `ParseValue` and EDS/DCF parsing: a `$NODEID` literal whose sum
  overflows its type, and a REAL32 bit pattern wider than 32 bits, were
  accepted with a wrapped value; they are now errors.
- `od.NewArray` with a negative count gave sub-index 0 the value 255;
  `od.PDOObjects` with n outside 1..512 wrapped the index.
  `eds.FromDictionary` exports SubNumber 255 (not 0) for an object with
  256 sub-indices.
- PDO, MPDO, SYNC, EMCY, NMT, LSS and SDO parameters stored with a type
  wider than CiA 301 gives them (a malformed EDS) were truncated, often
  into a valid value: a 0x1019 of 256 acted as 0, a transmission type
  0x1FE as 254, a 0x1029 of 0x100 as 0. Such values are now refused on
  write and ignored when read. A local 0x1019 of 1 or 241..255 is no
  longer applied by the SYNC consumer.
- `nmt`: guard time × life time factor beyond `time.Duration` wrapped to
  a negative life time and an immediate life-guarding event; it
  saturates. `WithLifeGuarding` with a negative guard time turns life
  guarding off, as `SetLifeGuarding` did. Heartbeat, guard and EMCY
  inhibit times saturate instead of wrapping.

### Added

- `master.WithEventQueue`, `master.DefaultEventQueue`,
  `Stats.EventsDropped`.
- Tests: `e2e/hostile_test.go` (scripted hostile SDO/USDO servers and
  clients, LSS, NMT, EMCY/heartbeat floods, malformed frames; leak and
  memory bounds), `TestMonkeyNodeSDO` (node and SDO client through a
  dropping, duplicating, reordering bus), fuzz targets `FuzzNodeFrames`
  and `FuzzMasterFrames`, and boundary tests per package.

### Changed

- go-canbus v0.8.0 (Go 1.26 floor, its own SAST hardening): the slcan
  read loop no longer panics on a reader reporting more bytes than its
  buffer; `unsafe` in SocketCAN is confined to three syscalls; capture
  writers refuse timestamps their format cannot hold
  (`capture.ErrUnrepresentable`) instead of wrapping. No API change for
  go-canopen.
- canopen-interop images v0.3.1 (contract 1.1 unchanged), shared
  workflows v3.3.1, CI on Go 1.26 and 1.27.

---

## v0.3.3

**Date:** 2026-10-31
**Previous release:** v0.3.2

### Changed

- Policy 1.2: build output in `bin/` and `release/`, a per-package coverage floor, and more tests and CI. No change in behaviour.
- Built on go-canbus v0.7.2.

---

## v0.3.2

**Date:** 2026-10-30
**Previous release:** v0.3.1

Fixes of the program download (CiA 302-3) and of the scan in `master`, and
of the node-ID assignment in `lss`.

- A step of `DownloadProgram` whose answer came after the SDO timeout
  (a loaded system, a slow flash) was carried out by the device, but the
  master repeated it: Clear and the program data were then refused with
  abort 0x08000022 (the program was no longer in the state they need).
  The download failed, a boot slave process ended with status I, and when
  the data had reached the device the new program stayed stopped: the
  next boot slave process found it up to date and did not start it. A
  step that timed out is now repeated only after reading the program
  state (0x1F51), and a late answer met by the next request (a protocol
  error) no longer fails the download. A timed-out start is repeated too
  unless the program runs. Found by CI; regression test
  `TestDownloadProgramLateAnswers`.
- `Master.Scan` took a device for absent, without an error, when one
  request timed out (a lost frame, a loaded system). It now repeats a
  request that timed out once, so a scan of absent nodes takes twice as
  long. Found by CI; regression test `TestScanRepeatsTimedOutRequest`.
- `lss.Master.FastscanSelect` and `AssignAll`: when the acknowledgement of
  the Fastscan step that selects a slave was lost, the selection restarted
  with that slave still in the configuration state. If its answer to the
  check of the next selection was lost too, the configure node-ID request
  reached both slaves and they took the same node-ID. The master now
  switches the slaves to waiting and checks before it restarts, and sends
  the switch before each check of that step. The case needs lost frames;
  see LIMITATIONS.md. Found by `make chaos`; regression test
  `TestAssignAllLostSelectionAck`.

---

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
