# otfabric.io integration proposal

A proposal for adding go-canopen to otfabric.io (`otfabric/website`).
Nothing here has been applied to the website; the website is maintained
separately. Current as of go-canopen v0.1.0 and canopen-interop v0.1.0
(2026-10-29).

## Positioning

| Topic | Place on the site | Notes |
|-------|-------------------|-------|
| CANopen (CiA 301) | Industrial fieldbus application protocols, next to Modbus and PROFINET, on top of the CAN page | An application layer and communication profile on CAN: object dictionary, SDO, PDO, NMT, heartbeat, SYNC, EMCY, TIME |
| CiA 402 | Device profiles, under CANopen | The drive profile (state machine, modes of operation); also the basis of the EtherCAT CoE drive profile and IEC 61800-7-201 |
| EDS / DCF (CiA 306) | Information models and device descriptions, next to DBC, GSDML and IEC 61850 SCL | Device description and configuration files. **Not network protocols.** |
| CANopen FD, LSS, CiA 302 | Roadmap | Not implemented (see ROADMAP.md) |

## Repository description and topics

| Repository | Description | Topics |
|------------|-------------|--------|
| go-canopen | Pure Go CANopen (CiA 301) on go-canbus: object dictionary, SDO with block transfer, NMT, heartbeat, PDO, SYNC, EMCY, TIME, a complete device and a network manager, EDS/DCF, Go code generation, CiA 402 drives, a frame dissector | `canopen`, `cia301`, `cia402`, `can-bus`, `canbus`, `socketcan`, `sdo`, `pdo`, `eds`, `dcf`, `fieldbus`, `motion-control`, `code-generation`, `golang`, `industrial-automation`, `ot-security`, `otfabric` |

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
  "description": "CANopen (CiA 301) in pure Go on go-canbus: object dictionary with access rules and hooks, SDO client and server with expedited, segmented and block transfer, NMT master and slave, heartbeat and node guarding, PDOs with every transmission type, SYNC, EMCY and TIME; a complete device built from an object dictionary and a network manager with boot procedure and DCF configuration; EDS/DCF parsing, validation and writing; typed Go generated from an EDS; the CiA 402 drive profile; a dissector checked against Wireshark.",
  "icon": "go-canopen"
}
```

## Proposed `ecosystem.json` protocol entry

```json
{ "id": "canopen", "name": "CANopen", "standard": "CiA 301 (EN 50325-4); CiA 306 EDS/DCF; CiA 402 drives",
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
and the CiA 301 reconfiguration rules; SYNC, EMCY and TIME; a complete
device that picks its services from its dictionary and follows the NMT
states; a network manager with scan, boot procedure, DCF configuration,
remote PDOs and an ordered event stream; EDS/DCF read, validate, write;
Go code generation; CiA 402 state machine, a simulated drive and a
controller; a dissector for captured traffic. Runs on Linux SocketCAN,
slcan USB adapters and an in-process virtual bus.

**How it is tested**: race detector, fuzzing (13 targets), chaos tests
through a fault-injecting CAN bridge (lost, duplicated, delayed,
reordered, corrupted frames) and a nightly chaos monkey, an end-to-end
plant (master, I/O modules, a drive) on the virtual bus and Linux vcan,
Wireshark's CANopen dissector as an oracle, EDS files from CANopenNode
and python canopen; coverage 97 % (gate 90 %). Interoperability tests
against CANopenNode, Lely CANopen and python canopen (canopen-interop)
are planned: **state no interop results until the suite has run.**

**Limitations to state**: no CiA conformance claim (the specifications
are member documents); no CANopen FD/USDO, LSS, CiA 302 manager features
(flying master, configuration and SDO manager), MPDO or store/restore of
parameters; one SDO server channel; segmented SDO relies on the CAN CRC;
nothing tested on real CAN hardware; vcan proves message encoding and
protocol logic, not timing or electrical behaviour.

**Roadmap**: interoperability suite, CANopen FD (USDO), LSS, a CiA 302
configuration manager, more device profiles (CiA 401, 406), a hardware
bench. Details: [ROADMAP.md](../ROADMAP.md).
