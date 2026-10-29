# Interoperability

go-canopen is meant to be tested against independent CANopen
implementations, through the containers of
[otfabric/canopen-interop](https://github.com/otfabric/canopen-interop)
(their command-line contract only: no code of those stacks is linked or
vendored). This page describes that suite, its results and the other
independent references. A skipped or simulated check is never reported
as a pass.

## Independent references in use today

| Reference | Licence | Where | Checks | Runs |
|-----------|---------|-------|--------|------|
| Wireshark's CANopen dissector (tshark; 4.6.9 locally, the Wireshark stable PPA in CI) | GPL-2.0 | `dissect/wireshark_test.go` (`TestWireshark`) | Frames of every service (NMT, SYNC, TIME, EMCY, PDO, SDO including aborts, heartbeat and boot-up, node guarding, LSS) are written to a pcap with go-canbus, dissected by tshark (`-d can.subdissector,canopen`), and its fields (function code, node-ID, NMT command, SYNC counter, EMCY code and register, SDO specifiers, index, sub-index and abort code, TIME, guarding state and toggle, LSS command, PDO data) are compared with `dissect.Decode` | Wherever tshark is installed; skipped without it, except with `CANOPEN_REQUIRE_TSHARK=1`, which CI's vcan jobs set: there a missing tshark fails the test |
| python canopen (christiansandberg/canopen) | MIT | `eds/crosscheck_test.go` (`TestCrossCheckPythonCanopen`) | The dictionaries built from the EDS/DCF fixtures (and python canopen's own sample EDS) compared with python canopen's import of the same files: data type, access type, PDO mappability, names, default values at node-ID 0x10 | Only where `EDS_PYCANOPEN` (or `python3`) can import canopen; otherwise it skips with a message. CI does not install python canopen, so **CI does not run this test**; the interop suite compares python canopen's import with go-canopen in CI (EDS scenarios below) |
| Third-party EDS files | Apache-2.0 (CANopenNode), MIT (python canopen) | `eds/testdata/third_party` (see its NOTICE) | CANopenNode's `DS301_profile.eds` and python canopen's `sample.eds` parse, validate, build dictionaries and round-trip; `codegen` generates from them | Every test run |

The pdo package's reconfiguration rules mirror CANopenNode v4's
`CO_PDO.c` (documented deviations in the package documentation), and the
SDO interpretations follow the open-source stacks' behaviour; that is
design input, not a test against them.

## The canopen-interop suite

`interop/` (build tag `interop`) runs go-canopen against the three stacks
of [canopen-interop](https://github.com/otfabric/canopen-interop) v0.1.0
(container contract 1.0), through their command-line contract only: no
code of those stacks is linked or vendored. The images are pulled by
release and multi-arch index digest (`interop/harness_test.go`); every
container runs with `--network host` on the Linux `vcan0` of the test
process.

| Image | Upstream | Language |
|-------|----------|----------|
| `canopennode` | CANopenNode v4 with CANopenLinux (`canopend`, ASCII gateway as master) | C |
| `lely` | Lely CANopen (lely-core) 2.3.5, dcf-tools for EDS checks | C/C++, Python |
| `pythoncanopen` | python canopen 2.4.1 on python-can | Python |

```sh
make interop          # Linux, Docker, vcan0; writes bin/interop-report.json
make interop-images   # build canopen-interop from source as :dev
CANOPEN_INTEROP_TAG=dev make interop
```

`CANOPEN_REQUIRE_INTEROP=1` (set by `make interop` and CI) turns a
missing Docker, image or interface into a failure.
`CANOPEN_INTEROP_REPORT` writes the matrix as JSON: one row per adapter,
direction and scenario, whose result is `pass`, `fail`, `skip` (a
capability the image does not declare, or an operation its contract
marks `unsupported`, with the reason) or `known-difference` (with the
reason, listed below). A skip or a known difference is never counted as
a pass. The Interop workflow runs the suite on GitHub's amd64 and arm64
runners and uploads the report.

### Scenarios

**go-canopen as master and client** (`go-canopen-master`) of each stack's
`device` (node 5, built from `interop-device.eds`; go-canopen reads the
same EDS with `eds.ParseFile` for the data types and defaults):

- SDO upload, normal (expedited or segmented, as the server chooses) and
  block (with CRC), of every readable sub-object of the EDS (0x1000 to
  0x200A), byte for byte and as typed values against the EDS defaults.
- SDO download, normal and block, of every writable test object
  (0x2000..0x200A, write-only 0x2009 included), read back.
- The DOMAIN 0x2005 with 0, 1, 4, 7, 8, 100, 889, 1024 and 4096 bytes,
  both ways, normal and block.
- Aborts: no object (upload and download), no sub-index (VAR and
  RECORD), read of write-only, write of read-only and const, lengths
  (too long, too short, segmented too long; the general 0x06070010 or
  the specific code), LowLimit/HighLimit of 0x200A and the limits
  themselves.
- `master.Scan` and the identity object.
- The values written, checked with the device's own `--dump` at its end.
- NMT: boot-up, heartbeat states seen by the master's heartbeat consumer
  after start, stop, pre-operational, start/pre-operational to all nodes,
  reset communication and reset node (boot-up each); heartbeat loss
  (`Master.Monitor`) when the device ends.
- PDO: TPDO1 read with `pdo.ReadConfig` and decoded with `pdo.Listener`
  (the device's `--tick` counter, increasing); TPDO2 remapped with
  `pdo.Configure` (three objects, event timer) and decoded; TPDO3
  configured synchronous and driven by the master's SYNC producer; RPDO1
  sent with `Master.RPDOSender` and checked by SDO and `--dump`.
- EMCY from `device --emcy`, decoded by `emcy.Consumer` and the master.
- `Master.ConfigureFromDCF` with `interop-device-node5.dcf`, checked by
  SDO and `--dump`.

**Each stack as master and client** (`go-canopen-device`) of a go-canopen
`node` (node 6, dictionary of `interop-device.eds`, in the test process
on go-canbus SocketCAN):

- `sdo-upload` (normal and `--block`) of 22 sub-objects, byte-exact
  against the node's dictionary; `sdo-download` (normal and `--block`)
  of every writable test object; the DOMAIN sizes above, both ways;
  aborts (no object, no sub-index, write-only, read-only, lengths,
  limits).
- `nmt` start, stop, pre-operational, reset communication (boot-up, the
  application area kept) and reset node (boot-up, defaults restored)
  against the node's state.
- `listen`: the node's boot-up, its heartbeat state operational and an
  EMCY it raises.
- `pdo-receive` of the node's TPDO1 (counter increasing),
  `pdo-configure` of TPDO2 (checked in the dictionary and on the bus),
  `sync`: python canopen's SYNC producer drives a synchronous TPDO3 (20
  SYNC, 20 TPDOs).
- `cia402` (python canopen `BaseNode402`) against a go-canopen node with
  a `cia402.Drive` (node 7): enable (OPERATION ENABLED), disable (SWITCH
  ON DISABLED), and fault reset after an injected fault (BaseNode402
  then goes on to OPERATION ENABLED). This is the only CiA 402 check
  against an independent implementation.

**EDS** (`eds`): each stack's `eds-check` against `eds.ParseFile` and
`File.Validate` on canopen-interop's five fixtures and go-canopen's seven
EDS/DCF test files: the verdict (valid or not), and for files both
accept the normalized object list (index, sub-index, data type, access,
PDO mapping, and the DefaultValue literal evaluated with
`canopen.ParseValue` against go-canopen's reading of the same
sub-object).

## Interop matrix

Results of canopen-interop v0.1.0 against go-canopen v0.2.0, per adapter
and direction: pass / known difference / skip / fail. CI is the Interop
workflow of pull request #2 (GitHub-hosted ubuntu-24.04 and
ubuntu-24.04-arm runners); its report is the workflow's artifact.

| Adapter | Direction | Local (Lima VM, arm64, Linux 7.0) | CI amd64 | CI arm64 |
|---------|-----------|------------------|----------|----------|
| canopennode | go-canopen-master | 340 / 21 / 2 / 0 | 340 / 21 / 2 / 0 | 340 / 21 / 2 / 0 |
| canopennode | go-canopen-device | 95 / 0 / 8 / 0 | 95 / 0 / 8 / 0 | 95 / 0 / 8 / 0 |
| canopennode | eds | 0 / 0 / 24 / 0 | 0 / 0 / 24 / 0 | 0 / 0 / 24 / 0 |
| lely | go-canopen-master | 347 / 16 / 0 / 0 | 347 / 16 / 0 / 0 | 347 / 16 / 0 / 0 |
| lely | go-canopen-device | 97 / 1 / 5 / 0 | 97 / 1 / 5 / 0 | 97 / 1 / 5 / 0 |
| lely | eds | 11 / 5 / 8 / 0 | 11 / 5 / 8 / 0 | 11 / 5 / 8 / 0 |
| pythoncanopen | go-canopen-master | 189 / 3 / 171 / 0 | 189 / 3 / 171 / 0 | 189 / 3 / 171 / 0 |
| pythoncanopen | go-canopen-device | 103 / 0 / 0 / 0 | 103 / 0 / 0 / 0 | 103 / 0 / 0 / 0 |
| pythoncanopen | eds | 11 / 7 / 6 / 0 | 11 / 7 / 6 / 0 | 11 / 7 / 6 / 0 |

By area:

| Reference | SDO | NMT / heartbeat | PDO | SYNC / EMCY | EDS / DCF | CiA 402 |
|-----------|-----|-----------------|-----|-------------|-----------|---------|
| CANopenNode | ✅ both directions (known: empty DOMAIN, 0x1003 entries) | ✅ both directions | ✅ TPDO1/2/3, RPDO1 of its device | ✅ SYNC to its TPDO, EMCY both directions | DCF applied ✅; no EDS checker | – (no client) |
| Lely CANopen | ✅ both directions (known: 0x1003 entries, 0-byte block download by its client) | ✅ both directions | ✅ device TPDO/RPDO; `pdo-receive` of ours | ✅ SYNC to its TPDO, EMCY both directions | ✅ dcf-tools (known: 5 files it rejects) | – (no client) |
| python canopen | ✅ both directions (its device has no block transfer) | ✅ both directions | ✅ `pdo-configure`, `pdo-receive` of ours (its device has no PDOs) | ✅ its SYNC drives our TPDO; EMCY both directions | ✅ import_od (known: 7) | ✅ BaseNode402 operates our drive |

### Known differences

| Direction | Adapter | Scenario | Reason |
|-----------|---------|----------|--------|
| master | canopennode, lely | `upload-0x1003:01..08` (normal and block) | Their SDO servers abort the upload of a pre-defined error field entry above the number of errors (0x1003:00) with 0x08000024 (no data available); the EDS default 0 is not readable. CiA 301 does not define the value of unused entries |
| master | canopennode | `upload-0x2005:00` (normal and block) | CANopenNode's SDO server cannot upload 0 bytes: the never-written DOMAIN is aborted with 0x06040047 (CO_SDOserver.c) |
| master | pythoncanopen | `upload-0x2005:00` | python canopen's LocalNode aborts the upload of a never-written DOMAIN with 0x060A0023 |
| master | canopennode | `domain-0`, `block-domain-0` | CANopenNode's SDO server refuses a 0-byte download (segmented 0x06070013, block 0x06040047) |
| master | pythoncanopen | `domain-0` | python canopen's LocalNode stores 0 bytes but aborts their upload with 0x08000024 |
| master | pythoncanopen | `abort-no-subindex` | python canopen's LocalNode ignores the sub-index of a VAR: 0x2000:01 returns 0x2000:00 instead of abort 0x06090011 |
| master | canopennode | `device-dump`, `configure-from-dcf` | CANopenNode keeps a VISIBLE_STRING in a fixed buffer: a shorter string is NUL-terminated and the adapter's `--dump` prints the whole buffer; the SDO upload returns the string itself (checked) |
| device | lely | `block-domain-0` | Lely's SDO client reports a 0-byte block download as done without sending a frame: our DOMAIN keeps its previous value |
| eds | lely | `valid-object-links.eds` | dcf-tools do not know the `[<index>ObjectLinks]` section |
| eds | lely | `valid-compact.eds` | dcf-tools reject the octal literal `0755` and the data type definition objects 0x0020/0x0040 |
| eds | lely | `valid-io401.eds`, `valid-node37.dcf` | dcf-tools reject `0x280+$NODEID` (number first) and the REAL32 LowLimit `0.0`; go-canopen and python canopen accept both |
| eds | lely, pythoncanopen | `valid-drive402.eds` | The file starts with a UTF-8 byte order mark, which Python's configparser rejects; go-canopen skips it |
| eds | pythoncanopen | `valid-compact.eds` | import_od does not support the data type definition objects 0x0020/0x0040 |
| eds | pythoncanopen | `valid-python_canopen_sample.eds` | import_od does not validate; go-canopen's `Validate` reports SubNumber and object-list count mismatches and the unknown data type 0x0040 in python canopen's own sample (Lely rejects it too) |
| eds | pythoncanopen | `valid-canopennode_ds301_profile.eds` | import_od fails on an empty integer DefaultValue (`int('')`); go-canopen reads it as 0, Lely accepts the file |
| eds | pythoncanopen | `objects-compact-subobj.eds` | CompactSubObj: python canopen creates sub 0 rw and only sub 1; go-canopen and Lely create sub 0 ro and 1..n |
| eds | pythoncanopen | `objects-io401.eds`, `objects-node37.dcf` | python canopen lists the `[DummyUsage]` data types 0x0002..0x0007 as objects; go-canopen's dictionary does not hold them (it accepts them as RPDO dummy entries) |

### Skips

| Adapter | Skipped | Reason |
|---------|---------|--------|
| canopennode | 2 limit aborts (master) | its device does not check LowLimit/HighLimit (`deviceLimits` false) |
| canopennode | `domain-0`, `block-domain-0` (device) | its gateway client cannot download 0 bytes (`unsupported`) |
| canopennode | `pdo-receive`, `pdo-configure`, `sync`, 3 × `cia402` (device) | operations it does not offer |
| canopennode | 24 EDS scenarios | no `eds-check` |
| lely | `pdo-configure`, `sync`, 3 × `cia402` (device) | operations it does not offer |
| lely, pythoncanopen | object lists of files a checker rejects (`objects-*`) | nothing to compare when one side judges the file invalid |
| pythoncanopen | 165 block transfers (master) | its device (LocalNode) has no SDO block transfer (`deviceBlock` false) |
| pythoncanopen | 2 limit aborts, TPDO1/2/3, RPDO1 (master) | its device checks no limits and has no PDOs (`deviceLimits`, `deviceTpdo`, `deviceRpdo` false) |

### Found by the suite

- **SDO block transfer of 0 bytes** (fixed in v0.2.0): Lely's SDO server
  answers the block upload of an empty object with the end message and no
  segment, and python canopen's client does the same in a block download
  of 0 bytes. go-canopen's client and server took that end message for an
  out-of-sequence segment and waited until the timeout (the client then
  repeated its start request, which Lely aborted with 0x05040001). Both
  now accept the end of a transfer before any segment; unit tests in
  `sdo` (`TestClientBlockUploadEmpty`, `TestMachine`).

## What vcan proves

All stacks share the Linux SocketCAN code on a vcan interface: such
tests show that each stack puts the same CANopen messages on the bus and
interprets the other's messages alike. vcan has no arbitration, bit
timing, error frames or bus load: timing behaviour (SYNC jitter, inhibit
times under load, heartbeat margins) and electrical behaviour are not
covered. Nothing has been tested on real CAN hardware
([LIMITATIONS.md](LIMITATIONS.md)).
