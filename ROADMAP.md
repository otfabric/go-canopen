# Gaps and follow-ups

What go-canopen does not do yet, what is not verified, and the follow-ups
worth doing, as of v0.1.0 (2026-10-29). [LIMITATIONS.md](LIMITATIONS.md)
lists every limitation in detail; this page groups them into decisions.
Effort: **S** about a day, **M** a few days, **L** a week or more.

## Where things stand

| Area | State | Verified against |
|------|-------|------------------|
| Object dictionary, data types | Complete for CiA 301 basic types | Unit and fuzz tests; EDS fixtures of CANopenNode and python canopen |
| SDO client and server | Expedited, segmented, block (CRC, protocol switch), retries | Unit, fuzz and chaos tests (lost, repeated, delayed, reordered frames); Wireshark dissector for the frames |
| NMT, heartbeat, node guarding | Complete, master and slave | Unit tests, chaos tests, e2e on vcan |
| PDO | All transmission types, RTR, event timer, inhibit, deadline monitoring, reconfiguration rules, remote configuration | Unit tests; rules mirror CANopenNode v4 |
| SYNC, EMCY, TIME | Producers and consumers | Unit tests, Wireshark dissector |
| node, master | Complete device, network manager with boot procedure and DCF configuration | e2e plant (I/O modules, drive) on the virtual bus and vcan; chaos monkey |
| EDS/DCF | Parse, validate, write (round trip), dictionaries | python canopen cross-check (local only), third-party EDS files |
| CiA 402 | State machine, PP/PV/PT/HM/CSP/CSV/CST, simulated drive, controller | Unit tests, e2e |

## Gaps in function

| # | Gap | Impact | Follow-up | Effort |
|---|-----|--------|-----------|--------|
| 1 | No CANopen FD (CiA 1301): USDO, CAN FD PDOs (up to 64 bytes) | CANopen FD devices cannot be served or managed | go-canbus carries CAN FD already; add USDO (client and server), FD PDO lengths, the CiA 1301 object changes | L |
| 2 | No LSS (CiA 305) | Node-IDs and bit rates of unconfigured devices cannot be set over the bus | LSS master (switch, configure node-ID and bit timing, store, inquire, fastscan) and slave (`node.SetNodeID` is the hook already) | M |
| 3 | CiA 302 manager features: no flying master, configuration manager with stored concise DCFs, SDO manager, NMT requests | Multi-master and self-configuring networks are not supported | Configuration manager first (0x1F22 concise DCF, 0x1F20 store), then SDO manager | L |
| 4 | No store/restore (0x1010, 0x1011) | Configurations do not survive a power cycle | A pluggable store (file, callback) behind the CiA 301 signatures ("save", "load") | S–M |
| 5 | No MPDO (SAM/DAM, scanner/dispatcher lists) | Devices that rely on MPDOs cannot be used | MPDO producer/consumer in `pdo` | M |
| 6 | SYNC window (0x1007) not enforced | Synchronous PDOs may go out after the window | Cancel pending synchronous TPDOs and discard RPDOs at the window end | S |
| 7 | One SDO server channel; SDO client parameters (0x1280..) unused | No device-to-device SDO from the dictionary | Serve 0x1201..; create clients from 0x1280.. | S–M |
| 8 | Device profiles other than CiA 402 | Applications define CiA 401 I/O, CiA 406 encoders etc. themselves | Profile packages like `cia402`: CiA 401 first, then 406 | M each |
| 9 | Modular-device EDS sections kept, not interpreted | Modular devices need hand-built dictionaries | Assemble a dictionary from `[SupportedModules]`/`[ConnectedModules]` | M |
| 10 | 0x1028 (emergency consumer) and the rest of 0x1029 not interpreted | Configured via options instead | Read them in `node` | S |

## Gaps in verification

| # | Gap | Why it matters | Follow-up |
|---|-----|----------------|-----------|
| V1 | The interop suite (v0.2.0) covers what canopen-interop's contract 1.0 exposes: no LSS, TIME, node guarding, SRDO, MPDO; RPDOs only towards the stacks' devices; CiA 402 only with python canopen's client | Those services are checked against go-canopen itself and Wireshark only | Extend canopen-interop's contract (TIME, guarding, RPDO sender, LSS when implemented) and add the scenarios |
| V2 | The python canopen dictionary cross-check (`eds/crosscheck_test.go`) is skipped in CI | Names and evaluated defaults at another node-ID are compared only locally; the interop suite's EDS scenarios compare the rest in CI | Install python canopen in a CI job and set a require flag |
| V3 | Nothing on real hardware | vcan has no arbitration, timing, error frames or load; SYNC jitter, heartbeat margins and inhibit times under load are unmeasured | A bench: two USB adapters on SocketCAN, a commercial CANopen I/O module and a drive, a self-hosted runner |
| V4 | No CiA conformance test | Conformance is not claimed | Only with CiA membership and its test tool |
| V5 | Timing is exercised, not measured (SYNC period, inhibit, event timer) | A timing regression could pass | Measure frame timestamps on a vcan monitor socket in the e2e suite |

## Suggested order

1. **Interop suite**: done in v0.2.0 (V1 lists what it does not cover).
2. **CANopen FD / USDO** (gap 1, the next phase): go-canbus already
   carries CAN FD; it is the largest functional gap.
3. **LSS** (gap 2): needed by most real commissioning flows.
4. **CiA 302 manager** (gap 3) with **store/restore** (gap 4).
5. **More profiles**: CiA 401, 406, and others on demand (gap 8).
6. **Hardware bench** (V3), as soon as hardware is available, in
   parallel with the above.

Smaller items (gaps 5, 6, 7, 9, 10) on demand.
