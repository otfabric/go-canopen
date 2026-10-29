# otfabric.io integration proposal

A proposal for adding go-canopen to otfabric.io (`otfabric/website`).
Nothing here has been applied to the website; the website is maintained
separately. Current as of go-canopen v0.3.0 and canopen-interop v0.1.0
(2026-10-29).

## Positioning

| Topic | Place on the site | Notes |
|-------|-------------------|-------|
| CANopen (CiA 301) | Industrial fieldbus application protocols, next to Modbus and PROFINET, on top of the CAN page | An application layer and communication profile on CAN: object dictionary, SDO, PDO, NMT, heartbeat, SYNC, EMCY, TIME |
| CiA 402 | Device profiles, under CANopen | The drive profile (state machine, modes of operation); also the basis of the EtherCAT CoE drive profile and IEC 61800-7-201 |
| EDS / DCF (CiA 306) | Information models and device descriptions, next to DBC, GSDML and IEC 61850 SCL | Device description and configuration files. **Not network protocols.** |
| CANopen FD (CiA 1301) | Under CANopen, as its CAN FD successor | USDO, 64-byte PDOs, FD emergencies; implemented from public sources, interoperability not yet tested (no reference stack) |
| LSS (CiA 305), CiA 302 network management | Under CANopen | Node-ID assignment (Fastscan), network boot-up, concise DCF configuration, program download |

## Repository description and topics

| Repository | Description | Topics |
|------------|-------------|--------|
| go-canopen | Pure Go CANopen (CiA 301) and CANopen FD (CiA 1301) on go-canbus: object dictionary, SDO/USDO, NMT, heartbeat, PDO and MPDO, SYNC, EMCY, TIME, LSS, a complete device and a CiA 302 network manager, EDS/DCF, Go code generation, CiA 402 drives, a frame dissector | `canopen`, `canopen-fd`, `cia301`, `cia302`, `cia305`, `cia402`, `lss`, `can-bus`, `can-fd`, `canbus`, `socketcan`, `sdo`, `pdo`, `eds`, `dcf`, `fieldbus`, `motion-control`, `code-generation`, `golang`, `industrial-automation`, `ot-security`, `otfabric` |

## Proposed `src/data/projects.json` entry

```json
{
  "id": "go-canopen",
  "name": "go-canopen",
  "repo": "go-canopen",
  "category": "library",
  "protocols": ["canopen", "can"],
  "status": "available",
  "install": "go get github.com/otfabric/go-canopen",
  "description": "CANopen (CiA 301) and CANopen FD (CiA 1301) in pure Go on go-canbus: object dictionary with access rules and hooks, SDO client and server with expedited, segmented and block transfer, USDO, NMT master and slave, heartbeat and node guarding, PDOs and MPDOs, SYNC with the synchronous window, EMCY and TIME, LSS (CiA 305), store and restore; a complete device built from an object dictionary and a CiA 302 network manager (network boot-up, concise DCF configuration, program download, LSS assignment); EDS/DCF parsing, validation and writing; typed Go generated from an EDS; the CiA 402 drive profile; a dissector checked against Wireshark.",
  "icon": "go-canopen"
}
```

## Proposed `ecosystem.json` protocol entry

```json
{ "id": "canopen", "name": "CANopen", "standard": "CiA 301 (EN 50325-4); CiA 302, 305, 306 EDS/DCF; CiA 402 drives; CiA 1301 CANopen FD",
  "focus": "Application layer on CAN: object dictionary, SDO, PDO, NMT, error control", "maturity": "Very mature", "commercial": "Very established",
  "summary": "Widespread in machines, drives, medical and mobile equipment; open-source stacks include CANopenNode (C), Lely CANopen (C/C++) and python canopen. CANopen FD (CiA 1301) extends it to CAN FD." }
```

## Feature summary for a protocol page

**What it does**: the CiA 301 object dictionary (all basic data types,
access rights, limits, node-ID-relative defaults); SDO client and server
with expedited, segmented and block transfer (CRC, protocol switch,
retries); NMT module control with both resets, heartbeat producer and
consumer, node and life guarding; PDOs with synchronous, RTR and
event-driven transmission, inhibit time, event timer, deadline monitoring
and the CiA 301 reconfiguration rules; multiplexed PDOs (DAM, SAM,
scanner and dispatcher lists); SYNC with the synchronous window, EMCY
and TIME; LSS master and slave with Fastscan and automatic node-ID
assignment; store and restore of parameters; CANopen FD with the USDO,
64-byte PDOs and 20-byte emergencies; a complete device that picks its
services from its dictionary and follows the NMT states; a network
manager with scan, boot procedure, DCF configuration, the CiA 302
network boot-up (with an LSS step), concise DCF configuration, program
download, remote PDOs and an ordered event stream; EDS/DCF read, validate, write;
Go code generation; CiA 402 state machine, a simulated drive and a
controller; a dissector for captured traffic. Runs on Linux SocketCAN,
slcan USB adapters and an in-process virtual bus.

**How it is tested**: race detector, fuzzing (25 targets), chaos tests
through a fault-injecting CAN bridge (lost, duplicated, delayed,
reordered, corrupted frames, classical and CAN FD) and a nightly chaos
monkey (including LSS assignment and the CiA 302 boot-up), end-to-end
scenarios (a plant with I/O modules and a drive, LSS plus network
boot-up, MPDOs, a CANopen FD network, the SYNC window, store/restore
across restarts, program download) on the virtual bus and Linux vcan,
Wireshark's CANopen dissector as an oracle, EDS files from CANopenNode
and python canopen; coverage 97 % (gate 90 %, every package ≥ 90 %).
Interoperability against CANopenNode, Lely CANopen and python canopen
(canopen-interop) for the CiA 301 services since v0.2.0; **the v0.3.0
features (LSS, MPDO, CANopen FD, CiA 302 boot-up, store/restore) have no
interop results yet** (pending canopen-interop contract 1.1; no
reference stack implements CANopen FD).

**Limitations to state**: no CiA conformance claim (the specifications
are member documents); CANopen FD from public sources only (segmented
USDO layouts inferred, FD emergency layout selectable because sources
conflict, no remote USDO); no flying master or SDO manager (CiA 302-5);
one SDO server channel; segmented SDO relies on the CAN CRC; nothing
tested on real CAN hardware; vcan proves message encoding and protocol
logic, not timing or electrical behaviour.

**Roadmap**: interop for the v0.3.0 features (canopen-interop contract
1.1), more device profiles (CiA 401, 406), the remaining CiA 302 objects,
a hardware bench. Details: [ROADMAP.md](../ROADMAP.md).
