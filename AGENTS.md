# AGENTS.md — go-canopen

## What this is

Pure Go CANopen library (CiA 301 application layer and communication profile, CiA 305 LSS, CiA 1301 CANopen FD, CiA 302 network management, CiA 306 EDS/DCF, CiA 402 drive profile) layered on [go-canbus](https://github.com/otfabric/go-canbus). Every service runs on a `canbus.Bus`: SocketCAN, slcan or the in-process virtual bus. No cgo. Profile: **protocol-library**. Nothing is CiA-certified; independently verified features and their peers are listed in INTEROPERABILITY.md.

## Organisation standards

This repository follows **otfabric/engineering policy 1.1**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `enforce`). Organisation skills: `otfabric` Claude Code plugin. Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| Wire behaviour | CiA 301 / 305 / 306 / 302 / 1301 / 402 as cited in package docs; interpretations of incomplete public sources in LIMITATIONS.md "Interpretations" |
| What is verified against independent stacks | INTEROPERABILITY.md (canopen-interop images pinned by digest in `interop/harness_test.go`) |
| Scope not implemented, known behaviour | LIMITATIONS.md |
| Test levels, coverage policy and exclusions | TESTING.md |
| Errors | ERRORS.md (1:1 with exported `Err*`) |
| Release history | RELEASE.md (`## vX.Y.Z` … `---`), CHANGELOG.md |
| Work in progress | PROGRESS.md, ROADMAP.md |

## Hard rules

1. Never lower `COVER_MIN` (90), extend the coverage exclusions in TESTING.md, add lint suppressions, or skip/delete tests to make CI pass.
2. `*_REQUIRE_*` variables (`CANOPEN_REQUIRE_VCAN`, `CANOPEN_REQUIRE_TSHARK`, `CANOPEN_REQUIRE_INTEROP`) turn a missing **prerequisite** into a failure; capability skips (a stack without FD or 0x1010) stay skips.
3. Never take `lcMu` inside `mu` in `master` (Start → boot → goBoot deadlock).
4. A node enables its SDO server before boot-up, fences it at NMT reset and opens it at boot-up (`sdo.WithResetFence`, `WithFenced`, `nmt.WithBootUpHandler`).
5. Interop claims only from passing scenarios against the pinned canopen-interop release.
6. No new runtime dependency beyond go-canbus without an ADR.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| SocketCAN-facing code (Linux) | `make vcan-setup` then `make test-vcan` |
| fuzzed decoders | `make fuzz FUZZTIME=30s` |
| node/master/sdo state machines | `make chaos` |
| interop-relevant behaviour | `make interop` (Linux, Docker, vcan0) |
| the code generator | `make generate generate-check` |

## Definition of Done

`make check` green; tests for new behaviour incl. error paths; fuzz target for new decoders; ERRORS.md / LIMITATIONS.md / INTEROPERABILITY.md updated in the same change; RELEASE.md section for user-visible changes.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session. Never chain a merge and a release; confirm the PR is merged and `main` contains it first.
