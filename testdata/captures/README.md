# Recorded CANopen traffic

CANopen traffic recorded by others, used as independent evidence for
go-canopen's decoder, SDO client and server, node guarding and CiA 402
state handling. The main source is the set of message logs Copley Controls
publishes of its own drives; the drives identify themselves in the traffic.
It is evidence from real devices, recorded by others; it is not a test
against a live drive and not a conformance claim.

[manifest.json](manifest.json) is the machine-readable form of this file:
every source with its URL, revision, size, SHA-256, licence, evidence
class, devices and operations, and every golden case with the tests that
check it.

## Sources and terms

| Directory | Source | Revision | Class |
|---|---|---|---|
| `copley/` | [CopleyControlsOfficial/CANopen-Message-Log-Examples](https://github.com/CopleyControlsOfficial/CANopen-Message-Log-Examples): four CANview logs and the Accelnet EDS, published by Copley "as a reference for implementing the CANopen protocol" | commit `be9ed7055eb88a4b933788b3e05de62bcfe7b4b1` | A |
| `wireshark/` | [Wireshark wiki, CANopen](https://wiki.wireshark.org/CANopen) sample capture `SampleCaptures/CANopen.pcap`; byte-identical to [EmreEkin/ICS-Pcaps `CANopen/CANopen.pcap`](https://github.com/EmreEkin/ICS-Pcaps/blob/0ffbf023afb4e39373228b54851bb4a1f33a3ab8/CANopen/CANopen.pcap), which is therefore not copied twice | downloaded 2026-11-08 | C |

The files are byte-identical to their sources under the names below,
checked by [SHA256SUMS](SHA256SUMS) (`shasum -a 256 -c SHA256SUMS` in this
directory); `.gitattributes` keeps git from touching their CRLF line ends.

Examined and not used: the candump in
[ros-industrial/ros_canopen#473](https://github.com/ros-industrial/ros_canopen/issues/473)
(class B: a Lichuan servo drive on a Raspberry Pi, not identified in the
traffic). It is about 25 frames of candump's console output, shortened by
the poster, without the PDO mapping its PDOs need; it shows nothing the
Copley logs do not, except a boot-up message and NMT reset communication.

Evidence classes: A, physical devices identified in the traffic; B,
physical origin documented but not verified; C, origin unknown; D,
generated. Only A and B support a statement about real devices; C is used
for decoder regression.

## Names

`<client-or-peer>_<device>_<operation>.<ext>`, lower case, words joined by
hyphens, in the directory of the source; the extension keeps the original
format (`.pcap`, `.eds`; `.log` for a CANview log,
whose original has no extension). manifest.json keeps each original name
and URL.

| Client or peer | Software |
|---|---|
| `unknown` | not stated by the source. The Copley logs were recorded with Copley's CANview; the master that sent the requests is not identified in them (no identity object of its own, no heartbeat) |

| Device | Meaning |
|---|---|
| `copley-1100`, `copley-11e0`, `copley-1060` | a Copley Controls drive (vendor-ID 0xAB) by its product code from 0x1018:02; the product names are not in the logs |
| `unknown` | no device identity in the traffic |

The Copley EDS, not a capture, is `copley/copley_accelnet.eds` (vendor,
family).

## Devices

Read from the drives' own answers to 0x1018 (identity object). Vendor-ID
0x000000AB is Copley Controls'. The product names of the codes are not in
the logs; the EDS describes the Accelnet family.

| Log | Node-IDs | Product code | Revision | Serial number |
|---|---|---|---|---|
| Absolute Move, Relative Move | 1 | 0x00001100 | 0x00010004 | 0x00E735B9 |
| Linkage Home and Move | 2, 3 | 0x000011E0 | 0x00010004 | 0x003FCCA5 (both nodes: most likely two axes of one drive) |
| PVT Move | 1, 2 | 0x00001060 | 0x00010004 | 0x002FBB62 (both nodes: most likely two axes of one drive) |

The master is not identified (the logs show it on CANview port 0).

## Files

| File | Original name | Frames | What it holds |
|---|---|---:|---|
| `copley/unknown_copley-1100_absolute-move.log` | `Absolute Move` | 2,274 | Identity; reads and rewrites of the PDO communication and mapping parameters (TPDO1 = 0x2190, statusword 0x6041, 0x1002); TPDO5 on COB-ID 0x180 with the high-resolution time stamp 0x1013 every 10th SYNC; the drive as SYNC producer (0x1005 = 0x40000080, 0x1006 = 10 ms); NMT start; node guarding (0x100C = 200 ms, 0x100D = 3); fault reset; profile position move; at the end, the life guarding error EMCY 0x8130 and 0x61FF |
| `copley/unknown_copley-1100_relative-move.log` | `Relative Move` | 2,464 | As Absolute Move, relative target |
| `copley/unknown_copley-11e0_linkage-home-and-move.log` | `Linkage Home and Move` | 2,928 | Two axes: TPDO and RPDO remapping over SDO, TPDO5 time stamp of node 2 consumed by RPDO5 of node 3 on 0x180, homing, coordinated move over RPDOs |
| `copley/unknown_copley-1060_pvt-move.log` | `PVT Move` | 8,861 | Two axes: PVT segments over RPDO1 (manufacturer object 0x2010), position feedback over TPDO2 |
| `copley/copley_accelnet.eds` | `CANopen.eds` | | Copley Controls Accelnet EDS, FileRevision 7, 2014-03-18 |
| `wireshark/unknown_unknown_boot-nmt-sdo-lss.pcap` | `CANopen.pcap` | 34 | Boot-ups, NMT, SDO uploads and downloads answered with aborts, an upload segment exchange, LSS identify non-configured slave, a block upload aborted at once |

All 16,527 frames of the four logs: 1,101 SDO exchanges (all expedited),
460 node guarding requests and as many answers, 6 NMT, 12 EMCY, 7,326 SYNC and
PDOs.

## How the tests use them

| Test | Checks |
|---|---|
| `internal/capture` | The CANview line reader (and its refusals), the four logs against manifest.json, a SocketCAN pcap reader, SHA256SUMS |
| `dissect` `TestCopley*`, `TestWiresharkSampleCapture` | All 16,527 frames decode without error; every SDO answer belongs to the request before it (node, object, command); every guard request is answered by a guard response, also with the toggle bit clear; frames on 0x180 are TPDO5 of the drive once its configuration was seen; identity; emergencies |
| `dissect` `TestRecordedFramesMatchWireshark` | Decode equals Wireshark 4.6's CANopen dissector, field by field, for all 16,527 frames and the sample capture (with tshark installed) |
| `sdo` `TestCaptureReplayClient` | For all 1,101 exchanges, the client sends the recorded master's requests byte for byte (the peer answers only an identical request) and accepts the drives' answers, also those with stale bytes after the indicated size |
| `sdo` `TestCaptureReplayServer` | The server, with the data types of the Copley EDS, answers every recorded request as the drive did |
| `eds` `TestCopleyEDS` | The Copley EDS validates without errors and holds every object the logs read or write, with a matching access type and size |
| `nmt` `TestCaptureGuardMaster`, `TestCaptureLifeGuarding` | The guard master accepts every recorded answer (first toggle bit 1); a responder with the recorded guard time and factor detects the loss when the drive reported it (600 ms after the last request) |
| `cia402` `TestCaptureCopleyStateMachine`, `TestCaptureDriveParity` | Statusword changes from TPDO1, decoded with the recorded mapping, against the CiA 402 diagram |

## Comparison rules

- SDO values are device state (a position, a status register): before an
  upload the server's object holds the value the drive answered. The
  protocol is compared: the command byte with its e, s and n bits, the
  multiplexer, and the indicated data bytes.
- Bytes after the indicated size of an expedited answer are not compared.
  CiA 301 leaves them undefined; the Copley drives leave stale bytes there
  (`4F 00 18 02 FF 01 00 80` answers one byte, 0xFF), go-canopen sends
  zeros.
- Time stamps are not compared, except intervals: the life guarding error
  against guard time × life time factor, and the 0x1013 time stamp against
  the log's clock.

## Adding a log

Copy it byte-identical under the source's directory with a name as above,
add its line to SHA256SUMS, and record it (original name, URL, revision,
SHA-256) in manifest.json and the tables above. A file over about 50 MB
stays out of the repository and gets a fetch script instead.
