# Interoperability

go-canopen is meant to be tested against independent CANopen
implementations, through the containers of
[otfabric/canopen-interop](https://github.com/otfabric/canopen-interop)
(their command-line contract only: no code of those stacks is linked or
vendored). That suite is **not written yet**; this page says what is
planned and what is checked against independent implementations today.
A skipped or simulated check is never reported as a pass.

## Independent references in use today

| Reference | Licence | Where | Checks | Runs |
|-----------|---------|-------|--------|------|
| Wireshark's CANopen dissector (tshark; 4.6.9 locally, the Wireshark stable PPA in CI) | GPL-2.0 | `dissect/wireshark_test.go` (`TestWireshark`) | Frames of every service (NMT, SYNC, TIME, EMCY, PDO, SDO including aborts, heartbeat and boot-up, node guarding, LSS) are written to a pcap with go-canbus, dissected by tshark (`-d can.subdissector,canopen`), and its fields (function code, node-ID, NMT command, SYNC counter, EMCY code and register, SDO specifiers, index, sub-index and abort code, TIME, guarding state and toggle, LSS command, PDO data) are compared with `dissect.Decode` | Wherever tshark is installed; skipped without it, except with `CANOPEN_REQUIRE_TSHARK=1`, which CI's vcan jobs set: there a missing tshark fails the test |
| python canopen (christiansandberg/canopen) | MIT | `eds/crosscheck_test.go` (`TestCrossCheckPythonCanopen`) | The dictionaries built from the EDS/DCF fixtures (and python canopen's own sample EDS) compared with python canopen's import of the same files: data type, access type, PDO mappability, names, default values at node-ID 0x10 | Only where `EDS_PYCANOPEN` (or `python3`) can import canopen; otherwise it skips with a message. CI does not install python canopen, so **CI does not run it** |
| Third-party EDS files | Apache-2.0 (CANopenNode), MIT (python canopen) | `eds/testdata/third_party` (see its NOTICE) | CANopenNode's `DS301_profile.eds` and python canopen's `sample.eds` parse, validate, build dictionaries and round-trip; `codegen` generates from them | Every test run |

The pdo package's reconfiguration rules mirror CANopenNode v4's
`CO_PDO.c` (documented deviations in the package documentation), and the
SDO interpretations follow the open-source stacks' behaviour; that is
design input, not a test against them.

## Planned: the canopen-interop suite

canopen-interop v0.1.0 publishes three stacks behind one contract
(device, SDO upload/download, NMT, listen, and per stack PDO, SYNC, EDS
check and CiA 402 operations), all on Linux `vcan`:

| Image | Upstream | Language | Planned role against go-canopen |
|-------|----------|----------|---------------------------------|
| `canopennode` | CANopenNode v4.1 with CANopenLinux (`canopend`) | C | device for the go-canopen master and SDO client (expedited, segmented, block); master (via its CiA 309-3 gateway) for a go-canopen node |
| `lely` | Lely CANopen (lely-core) v2.3.5 | C/C++ | device and master; RPDO reception of go-canopen TPDOs; `dcfchk` on EDS/DCF files go-canopen writes |
| `pythoncanopen` | python canopen 2.4.1 on python-can 4.6.1 | Python | device and master; PDO configuration and reception, SYNC, CiA 402 against the go-canopen drive model; EDS import of files go-canopen writes |

The suite will live in `interop/` (build tag `interop`), run with
`make interop` (Linux, Docker, `vcan0`; images pinned by digest) or
against images built from source with `make interop-images`, and write a
result matrix (`bin/interop-report.json`) in which every scenario is
`pass`, `fail`, `skip` (with the reason) or `known-difference` (with the
reason, listed here). The Makefile targets exist; the `interop/`
directory does not yet.

Scenarios to cover, both directions where the reference can play both
roles:

- SDO: every basic type, strings, DOMAIN; expedited, segmented, block
  (with and without CRC, protocol switch); aborts for missing objects,
  access rights, lengths and limits.
- NMT: commands to one node and to all, both resets, boot-up, heartbeat
  states observed by each side's consumer, node guarding where supported.
- PDO: event-driven, cyclic and acyclic synchronous TPDOs received by the
  references; references' TPDOs received by go-canopen RPDOs; remote PDO
  configuration in the CiA 301 order.
- SYNC, EMCY and TIME production and consumption.
- EDS/DCF: go-canopen's `eds.Write` output checked by Lely's `dcfchk`
  and imported by python canopen; the shared fixture
  `interop-device.eds` read by go-canopen.
- CiA 402: python canopen's `BaseNode402` operating the go-canopen drive.

## Interop matrix

**To be filled by the interop suite.** No go-canopen result against
CANopenNode, Lely CANopen or python canopen exists yet: none is claimed
here, in the README or in the release notes. canopen-interop's own
self-test (the references against each other) is reported in its
docs/COMPATIBILITY.md and says nothing about go-canopen.

| Reference | SDO | NMT / heartbeat | PDO | SYNC / EMCY / TIME | EDS / DCF | CiA 402 |
|-----------|-----|-----------------|-----|--------------------|-----------|---------|
| CANopenNode | — | — | — | — | — | — |
| Lely CANopen | — | — | — | — | — | — |
| python canopen | — | — | — | — | — | — |

## What vcan proves

All stacks would share the Linux SocketCAN code on a vcan interface: such
tests show that each stack puts the same CANopen messages on the bus and
interprets the other's messages alike. vcan has no arbitration, bit
timing, error frames or bus load: timing behaviour (SYNC jitter, inhibit
times under load, heartbeat margins) and electrical behaviour are not
covered. Nothing has been tested on real CAN hardware
([LIMITATIONS.md](LIMITATIONS.md)).
