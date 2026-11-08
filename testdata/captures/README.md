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

| Directory | Source | Revision | Class | Terms | In this repository |
|---|---|---|---|---|---|
| `copley/` | [CopleyControlsOfficial/CANopen-Message-Log-Examples](https://github.com/CopleyControlsOfficial/CANopen-Message-Log-Examples): four CANview logs and the Accelnet EDS | commit `be9ed7055eb88a4b933788b3e05de62bcfe7b4b1` | A | No licence file; the README offers the logs to customers "as a reference for implementing the CANopen protocol". No redistribution permission is stated. | Excerpts only (`*.excerpt.json`); `make captures-extended` fetches the whole files |
| `wireshark/` | [Wireshark wiki, CANopen](https://wiki.wireshark.org/CANopen) sample capture `SampleCaptures/CANopen.pcap`; byte-identical to [EmreEkin/ICS-Pcaps `CANopen/CANopen.pcap`](https://github.com/EmreEkin/ICS-Pcaps/blob/0ffbf023afb4e39373228b54851bb4a1f33a3ab8/CANopen/CANopen.pcap) | downloaded 2026-11-08 | C | No licence statement; published as a sample capture for the dissector (as go-s7comm uses the wiki's S7comm samples) | The file, unchanged ([SHA256SUMS](SHA256SUMS)) |

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

**Redistribution.** The Copley logs are public, but public is not
permission. They are therefore not committed: each `copley/*.excerpt.json`
holds the lines a golden case needs, verbatim with their line numbers
(`[line, "text"]`), and the SHA-256 of the whole file. The excerpts are
small (about 200 of 2,274 to 8,861 lines each) and consist of protocol
frames. If Copley grants permission, the whole logs can be committed and
`scripts/fetch-captures.sh` retired; this is the owner's call.

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

| File | Frames | What it holds |
|---|---:|---|
| Absolute Move (excerpt: 212 lines) | 2,274 | Identity; reads and rewrites of the PDO communication and mapping parameters (TPDO1 = 0x2190, statusword 0x6041, 0x1002); TPDO5 on COB-ID 0x180 with the high-resolution time stamp 0x1013 every 10th SYNC; the drive as SYNC producer (0x1005 = 0x40000080, 0x1006 = 10 ms); NMT start; node guarding (0x100C = 200 ms, 0x100D = 3); fault reset; profile position move; at the end, the life guarding error EMCY 0x8130 and 0x61FF |
| Relative Move (extended only) | 2,464 | As Absolute Move, relative target |
| Linkage Home and Move (excerpt: 286 lines) | 2,928 | Two axes: TPDO and RPDO remapping over SDO, TPDO5 time stamp of node 2 consumed by RPDO5 of node 3 on 0x180, homing, coordinated move over RPDOs |
| PVT Move (excerpt: 190 lines) | 8,861 | Two axes: PVT segments over RPDO1 (manufacturer object 0x2010), position feedback over TPDO2 |
| CANopen.eds (extended only) | | Copley Controls Accelnet EDS, FileRevision 7, 2014-03-18 |
| `wireshark/canopen-sample.pcap` | 34 | Boot-ups, NMT, SDO uploads and downloads answered with aborts, an upload segment exchange, LSS identify non-configured slave, a block upload aborted at once |

All 16,527 frames of the four logs: 1,101 SDO exchanges (all expedited),
460 node guarding requests and as many answers, 6 NMT, 12 EMCY, 7,326 SYNC and
PDOs.

## How the tests use them

| Test | Checks |
|---|---|
| `internal/capture` | The CANview line reader (and its refusals), the excerpt loader, a SocketCAN pcap reader, SHA256SUMS |
| `dissect` `TestCopley*`, `TestWiresharkSampleCapture` | Every frame decodes without error; every SDO answer belongs to the request before it (node, object, command); every guard request is answered by a guard response, also with the toggle bit clear; frames on 0x180 are TPDO5 of the drive once its configuration was seen; identity; emergencies |
| `dissect` `TestRecordedFramesMatchWireshark` | Decode equals Wireshark 4.6's CANopen dissector, field by field, for every committed frame (with tshark installed) |
| `sdo` `TestCaptureReplayClient` | The client sends the recorded master's requests byte for byte (the peer answers only an identical request) and accepts the drives' answers, also those with stale bytes after the indicated size |
| `sdo` `TestCaptureReplayServer` | The server answers every recorded request as the drive did |
| `nmt` `TestCaptureGuardMaster`, `TestCaptureLifeGuarding` | The guard master accepts every recorded answer (first toggle bit 1); a responder with the recorded guard time and factor detects the loss when the drive reported it (600 ms after the last request) |
| `cia402` `TestCaptureCopleyStateMachine`, `TestCaptureDriveParity` | Statusword changes from TPDO1, decoded with the recorded mapping, against the CiA 402 diagram |
| extended (`make captures-extended`) | The same on the whole four logs (1,101 SDO exchanges to client and server, the server's data types from the Copley EDS), all 16,527 frames against Wireshark, the EDS against every object the logs access, and the excerpts against the logs line by line |

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
- The excerpts' SYNC frames (0x080, no data) are left out outside the
  first lines of each log.

## Adding a log

Copy a file whose terms allow it under the source's directory and add its
line to SHA256SUMS; otherwise add it to `scripts/fetch-captures.sh` with
its revision and SHA-256 and commit an excerpt. Record the source in
manifest.json and the tables above.
