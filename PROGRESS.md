# Progress

Durable state of the CANopen work (go-canopen, canopen-interop,
canopenctl). Newest first within each section.

## Status (2026-10-29): go-canopen v0.3.0 prepared (branch `next`)

| Repo | Release | Notes |
|------|---------|-------|
| go-canopen | v0.3.0 (section in RELEASE.md) | LSS, MPDO, CANopen FD (USDO), SYNC window, store/restore, CiA 302 boot-up, concise DCF, program download; integrated in `node` and `master`. Interop against canopen-interop v0.2.0 (contract 1.1): 1,620 scenarios, 0 failures (local); LSS, MPDO, CiA 302 boot-up, concise DCF, SYNC window covered; CANopen FD, store/restore and program download have no oracle |

## Interop contract 1.1 (2026-10-29)

`interop/` on canopen-interop v0.2.0 (digests in harness_test.go):
lss_test.go, mpdo_test.go, boot_test.go, cdcf_test.go,
syncwindow_test.go (FD recorded as skips). Local run (Lima canvm):
1,620 scenarios, 0 failures. Fixed from it: `AssignAll` at node-ID 127
and with Lely slaves (Fastscan answered with a pending node-ID),
`sdo.WithResetFence` (a node answered SDO requests that followed an NMT
reset command, Lely's boot master then saw status M), SYNC window on the
monotonic clock (the VM steps its wall clock). The manager scenarios
configure 0x1F80 before Start: setting it after Start and calling
`BootNetwork` races the automatic network boot-up of the master's own
boot-up (the explicit call can end with "context canceled").

## v0.3.0 integration (2026-10-29)

- `node`: `WithLSS` (unconfigured start with node-ID 0xFF; services
  built with a placeholder node-ID and started by the reset that applies
  a real one; LSS settings in the store under pseudo index 0x0000), MPDO
  services per MPDO-configured PDO (watcher on mapping sub 0), SYNC window
  from each received SYNC, `WithFD` (USDO server replaces the SDO server,
  guarding left out, FD EMCY with CiA spec 301 and the system clock).
- `nmt`: `WithFD`, `Unconfigured` slaves (no boot-up; only reset
  commands), FD parsers. Fixed a data race: package-level
  `canbus.WithFilters` options are written by every Subscribe (go-canbus
  copies the filter slice into the captured FilterSet); they are now
  built per call. Found when several unconfigured nodes started their
  services concurrently from LSS resets.
- `master`: an internal `remote` access (SDO client, or the USDO client
  with `WithFD`) behind every procedure; USDO aborts wrapped so that
  `errors.As(*canopen.AbortError)` still holds. `LSS()`, `WithLSSBoot`
  (Fastscan rounds until one assigns nothing; boot-ups during the LSS
  step are not booted, the reset after it boots them — without that, a
  slave booted during the step was reset by the step's broadcast reset
  communication and failed with status K), MPDO list helpers.
- e2e scenarios (`e2e/scenarios_test.go`) on the virtual bus and vcan;
  `TestMonkeyLSSBoot`; examples lss-assign, canopen-fd, network-boot.
- SocketCAN e2e run in the Lima VM `canvm` (vcan0 MTU 72): cross-compile
  `GOOS=linux GOARCH=arm64 go test -c -o bin/e2e.test ./e2e`, run from
  `e2e/` with `CANOPEN_REQUIRE_VCAN=1`.

## Status (2026-10-29): go-canopen v0.1.0 prepared

| Repo | Release | Notes |
|------|---------|-------|
| go-canopen | v0.1.0 (section in RELEASE.md; tag after merge of PR #1) | Phases 1–4: core, od, SDO (block), NMT/heartbeat/guarding, PDO, SYNC/EMCY/TIME, node, master, EDS/DCF, codegen, CiA 402, dissect. Coverage 97.4 % (every package ≥ 93 %). Wireshark oracle in CI; python canopen EDS cross-check local only. No interop results yet |
| canopen-interop | v0.1.0 | CANopenNode v4.1, Lely 2.3.5, python canopen 2.4.1 behind one contract; self-test of the references against each other |
| canopenctl | in progress | CLI on go-canopen |

## Interop suite (2026-10-29, v0.2.0 section in RELEASE.md)

`interop/` against canopen-interop v0.1.0 (pinned by digest), Interop
workflow on amd64/arm64; matrix and known differences in
INTEROPERABILITY.md. Found and fixed: SDO block transfers of empty data
ending without a segment (Lely server, python canopen client). Run
locally in the Lima VM `canvm`: cross-compile `bin/interop.test`
(GOOS=linux GOARCH=arm64, -tags interop) and run it from `interop/` with
`sg docker`.

## Next

1. Interop of store/restore, program download, TIME and node guarding
   (needs canopen-interop operations); CI results of contract 1.1.
2. More profiles (CiA 401, 406), SDO server channels; a hardware bench
   with a CAN FD adapter. See ROADMAP.md.

## Decisions

- Every service removes the dictionary hooks and watchers it installed on
  Close (`od` hooks return remove functions; `pdo.Protect` returns
  `unprotect`), so a dictionary can serve successive nodes;
  `od.Dictionary.HookCount` is the diagnostic and `TestRecreateNodeHooks`
  the guard.
- A node's SDO server is created enabled just before the boot-up message:
  a master that answers a boot-up with an immediate SDO request (the boot
  procedure) found it still disabled under load (CI flake of
  `TestBootProcedure`, race build).
- The heartbeat consumer restarts its supervision at a reset
  communication (`HeartbeatConsumer.Restart`, called by the node).
- `master.Configure` releases EMCY/SYNC/TIME COB-IDs in use before a
  CAN-ID change (devices refuse it, CiA 301); found applying a DCF with an
  absolute EMCY COB-ID.
- EDS: an NrOfRXPDO/NrOfTXPDO count mismatch is a warning (Lely and python
  canopen accept such files).
- PDO reconfiguration rules mirror CANopenNode v4 (CO_PDO.c), with the
  deviations in the pdo package documentation.
- Package `sync` is named `syncobj` so it does not shadow the standard
  library's `sync`.
- Lint configuration `.golangci.yml` as go-canbus (errcheck, govet,
  ineffassign, staticcheck, misspell with the CiA 306 spelling
  "Comissioning" allowed, godot, nilerr, exhaustive); errcheck excluded
  for tests and examples.
- CANopen FD nodes serve the USDO only (no classical SDO server): both
  answer on 580h + node-ID. The master switches all its procedures to
  USDO with `WithFD` rather than exposing a second set of APIs.
- LSS Fastscan in tests runs with known vendor, product and revision
  (only the serial number is scanned) and windows of 10–20 ms: 5 ms
  missed late answers under the race detector with coverage.
- Timing-based tests use margins of many periods (the heartbeat chaos
  test supervises 10 ms heartbeats with 250 ms): an 80 ms margin gave
  false timeouts with the whole module's tests running in parallel.
