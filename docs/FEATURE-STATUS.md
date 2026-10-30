# Support and feature matrix

What go-canopen supports, how each feature is tested, and against what. Status: v0.3.1; the latest recorded interop results are canopen-interop v0.2.0 (contract 1.1) against go-canopen v0.3.0, run locally. Per-adapter counts, known differences and skips are in [INTEROPERABILITY.md](../INTEROPERABILITY.md); service details in the [README](../README.md#cia-301-services); what is out of scope in [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: there is no CiA conformance claim, no conformance test run and no certification.

## 1. Versions

| go-canopen | Go | canopen-interop (contract) | Supported |
|---|---|---|---|
| **v0.3.1** | 1.23 or later (CI: 1.23 to 1.26) | v0.2.0 (1.1) | **yes** |

## 2. Platforms

| Platform | Tested in CI |
|---|---|
| Linux amd64, arm64 (`ubuntu-24.04`, `ubuntu-24.04-arm`) | the test suite; Linux vcan; Interop |
| macOS, Windows | `go test ./...` on `macos-latest`, `windows-latest` |

## 3. Feature support

✅ supported · ◐ partly · — not exercised by that peer. **Independent**: in the canopen-interop matrix, per [INTEROPERABILITY.md](../INTEROPERABILITY.md#interop-matrix), with the known differences recorded there. All of it runs on vcan.

| Feature | Status | CANopenNode | Lely CANopen | python canopen |
|---|:---:|:---:|:---:|:---:|
| SDO expedited, segmented, block | ✅ | ✅ | ✅ | ✅ (its device has no block transfer) |
| NMT, heartbeat | ✅ | ✅ | ✅ | ✅ |
| PDO | ✅ | ✅ | ✅ | ✅ (its device has no PDOs) |
| SYNC, EMCY | ✅ | ✅ | ✅ | ✅ |
| SYNC window (0x1007) | ✅ | ✅ | ✅ | ✅ `rpdo-send` |
| MPDO DAM and SAM | ✅ | — no MPDO | ✅ | — no MPDO |
| LSS (CiA 305) | ✅ | ✅ | ✅ | ◐ its master against a go-canopen node; no LSS slave |
| CiA 302 boot-up | ✅ | ✅ | ✅ | ✅ (no LSS slave) |
| Concise DCF (`cdcf`) | ✅ | — | ✅ byte-exact | — |
| EDS, DCF (CiA 306) | ✅ | ◐ DCF applied; no EDS checker | ✅ dcf-tools (known: 5 files) | ✅ `import_od` (known: 7) |
| CiA 402 drives | ✅ | — no client | — no client | ✅ BaseNode402 operates a go-canopen drive |

No independent oracle, tested against go-canopen itself only:

| Feature | Why |
|---|---|
| CANopen FD (CiA 1301): USDO, FD PDOs, FD emergencies | no reference stack implements it |
| Store and restore parameters (0x1010, 0x1011) | contract 1.1 has no operation for it |
| Program download of CiA 302-3 | contract 1.1 has no operation for it |

Other references, outside the matrix: Wireshark's CANopen dissector (`dissect/wireshark_test.go`) and python canopen's EDS reader (`eds/crosscheck_test.go`).
