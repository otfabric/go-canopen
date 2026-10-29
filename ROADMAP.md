# Gaps and follow-ups

What go-canopen does not do yet, what is not verified, and the follow-ups
worth doing, as of v0.3.0 (2026-10-29). [LIMITATIONS.md](LIMITATIONS.md)
lists every limitation in detail; this page groups them into decisions.
Effort: **S** about a day, **M** a few days, **L** a week or more.

## Where things stand

| Area | State | Verified against |
|------|-------|------------------|
| Object dictionary, data types | Complete for CiA 301 basic types | Unit and fuzz tests; EDS fixtures of CANopenNode and python canopen; interop EDS scenarios |
| SDO client and server | Expedited, segmented, block (CRC, protocol switch), retries | Unit, fuzz and chaos tests; Wireshark; CANopenNode, Lely, python canopen (interop) |
| NMT, heartbeat, node guarding | Complete, master and slave; unconfigured slaves (LSS) | Unit tests, chaos tests, e2e on vcan; interop (NMT, heartbeat) |
| PDO | All transmission types, RTR, event timer, inhibit, deadline monitoring, reconfiguration rules, remote configuration, SYNC window, CANopen FD lengths | Unit tests; interop (classical PDOs); e2e |
| MPDO | DAM, SAM, scanner and dispatcher lists, created by `node` | Unit, fuzz and chaos tests, e2e; no interop yet |
| SYNC, EMCY, TIME | Producers and consumers, FD variants of SYNC and EMCY | Unit tests, Wireshark (classical), interop (SYNC, EMCY) |
| LSS | Master (Fastscan, AssignAll) and slave; `node.WithLSS`; LSS step in the CiA 302 boot-up | Unit, fuzz and chaos tests, e2e, chaos monkey; no interop yet |
| CANopen FD | USDO client and server, FD PDOs, FD EMCY, FD NMT/SYNC, `node.WithFD`, `master.WithFD` | Unit, fuzz and chaos tests (CAN FD chaos bridge), e2e on the virtual bus and vcan (MTU 72); no independent oracle exists yet |
| node, master | Complete device; network manager with boot procedure, DCF configuration, CiA 302 network boot-up, concise DCF, program download, store/restore | e2e (plant, LSS boot-up, MPDO, FD, SYNC window, store/restore, program download) on the virtual bus and vcan; chaos monkeys |
| EDS/DCF | Parse, validate, write (round trip), dictionaries, concise DCF | python canopen cross-check (local), interop checkers (Lely, python canopen), third-party EDS files |
| CiA 402 | State machine, PP/PV/PT/HM/CSP/CSV/CST, simulated drive, controller | Unit tests, e2e, python canopen's BaseNode402 (interop) |

## Gaps in function

| # | Gap | Impact | Follow-up | Effort |
|---|-----|--------|-----------|--------|
| 1 | CANopen FD: remote (routed) USDO, multi-object transfers, the CiA 1301 dictionary changes beyond PDO lengths | Routed networks and later CiA 1301 editions | When the layouts are public (or with CiA membership) | M |
| 2 | CiA 302: flying master and redundancy, SDO manager (CiA 302-5), DCF storage 0x1F20, program objects 0x1F52..0x1F54 | Multi-master networks and dynamic SDO connections are not supported | Needs sources beyond what is public (CiA 302 is a member document) | L |
| 3 | One SDO server channel; SDO client parameters (0x1280..) unused | No device-to-device SDO from the dictionary | Serve 0x1201..; create clients from 0x1280.. (also what an SDO manager hands out) | S–M |
| 4 | Device profiles other than CiA 402 | Applications define CiA 401 I/O, CiA 406 encoders etc. themselves | Profile packages like `cia402`: CiA 401 first, then 406 | M each |
| 5 | Modular-device EDS sections kept, not interpreted | Modular devices need hand-built dictionaries | Assemble a dictionary from `[SupportedModules]`/`[ConnectedModules]` | M |
| 6 | 0x1028 (emergency consumer) and the rest of 0x1029 not interpreted | Configured via options instead | Read them in `node` | S |
| 7 | LSS "slowscan" (identify remote slave binary search) | Fastscan covers the use; some masters use slowscan | Add to `lss.Master` | S |
| 8 | MPDOs in CAN FD frames on a CANopen FD network | MPDOs stay classical | If CiA 1301 sources show an FD MPDO | S |

## Gaps in verification

| # | Gap | Why it matters | Follow-up |
|---|-----|----------------|-----------|
| V1 | The interop suite covers canopen-interop's contract 1.0: no LSS, MPDO, CiA 302 boot-up, store/restore, program download, TIME, node guarding, SRDO; RPDOs only towards the stacks' devices; CiA 402 only with python canopen's client | The v0.3.0 features are checked against go-canopen itself and Wireshark only | canopen-interop contract 1.1 with LSS, MPDO, store/restore and NMT master boot-up operations, then the scenarios |
| V2 | No independent CANopen FD implementation to test against | The USDO segmented layouts are inferred and the FD EMCY layout is ambiguous; a mismatch with real devices would go unnoticed | A CANopen FD stack or device when one becomes available (CANopenNode FD, a commercial device, a public trace) |
| V3 | The python canopen dictionary cross-check (`eds/crosscheck_test.go`) is skipped in CI | Names and evaluated defaults at another node-ID are compared only locally | Install python canopen in a CI job and set a require flag |
| V4 | Nothing on real hardware | vcan has no arbitration, timing, error frames or load; SYNC jitter (and the SYNC window), heartbeat margins and inhibit times under load are unmeasured; LSS bit timing switching is untested | A bench: two USB adapters on SocketCAN (one CAN FD), a commercial CANopen I/O module and a drive, a self-hosted runner |
| V5 | No CiA conformance test | Conformance is not claimed | Only with CiA membership and its test tool |
| V6 | Timing is exercised, not measured (SYNC period and window, inhibit, event timer) | A timing regression could pass | Measure frame timestamps on a vcan monitor socket in the e2e suite |

## Suggested order

1. **Interop for the v0.3.0 features** (V1): canopen-interop contract
   1.1 and its scenarios.
2. **Hardware bench** (V4), including a CAN FD adapter, as soon as
   hardware is available.
3. **More profiles**: CiA 401, 406, and others on demand (gap 4).
4. **SDO server channels and client parameters** (gap 3).
5. CiA 302 remainder and CANopen FD remainder (gaps 1, 2) when sources
   allow.

Smaller items (gaps 5–8) on demand.
