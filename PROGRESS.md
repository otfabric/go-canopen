# Progress

Durable state of the CANopen work (go-canopen, canopen-interop,
canopenctl). Newest first within each section.

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

1. Phase 5: CANopen FD / USDO (CiA 1301).
2. LSS (CiA 305), CiA 302 configuration manager with store/restore,
   more profiles (CiA 401, 406). See ROADMAP.md.

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
- Timing-based tests use margins of many periods (the heartbeat chaos
  test supervises 10 ms heartbeats with 250 ms): an 80 ms margin gave
  false timeouts with the whole module's tests running in parallel.
