# Testing

## Levels

| Level | Where | Command |
|-------|-------|---------|
| Unit and property tests, race detector | any OS | `make test` |
| Tests on SocketCAN (vcan): the e2e plant and scenarios over `vcan0` (CAN FD sockets for the CANopen FD scenario) | Linux with `vcan0` (MTU 72) | `make vcan-setup test-vcan` (`CANOPEN_REQUIRE_VCAN=1`: a missing interface fails instead of skipping) |
| Recorded traffic of real drives (the four Copley Controls logs, 16,527 frames, their EDS, the Wireshark sample capture; [testdata/captures](testdata/captures/README.md)) | any OS (the Wireshark comparison with tshark) | `make test` |
| Wireshark CANopen dissector oracle (`dissect`) | any OS with tshark | `make test`; CI's vcan jobs install tshark and set `CANOPEN_REQUIRE_TSHARK=1` |
| python canopen EDS cross-check (`eds`) | any OS with python canopen | `EDS_PYCANOPEN=/path/to/python make test`; skipped (with a message) without it; not run in CI |
| End-to-end plant | any OS (virtual bus); Linux with vcan0 also SocketCAN | `make e2e` |
| Chaos monkey | any OS | `make chaos CHAOS_ITERATIONS=200`; CI: 20 seeds on every pull request; the Nightly chaos workflow: 500 seeds on amd64 and arm64 |
| Fuzzing | any OS | `make fuzz FUZZTIME=30s` (CI: 10 s per target); the targets are found automatically (`go test -list '^Fuzz'`) |
| Coverage gate (90 %) | any OS; CI measures it on Linux with vcan | `make coverage-check` |
| Per-package floor (90 %) | as above | `make coverage-pkg-check` |
| Golden code generation | any OS | `make generate-check` |
| Examples | any OS | `make examples` (each self-contained example must exit successfully) |
| Cross builds | any OS | `make cross` (linux amd64/arm64/arm, darwin amd64/arm64, windows amd64/arm64) |
| Lint | any OS | `make lint` (staticcheck, also as GOOS=linux), `make lint-ci` (golangci-lint with `.golangci.yml`), `make vet` |
| Interoperability with independent stacks (CANopenNode, Lely CANopen, python canopen; canopen-interop images pinned by digest) | Linux, Docker, vcan0 | `make interop` (writes `bin/interop-report.json`; `CANOPEN_REQUIRE_INTEROP=1`: a missing Docker, image or interface fails); the Interop workflow on amd64 and arm64. See [INTEROPERABILITY.md](INTEROPERABILITY.md) |
| MicroVM kernel matrix | — | Not applicable: go-canopen uses no kernel CAN feature beyond go-canbus's raw sockets, which go-canbus tests on kernels 6.1–7.0 |
| Everything above except vcan and interop | any OS | `make check` |

The interop suite runs locally in a Linux VM (the Lima VM `canvm`,
arm64): cross-compile the test binary into the repository's `bin/`
(`GOOS=linux GOARCH=arm64 go test -c -tags interop -o bin/interop.test
./interop`), and run it from `interop/` inside the VM with Docker
(`sg docker -c`), `CANOPEN_REQUIRE_VCAN=1 CANOPEN_REQUIRE_INTEROP=1` and
`CANOPEN_INTEROP_REPORT` pointing to a writable path; a full run takes
about 6.5 minutes.

A plain `go test ./...` passes on macOS and Windows: tests that need vcan
or tshark skip there. CI runs the module on Go 1.26 and 1.27 (with and
without the race detector) through the shared otfabric workflow, on macOS
and Windows, and on Linux amd64 and arm64 with vcan.

## Test inventory

617 `Test` functions, 27 fuzz targets, 13 `Example` functions and 4
benchmarks (SDO), in every library package and `e2e` (including the
hostile-peer suite `e2e/hostile_test.go` and the chaos test
`TestMonkeyNodeSDO`); and the interop
suite (`interop/`, build tag `interop`): 4 `Test` functions with 1,620
scenarios against three stacks, canopen-interop v0.4.0 (contract 1.2:
LSS, MPDO, CiA 302 boot-up, concise DCF and SYNC window included;
[INTEROPERABILITY.md](INTEROPERABILITY.md)).
`go test -race -count=10 ./node ./master ./e2e` passes (twice, macOS), and
the SocketCAN e2e tests pass ten times in a row on vcan0 (MTU 72) in a
Linux 7.0 arm64 VM.

### Fuzz targets

| Package | Target | Input |
|---------|--------|-------|
| canopen | `FuzzParseValue` | Value literals for any data type: a parsed value has the type's size and formats back |
| sdo | `FuzzServer` | Request sequences (expedited, segmented, block) to the server state machine: no panic |
| sdo | `FuzzClient` | Server responses to client transfers: no panic, documented errors only |
| nmt | `FuzzParseCommand`, `FuzzParseErrorControl` | NMT and error control frames |
| emcy | `FuzzParseFrame`, `FuzzParseFDFrame` | Emergency frames, classical and 20-byte CANopen FD (both layouts) |
| sync | `FuzzReceive`, `FuzzReceiveFD` | Frames to a SYNC consumer, classical and CAN FD: the counter reported is the one received |
| timestamp | `FuzzParseFrame` | TIME frames |
| pdo | `FuzzUnpack` | Mappings and payloads: what unpacks repacks to the same mapped bits (the bits of the last byte beyond the mapping are 0); decoding never panics |
| mpdo | `FuzzParseMessage`, `FuzzEntries`, `FuzzReceive` | MPDO frames; scanner and dispatcher entries (encode/decode round trip, validation); frames to a consumer: no panic, only valid writes |
| lss | `FuzzSlave`, `FuzzResponse` | Master requests to the slave state machine; slave responses to the master's parser |
| usdo | `FuzzDecode`, `FuzzServer`, `FuzzClient` | USDO messages; request sequences to the server; responses to client transfers |
| cdcf | `FuzzDecode` | Concise DCF bytes: no panic; what decodes encodes back |
| eds | `FuzzParse` | EDS/DCF text: no panic or hang; what parses survives Write and Parse unchanged |
| codegen | `FuzzGenerate` | EDS models: no panic; accepted files give Go source that parses and type-checks against this module's packages (only parsed where the go command or the module's dependencies are not at hand) |
| cia402 | `FuzzDrive` | Controlword, mode, option code, fault and step sequences: the statusword always matches a valid state, every transition is one of the state diagram |
| dissect | `FuzzDecode`, `FuzzDecodeFD` | Arbitrary classical and CAN FD frames: no panic |

The fuzz targets of `dissect`, `sdo` (server and client), `nmt` (error
control) and `emcy` are also seeded with frames recorded from Copley
drives ([testdata/captures](testdata/captures/README.md)).

### Chaos

go-canbus's `chaos` bridge (seeded drop, duplicate, delay, reorder,
corrupt) sits between peers:

- `sdo`: `TestChaosBlock`, `TestChaosBlockDrops`, `TestChaosSegmented`,
  `TestChaosCorruptBlock`: a transfer either completes with the right
  data or fails with an abort or a timeout (corruption only for block
  transfers, which carry a CRC).
- `nmt`: heartbeat supervision through a lossy, delaying, duplicating and
  reordering bridge: no false timeout, no false state.
- `pdo`: an event-driven TPDO with an event timer through a lossy,
  duplicating bridge: the event timer repeats lost PDOs, duplicates are
  harmless, the RPDO deadline fires when the link breaks.
- `master`: `TestMonkeyMasterNode` runs a master and a node through random
  physical faults (drops, duplicates, delays): SDO traffic, PDO
  configuration, NMT state changes, heartbeat supervision, PDO traffic and
  a reset. Nothing succeeds with wrong data, the node only takes
  commanded states, no PDO crosses while the node is not operational, and
  everything shuts down without leaking goroutines. 5 seeds in `make
  test`; `CANOPEN_CHAOS_ITERATIONS` (set by `make chaos`) for more.
- `master`: `TestMonkeyManagerBoot` (the CiA 302 network boot-up with a
  concise DCF through random faults) and `TestMonkeyLSSBoot`: two
  unconfigured devices, the network boot-up with the LSS step
  (`WithLSSBoot`) through random drops, duplicates and delays; every
  device must end with the node-ID of the slave whose identity it has
  (never another, never one twice), configured, the boot-up retried while
  it fails cleanly.
- `lss`: `TestMonkeyAssignAll`, `TestMonkeyFastscanList`: AssignAll never
  gives a node-ID twice, Fastscan never invents a device.
- `usdo`: `TestChaos`, `TestChaosCorrupt` through a CAN FD chaos bridge
  (`internal/testbus.Chaos` with `virtual.WithFD()`): a transfer completes
  with the right data or fails with an abort or a timeout. The one
  accepted difference is the data of an earlier, failed download stored
  by its last request arriving late (LIMITATIONS.md, "A failed download
  can still be applied later on a reordering bus").
- `mpdo`: `TestChaos`: MPDOs through a lossy, duplicating bridge.

### End-to-end

`e2e` builds a plant: a master, two I/O modules (TPDO on SYNC, RPDO,
heartbeat supervision between them) and a CiA 402 drive, booted with boot
procedures, configured, operated (SDO, PDO, SYNC, EMCY, heartbeat loss,
resets, a node-ID change). `TestPlantVirtual` runs it on the virtual bus,
`TestPlantSocketCAN` (Linux) on vcan0 or `CANOPEN_TEST_IFACE`.

`TestScenariosVirtual` and `TestScenariosSocketCAN` run the scenarios of
the v0.3.0 features, each participant on its own port or CAN_RAW socket:

| Scenario | What |
|----------|------|
| `LSSNetworkBoot` | Three unconfigured nodes (`node.WithLSS`); LSS `AssignAll` (stored) gives them node-IDs 5..7, then the CiA 302 network boot-up configures each from its concise DCF and starts it |
| `MPDO` | DAM- and SAM-MPDOs between two nodes; the dispatcher list written by the master |
| `FDNetwork` | CANopen FD master and two nodes (CAN FD sockets): PDO mapping over USDO, a 64-byte PDO from one node to the other, an FD emergency |
| `SyncWindow` | A synchronous RPDO after the window (0x1007) is discarded, one within it applied |
| `StoreRestart` | Parameters saved over SDO survive a node restart (file store); restored defaults apply after a reset node |
| `ProgramDownload` | CiA 302-3 program download with SDO block transfer |

### Leaks and races

Every package runs with `-race` in `make test` and CI. Tests of
long-running services check for leaked goroutines
(`internal/nettest.CheckLeaks`). Service hooks are checked too: creating
and closing nodes repeatedly on one dictionary leaves its hook count
(`od.Dictionary.HookCount`) unchanged (`TestRecreateNodeHooks`).

### Lifecycle contract tests

The long-lived types, and the rows of the contract test matrix of the
engineering standard (lifecycle.md, L1–L16) that apply to them. MUST: the
type has the feature. n/a: it cannot have it, with the reason. A test
named without a package is in the package of its type.

Types: `node.Node`; `master.Master` (with its CiA 302 manager);
`nmt.Slave`, `HeartbeatProducer`, `HeartbeatConsumer`, `GuardMaster`,
`GuardResponder`; `sdo.Client`, `sdo.Server`; `usdo.Client`,
`usdo.Server`; `lss.Master`, `lss.Slave`; `emcy.Producer`, `Consumer`;
`syncobj.Producer`, `Consumer`; `timestamp.Producer`, `Consumer`;
`pdo.TPDO`, `RPDO`, `Listener`; `mpdo.Producer`, `Consumer`. The types
with one goroutine and no other state to close (`nmt`, `lss.Slave`,
`emcy`, `syncobj`, `timestamp`, `usdo`) share `internal/lifecycle`, whose
tests cover L1, L2, L10 and L14 for all of them. `nmt.Master`,
`cia402.Controller`, `cia402.Drive` and `program.Device` have no
goroutine.

| Row | Applies | Tests |
|-----|---------|-------|
| L1 Close twice | MUST, every type | `internal/lifecycle` `TestStartClose`, `TestCloseBeforeStart`; `node` `TestNodeClosed`, `TestAutoStartAndLocalControl`; `sdo` `TestServerLifecycle`, `TestClientClose`; `usdo` `TestLifecycleAndOptions`; `lss` `TestMasterLifecycle`, `TestSlaveLifecycle`; `master` `TestEventsAndLifecycle`; `pdo` `TestTPDOTransmit`, `TestSenderListener`; `mpdo` `TestConsumerLifecycle`, `TestProducerErrors`; `nmt` `TestHeartbeatConsumer` |
| L2 two goroutines call Close | MUST, every type with a goroutine: every Close returns only when the goroutine has ended | `internal/lifecycle` `TestEveryCloseWaits`; `sdo`, `usdo` `TestServerEveryCloseWaits`; `lss` `TestSlaveEveryCloseWaits`; `pdo` `TestRPDOEveryCloseWaits`; `mpdo` `TestConsumerEveryCloseWaits`; `node` `TestNodeEveryCloseWaits`; `master` `TestMasterEveryCloseWaits`; `nmt` `TestCloseEndsReset` (Close waits for a reset in progress) |
| L3 Close during a blocked read or wait | MUST for the clients and waits | `sdo` `TestClientClose`; `usdo` `TestClientClosed`; `nmt` `TestHeartbeatConsumer` (`WaitBootUp`); `master` `TestCloseEndsOperations` (network boot-up, scan), `TestCloseEndsBootSlave`, `TestBootNetworkAfterClose`; `mpdo` `TestCloseEndsInhibitWait`; `mpdo` `TestSubscriptionEnds`, `pdo` `TestBusClosed`, `TestListenerBusClosed` (the bus ends) |
| L4 Close during a write | MUST where a send can block | `pdo` `TestCloseEndsTransmit`, `TestCloseBetweenSampleAndSend`, `TestTPDOSyncWindowSendBounded`; `nmt` `TestCloseEndsBootUpSend`; `usdo` `TestCloseEndsBulkDownload`; otherwise each send is one CAN frame, bounded by its send timeout (LIMITATIONS.md, "Operations racing Close") |
| L5 Close during a handshake | n/a: no service has a handshake. Close during `Start` | `internal/lifecycle` `TestCloseFromStart`; `nmt` `TestSlaveHandlerClosesSlave/Start`; `node` `TestNodeHandlerClosesNode/state_handler_of_Start` |
| L6 cancel a pending request | MUST for the clients | `sdo` `TestClientContext`; `usdo` `TestClientTimeoutAndCancel`; `master` `TestScanRefusedAndCancelled`, `TestBootRestartedByNewBootUp`, `TestBootNetworkSuperseded`, `TestBootSlaveCancelledByNetworkBoot`; `lss` `TestMasterContext`, `TestSequenceRules` (a service waiting for a sequence ends with its context); `mpdo` `TestInhibitTime` |
| L7 response after the request timed out | MUST for the clients | `sdo` `TestClientStaleAbortAtInitiate`, `TestClientStaleSegment` (counted in `sdo.ClientStats.Ignored`); `usdo` `TestClientIgnores`; `master` `TestDownloadProgramLateAnswers`; `lss` `TestMasterUnexpectedFrames` (counted in `lss.MasterStats.Unexpected`) |
| L8 response after reconnect | n/a: no type reconnects | — |
| L9 callback calls a public method | MUST for the types with callbacks | the handler in `TestServerHookClosesServer` (`sdo`, `usdo`), `TestSlaveHandlerClosesSlave` (`nmt`, `lss`), `TestEventHandlers` (`nmt`), `TestRPDOHandlers`, `TestListenerHandler` (`pdo`), `TestConsumerHandlers` (`mpdo`), `TestConsumerHandler` (`emcy`, `sync`, `timestamp`), `TestNodeHandlerClosesNode` (`node`), `TestEventHandlerClosesMaster` (`master`) calls a method of its service before it closes it; `node` `TestAutoStartAndLocalControl` (autoStart sets the state from the state handler) |
| L10 callback calls Close | MUST for the types with callbacks: Close returns, and a Close on another goroutine waits for the callback | the tests of L9; `internal/lifecycle` `TestCloseFromServiceGoroutine` |
| L11 slow callback | MUST for `master` (events are queued); the other callbacks run on their service's goroutine and hold it (documented) | `master` `TestEventQueueBounded` |
| L12 queue at capacity | MUST for the bounded queues | `master` `TestEventQueueBounded` (oldest dropped, `EventsDropped`); `emcy` `TestProducerInhibit` (`ErrBusy`, `Dropped`), `TestProducerClosed` (queued messages dropped at Close); `usdo` `TestServerBusyAndParallel` (`AbortBusy`) |
| L13 concurrent sessions | MUST for the multi-session types | `usdo` `TestConcurrentSessions`; `sdo` `TestClientContext` (transfers of one client are serialised); `master` `TestScan`, `TestManagerNetworkBoot`; `lss` `TestConfigureIsOneSequence`, `TestSequenceRules`; `node` `TestServicesFollowTheStateThatHolds` (two goroutines change the NMT state) |
| L14 failure during construction or start | MUST for every type that acquires resources | `internal/lifecycle` `TestSetupFails`; `node` `TestNewErrors`, `TestRecreateNodeHooks`; `nmt` `TestSlaveErrors` (boot-up send failure); `master` `TestEventsAndLifecycle`; `pdo` `TestNewErrors`; `mpdo` `TestSubscribeFailure`; `lss` `TestMasterLifecycle`, `TestSlaveSendError`; `sync` `TestProducerSendError`; `timestamp` `TestPeriodic` |
| L15 `Shutdown(ctx)` | n/a: no type has a graceful shutdown | — |
| L16 Close racing reconnect | n/a: no type reconnects | — |
| Operations after Close | MUST, every type: `ErrClosed`, nothing sent | `node` `TestNodeClosed`; `master` `TestOperationsAfterClose`, `TestBootNetworkAfterClose`; `nmt` `TestSlaveClosed`; `pdo` `TestClosedTPDOSendsNothing`, `TestClosedRPDOAppliesNothing`; `usdo` `TestClientClosed`; `emcy`, `sync`, `timestamp` `TestProducerClosed`; `mpdo` `TestCloseEndsInhibitWait` |
| Callback that does not return normally | MUST, every call site: logged, the service goes on | `internal/callback`; `TestServerHookDoesNotReturnNormally` (`sdo`, `usdo`), `TestLoggerDoesNotReturnNormally` (`sdo`), `TestSlaveHandlerDoesNotReturnNormally` (`nmt`, `lss`), `TestEventHandlers`, `TestStateSourceDoesNotReturnNormally` (`nmt`), `TestRPDOHandlers`, `TestTPDOReadHookDoesNotReturnNormally`, `TestListenerHandler` (`pdo`), `TestConsumerHandlers` (`mpdo`), `TestConsumerHandler`, `TestClockDoesNotReturnNormally` (`emcy`, `timestamp`; `sync` the first), `TestAssignHandlerDoesNotReturnNormally` (`lss`), `TestNodeHandlerDoesNotReturnNormally` (`node`), `TestEventHandlerDoesNotReturnNormally` (`master`) |

The tests that assert that something does not happen while a callback
runs use `testing/synctest`; many older lifecycle tests still wait with
sleeps, which is to be replaced in a later release.

## Coverage policy

`make coverage` measures all library packages together (`-coverpkg`), so
code exercised through another package counts. `make coverage-check`
fails below 90 % total. `make coverpkg-check` keeps the package list in
`.github/workflows/ci.yml` equal to the Makefile's.

| Excluded | Reason |
|----------|--------|
| `examples/` | Programs, run by `make examples` |
| `internal/cmd/gengolden` | Build tool; its output is checked by `make generate-check` |
| `internal/golden/` | Generated code, verified by `codegen`'s differential test |
| `internal/capture`, `internal/nettest`, `internal/testbus` | Test helpers |
| `e2e` | The end-to-end tests themselves (they count towards the library's coverage) |

Measured 2026-11-14 on macOS (no vcan: the SocketCAN e2e tests skip),
`make coverage-check`; every package is at least 90 %:

| Package | Statements | Coverage |
|---------|-----------:|---------:|
| `canopen (root)` | 320 | 99.4 % |
| `cdcf` | 138 | 97.1 % |
| `cia402` | 885 | 95.6 % |
| `codegen` | 278 | 98.2 % |
| `dissect` | 425 | 100.0 % |
| `eds` | 877 | 98.7 % |
| `emcy` | 448 | 98.9 % |
| `internal/callback` | 40 | 100.0 % |
| `internal/goid` | 12 | 100.0 % |
| `internal/lifecycle` | 52 | 100.0 % |
| `lss` | 966 | 96.8 % |
| `master` | 1544 | 94.6 % |
| `mpdo` | 512 | 98.8 % |
| `nmt` | 801 | 98.3 % |
| `node` | 784 | 93.2 % |
| `od` | 390 | 99.2 % |
| `pdo` | 948 | 97.6 % |
| `program` | 124 | 99.2 % |
| `sdo` | 1020 | 97.5 % |
| `store` | 96 | 96.9 % |
| `sync` | 333 | 98.8 % |
| `timestamp` | 254 | 97.6 % |
| `usdo` | 877 | 95.8 % |
| **total** | 12124 | **97.1 %** |

## Running

```sh
make test                    # race, every package
make vcan-setup test-vcan    # Linux: SocketCAN must run, not skip
make coverage-check          # coverage.out, gate 90 %
make fuzz FUZZTIME=30s       # every fuzz target
make chaos CHAOS_ITERATIONS=200
make e2e
make check                   # generated code, format, lint, vet, vuln, race tests, coverage, examples, cross builds
```

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml`, `interop.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml`, `interop.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `nightly.yml` | the scheduled runs, on `main` |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
