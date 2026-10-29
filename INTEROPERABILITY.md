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
of [canopen-interop](https://github.com/otfabric/canopen-interop) v0.2.0
(container contract 1.1), through their command-line contract only: no
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
`File.Validate` on canopen-interop's seven fixtures and go-canopen's seven
EDS/DCF test files: the verdict (valid or not), and for files both
accept the normalized object list (index, sub-index, data type, access,
PDO mapping, and the DefaultValue literal evaluated with
`canopen.ParseValue` against go-canopen's reading of the same
sub-object).

**Contract 1.1** (canopen-interop v0.2.0), both directions where the
stacks allow:

- **LSS (CiA 305)**, go-canopen's LSS master (`Master.LSS`) against the
  LSS slaves of CANopenNode and Lely (`device --node-id 255 --serial`):
  identify non-configured and remote slave, switch state selective (and
  an absent address: timeout), inquire identity and node-ID, configure
  and activate bit timing (the device reports 250 kbit/s after 50 ms),
  configure node-ID (active after switch to waiting, or after NMT reset
  communication; the node then answers SDO with its serial number),
  store, `Fastscan` (the whole address), `FastscanSelect` with a node-ID,
  a configured device that must not answer Fastscan, and `AssignAll` of
  two unconfigured devices (node-IDs 126 and 127, stored). The LSS
  masters of all three stacks (`lss`) against go-canopen LSS nodes
  (`node.WithLSS`, node-ID 0xFF, store in memory): inquire, switch
  selective + configure node-ID (boot-up with the new node-ID), store,
  bit timing (the node's handlers see 250 kbit/s and a 50 ms delay),
  fastscan with node-ID, fastscan with only a configured node (no
  answer), and CANopenNode's `assign-all` of two nodes.
- **MPDO**, with Lely (`mpdo-send`, `mpdo-receive`): DAM (unicast, another
  node's, broadcast) and SAM (two dispatched objects, an unlisted
  producer and an unlisted object) in both directions; go-canopen's PDOs
  are made MPDOs by `Master.ConfigureMPDO` and its lists written by
  `Master.WriteScannerList` / `WriteDispatcherList` over SDO.
- **CiA 302 boot-up**: go-canopen's manager (`WithDictionary`,
  `BootNetwork`) boots each stack's device from the network list with a
  concise DCF (0x2001, heartbeat 200 ms supervised by 0x1016), checks it
  operational; a device whose product code differs from the expected one
  (`interop-device-other-product.eds`) ends with error status M; with
  `WithLSSBoot` an unconfigured LSS slave of CANopenNode or Lely gets its
  node-ID by Fastscan and expected identity, then boots. Lely's boot
  master (`boot`, concise DCF by dcfgen) boots a go-canopen node (values
  written, node started) and reports status M for the other product
  code.
- **Concise DCF** (`cdcf`, Lely): dcfgen's entries for seven values
  (UNSIGNED8/16/64, INTEGER32 negative, REAL32) against `cdcf.Encode`
  with `canopen.ParseValue`, and `co_dev_write_dcf`'s dump of
  `interop-device-node5.dcf` (0x1000–0x1FFF, 0x2000–0x9FFF) against the
  go-canopen dictionary of the same DCF: byte for byte.
- **SYNC window**: python canopen's `rpdo-send` against RPDO 1 of a
  go-canopen node (event-driven; synchronous without window, inside the
  window, after the window: discarded, counted in
  `RPDOStats.OutsideWindow`); go-canopen's master (`RPDOSender`, `SYNC`)
  against the devices of CANopenNode and Lely (Lely discards after the
  window, `syncWindowRpdo`; CANopenNode applies, which CiA 301 allows).
- **CANopen FD** is recorded as a skip in both directions: no image
  declares it (none of the pinned stacks implements CiA 1301).

## Interop matrix

Results of canopen-interop v0.2.0 (contract 1.1) against go-canopen
v0.3.0, per adapter and direction: pass / known difference / skip /
fail. Local: the Lima VM `canvm` (arm64, Linux 7.0), 2026-10-29, the
whole suite (`make interop`'s binary, 1,620 scenarios, 6.3 minutes); the
report is `bin/interop-report.json`. CI: the Interop workflow on
GitHub's amd64 and arm64 runners, whose report is the workflow's
artifact; its contract 1.1 results are not written here before that
workflow has run.

| Adapter | Direction | v0.1.0 images (contract 1.0) | v0.2.0 images (contract 1.1), local |
|---------|-----------|------------------------------|-------------------------------------|
| canopennode | go-canopen-master | 340 / 21 / 2 / 0 | 360 / 23 / 6 / 0 |
| canopennode | go-canopen-device | 95 / 0 / 8 / 0 | 102 / 0 / 18 / 0 |
| canopennode | eds | 0 / 0 / 24 / 0 | 0 / 0 / 31 / 0 |
| lely | go-canopen-master | 347 / 16 / 0 / 0 | 369 / 18 / 2 / 0 |
| lely | go-canopen-device | 97 / 1 / 5 / 0 | 107 / 1 / 12 / 0 |
| lely | eds | 11 / 5 / 8 / 0 | 18 / 5 / 8 / 0 |
| pythoncanopen | go-canopen-master | 189 / 3 / 171 / 0 | 192 / 3 / 194 / 0 |
| pythoncanopen | go-canopen-device | 103 / 0 / 0 / 0 | 113 / 0 / 7 / 0 |
| pythoncanopen | eds | 11 / 7 / 6 / 0 | 15 / 7 / 9 / 0 |

The contract 1.0 scenarios are unchanged and give the same results; the
v0.2.0 device adds 0x1007 (two more uploads per device, normal and
block) and the EDS checks two fixtures (`mpdo-device.eds`,
`interop-device-other-product.eds`).

By area:

| Reference | SDO | NMT / heartbeat | PDO | SYNC / EMCY | EDS / DCF | CiA 402 |
|-----------|-----|-----------------|-----|-------------|-----------|---------|
| CANopenNode | ✅ both directions (known: empty DOMAIN, 0x1003 entries) | ✅ both directions | ✅ TPDO1/2/3, RPDO1 of its device | ✅ SYNC to its TPDO, EMCY both directions | DCF applied ✅; no EDS checker | – (no client) |
| Lely CANopen | ✅ both directions (known: 0x1003 entries, 0-byte block download by its client) | ✅ both directions | ✅ device TPDO/RPDO; `pdo-receive` of ours | ✅ SYNC to its TPDO, EMCY both directions | ✅ dcf-tools (known: 5 files it rejects) | – (no client) |
| python canopen | ✅ both directions (its device has no block transfer) | ✅ both directions | ✅ `pdo-configure`, `pdo-receive` of ours (its device has no PDOs) | ✅ its SYNC drives our TPDO; EMCY both directions | ✅ import_od (known: 7) | ✅ BaseNode402 operates our drive |

Features of contract 1.1 (pass / known difference / skip / fail per
direction; master = go-canopen master/client, device = the stack against
a go-canopen node):

| Feature | CANopenNode | Lely CANopen | python canopen |
|---------|-------------|--------------|----------------|
| LSS, go-canopen master → stack slave (14) | 12 / 2 / 0 / 0 (known: no identify services) | 12 / 2 / 0 / 0 (known: activation at reset communication, Fastscan when configured) | 0 / 0 / 14 / 0 (no LSS slave) |
| LSS, stack master → go-canopen node (7) | 7 / 0 / 0 / 0 (incl. `assign-all`) | 6 / 0 / 1 / 0 (no assign-all) | 6 / 0 / 1 / 0 (no assign-all) |
| MPDO DAM and SAM, go-canopen producer → stack consumer (2) | – (skip: no MPDO) | 2 / 0 / 0 / 0 | – (skip: no MPDO) |
| MPDO DAM and SAM, stack producer → go-canopen consumer (2) | – (skip) | 2 / 0 / 0 / 0 | – (skip) |
| CiA 302 boot-up, go-canopen manager → stack device (3: boot slave, identity M, LSS step) | 3 / 0 / 0 / 0 | 3 / 0 / 0 / 0 | 2 / 0 / 1 / 0 (no LSS slave) |
| CiA 302 boot-up, stack boot master → go-canopen node (2) | – (skip: no boot master) | 2 / 0 / 0 / 0 | – (skip) |
| Concise DCF, stack generator vs `cdcf` (3) | – (skip: no generator) | 3 / 0 / 0 / 0 (byte-exact) | – (skip) |
| SYNC window, go-canopen master → stack device RPDO (3) | 3 / 0 / 0 / 0 (applies late RPDOs: CiA 301 "may") | 3 / 0 / 0 / 0 (discards after the window) | – (skip: no RPDOs) |
| SYNC window, stack `rpdo-send` → go-canopen node (4) | – (skip: no RPDO sender) | – (skip) | 4 / 0 / 0 / 0 |
| CANopen FD (USDO, FD PDOs), both directions (4) | skip | skip | skip |

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
| master | canopennode | `lss-identify-non-configured`, `lss-identify-remote` | CANopenNode v4.1's LSS slave does not answer "identify non-configured remote slave" (0x4C) nor "identify remote slave" (0x46..0x4B), checked with candump, while it answers Fastscan; CiA 305 makes them optional for slaves |
| master | lely | `lss-activate-on-waiting` | Lely's LSS slave applies a configured node-ID only at the next NMT reset communication (co_nmt_set_id keeps it pending); CANopenNode's and go-canopen's apply it when the master switches an unconfigured slave back to waiting. After NMT reset communication the node-ID is active (`lss-configure-node-id` passes); canopen-interop known difference |
| master | lely | `lss-fastscan-configured` | Lely's LSS slave answers Fastscan whatever its node-ID (co_lss_fastscan checks the identity only); CANopenNode's and go-canopen's answer only while their active and pending node-IDs are 0xFF; canopen-interop known difference |
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
| all | `fd-usdo-client`, `fd-pdo` (master), `fd-usdo-server`, `fd-pdo` (device) | no image declares CANopen FD: none of the pinned stacks (nor their development branches) implements CiA 1301; contract 1.1 has no FD operation |
| canopennode | 4 MPDO (both directions), 3 cdcf, `rpdo-*` (4, device), boot master (2, device) | operations it does not offer (`mpdo-*`, `cdcf`, `rpdo-send`, `boot`) |
| lely | `lss-assign-all` (device), `rpdo-*` (4, device) | no assign-all service, no RPDO sender |
| pythoncanopen | 14 LSS and `boot-lss` (master), `lss-assign-all` (device), 2 + 2 MPDO, boot master (2), 3 cdcf, SYNC window towards its device (3) | its device has no LSS slave and no RPDOs; no assign-all, MPDO, boot master or concise DCF generator |
| canopennode | 2 limit aborts (master) | its device does not check LowLimit/HighLimit (`deviceLimits` false) |
| canopennode | `domain-0`, `block-domain-0` (device) | its gateway client cannot download 0 bytes (`unsupported`) |
| canopennode | `pdo-receive`, `pdo-configure`, `sync`, 3 × `cia402` (device) | operations it does not offer |
| canopennode | 24 EDS scenarios | no `eds-check` |
| lely | `pdo-configure`, `sync`, 3 × `cia402` (device) | operations it does not offer |
| lely, pythoncanopen | object lists of files a checker rejects (`objects-*`) | nothing to compare when one side judges the file invalid |
| pythoncanopen | 165 block transfers (master) | its device (LocalNode) has no SDO block transfer (`deviceBlock` false) |
| pythoncanopen | 2 limit aborts, TPDO1/2/3, RPDO1 (master) | its device checks no limits and has no PDOs (`deviceLimits`, `deviceTpdo`, `deviceRpdo` false) |

### Found by the suite

Contract 1.1 (fixed in v0.3.0, each with a regression unit test):

- **`lss.Master.AssignAll` failed when the last device took node-ID
  127** (found with CANopenNode's slaves: two devices from 126): it
  returned `ErrNoNodeID` before checking whether any device was left.
  Now the run succeeds when Fastscan finds no further device and fails
  with `ErrNoNodeID` only when one is left without a node-ID
  (`TestAssignAllLastNodeID`).
- **`AssignAll` gave every node-ID to one Lely device**: Lely's LSS slave
  keeps answering Fastscan with a pending node-ID (until NMT reset
  communication), so the device with the lowest address was identified
  and configured again and again (126, then 127, then `ErrNoNodeID`)
  and the second device got none. A device now gets one node-ID per run;
  when Fastscan identifies an assigned device again, the remaining
  devices are listed with `Master.Fastscan` and configured by switch
  state selective (`TestAssignAllLelySlaves`,
  `TestAssignAllLelySlavesLimits`).
- **A go-canopen node answered an SDO request received after an NMT
  reset command from its state before the reset**, and lost the next
  one while its SDO server was replaced (found with Lely's boot master,
  which uploads 0x1000 and 0x1018 right after its reset communication and
  took the lost upload for a wrong product code: error status M, 4 of 6
  runs). The SDO server now also receives the NMT commands
  (`sdo.WithResetFence`, used by `node`) and stops answering at a reset
  of its node, in the order the frames arrived (`TestNoSDOAnswerAfterResetCommand`,
  `TestServerResetFence`).
- **The SYNC window was measured on the wall clock**: the window ran
  from the SYNC's kernel timestamp and was compared with the wall clock
  when a PDO arrived, so a step of the clock in between (the Lima VM
  steps its clock back every few minutes) once let a late RPDO through.
  The SYNC's reception time is now on the monotonic clock, derived from
  the delay since its kernel timestamp (`TestReceiveTime`).

Contract 1.0:

- **SDO block transfer of 0 bytes** (fixed in v0.2.0): Lely's SDO server
  answers the block upload of an empty object with the end message and no
  segment, and python canopen's client does the same in a block download
  of 0 bytes. go-canopen's client and server took that end message for an
  out-of-sequence segment and waited until the timeout (the client then
  repeated its start request, which Lely aborted with 0x05040001). Both
  now accept the end of a transfer before any segment; unit tests in
  `sdo` (`TestClientBlockUploadEmpty`, `TestMachine`).

## Features of v0.3.0 without an independent oracle

CANopen FD (USDO, FD PDOs, FD emergencies), store and restore of
parameters (0x1010/0x1011) and the program download of CiA 302-3 are
not in canopen-interop's contract 1.1: CANopen FD because none of the
reference stacks implements it, the others because the contract has no
operation for them. They are tested against go-canopen itself (unit,
fuzz and chaos tests, the e2e scenarios on the virtual bus and on Linux
vcan with CAN FD, MTU 72; [TESTING.md](TESTING.md)). LSS, MPDOs, the
CiA 302 boot-up with concise DCFs and the SYNC window are tested against
the stacks above.

## What vcan proves

All stacks share the Linux SocketCAN code on a vcan interface: such
tests show that each stack puts the same CANopen messages on the bus and
interprets the other's messages alike. vcan has no arbitration, bit
timing, error frames or bus load: timing behaviour (SYNC jitter, inhibit
times under load, heartbeat margins) and electrical behaviour are not
covered. Nothing has been tested on real CAN hardware
([LIMITATIONS.md](LIMITATIONS.md)).
