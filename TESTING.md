# Testing

## Levels

| Level | Where | Command |
|-------|-------|---------|
| Unit and property tests, race detector | any OS | `make test` |
| Tests on SocketCAN (vcan): the e2e plant and scenarios over `vcan0` (CAN FD sockets for the CANopen FD scenario) | Linux with `vcan0` (MTU 72) | `make vcan-setup test-vcan` (`CANOPEN_REQUIRE_VCAN=1`: a missing interface fails instead of skipping) |
| Wireshark CANopen dissector oracle (`dissect`) | any OS with tshark | `make test`; CI's vcan jobs install tshark and set `CANOPEN_REQUIRE_TSHARK=1` |
| python canopen EDS cross-check (`eds`) | any OS with python canopen | `EDS_PYCANOPEN=/path/to/python make test`; skipped (with a message) without it; not run in CI |
| End-to-end plant | any OS (virtual bus); Linux with vcan0 also SocketCAN | `make e2e` |
| Chaos monkey | any OS | `make chaos CHAOS_ITERATIONS=200`; CI: 20 seeds on every push; the Nightly chaos workflow: 500 seeds on amd64 and arm64 |
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
or tshark skip there. CI runs the module on Go 1.23–1.26 (with and
without the race detector) through the shared otfabric workflow, on macOS
and Windows, and on Linux amd64 and arm64 with vcan.

## Test inventory

402 `Test` functions, 25 fuzz targets, 13 `Example` functions and 4
benchmarks (SDO), in every library package and `e2e`; and the interop
suite (`interop/`, build tag `interop`): 4 `Test` functions with 1,620
scenarios against three stacks, canopen-interop v0.2.0 (contract 1.1:
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
| pdo | `FuzzUnpack` | Mappings and payloads: what unpacks repacks to the mapping's length; decoding never panics |
| mpdo | `FuzzParseMessage`, `FuzzEntries`, `FuzzReceive` | MPDO frames; scanner and dispatcher entries (encode/decode round trip, validation); frames to a consumer: no panic, only valid writes |
| lss | `FuzzSlave`, `FuzzResponse` | Master requests to the slave state machine; slave responses to the master's parser |
| usdo | `FuzzDecode`, `FuzzServer`, `FuzzClient` | USDO messages; request sequences to the server; responses to client transfers |
| cdcf | `FuzzDecode` | Concise DCF bytes: no panic; what decodes encodes back |
| eds | `FuzzParse` | EDS/DCF text: no panic or hang; what parses survives Write and Parse unchanged |
| codegen | `FuzzGenerate` | EDS models: no panic; accepted files give Go source that parses |
| cia402 | `FuzzDrive` | Controlword, mode, option code, fault and step sequences: the statusword always matches a valid state, every transition is one of the state diagram |
| dissect | `FuzzDecode`, `FuzzDecodeFD` | Arbitrary classical and CAN FD frames: no panic |

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
  with the right data or fails with an abort or a timeout.
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
| `internal/nettest`, `internal/testbus` | Test helpers |
| `e2e` | The end-to-end tests themselves (they count towards the library's coverage) |

Measured 2026-10-29 on macOS (no vcan: the SocketCAN e2e tests skip),
`make coverage-check`; every package is at least 90 %:

| Package | Statements | Coverage |
|---------|-----------:|---------:|
| `canopen` (root) | 299 | 99.7 % |
| `cdcf` | 136 | 97.1 % |
| `cia402` | 853 | 96.1 % |
| `codegen` | 277 | 97.8 % |
| `dissect` | 385 | 100.0 % |
| `eds` | 840 | 97.9 % |
| `emcy` | 429 | 98.1 % |
| `lss` | 892 | 96.5 % |
| `master` | 1371 | 94.4 % |
| `mpdo` | 470 | 98.7 % |
| `nmt` | 768 | 97.9 % |
| `node` | 745 | 92.6 % |
| `od` | 365 | 99.5 % |
| `pdo` | 879 | 97.4 % |
| `program` | 122 | 99.2 % |
| `sdo` | 939 | 97.6 % |
| `store` | 96 | 96.9 % |
| `sync` | 316 | 98.4 % |
| `timestamp` | 255 | 98.4 % |
| `usdo` | 846 | 96.1 % |
| **total** | 11283 | **96.9 %** |

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
