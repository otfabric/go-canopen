# Changelog

Release notes with details are in [RELEASE.md](RELEASE.md).

## v0.5.0 (2026-11-08)

Tested against recorded traffic of real Copley Controls drives and their
EDS. `dissect.Tracker` decodes node guarding answers with the toggle bit
clear as guard responses (they were heartbeats) and learns PDO COB-IDs
from SDO traffic (`PDO.Configured`); `eds.File.Validate` warns instead of
failing on mappable string objects.

## v0.4.1 (2026-11-04)

`sdo`: a segmented transfer retried on a bus that delays or reorders
frames could take a response to a stale request as its own and return
wrong data; the client discards responses received before each segment
request.

## v0.4.0 (2026-11-04)

Go 1.26 floor, go-canbus v0.8.0. SAST hardening: a malformed NMT frame no longer fences the
SDO server; USDO client uploads are limited (1 MiB default) and sizes of
4 GiB or more are refused instead of wrapped; LSS Fastscan stops against
a slave that acknowledges everything; the master's event queue is
bounded (`WithEventQueue`, `Stats.EventsDropped`); out-of-range values in
concise DCF, EDS literals and wide dictionary objects are refused instead
of truncated; `od.Dictionary.Add` refuses node-ID-relative defaults that
would wrap for node-ID 127; Fastscan repeats a step whose silence
contradicts an earlier answer with a doubled window, so late answers no
longer hide devices. Hostile-peer, fuzz and chaos tests added.

## v0.3.2 (2026-10-30)

`master`: `DownloadProgram` reads the program state before repeating a
step that timed out; a late answer no longer fails a software update or
leaves the new program stopped. `Scan` repeats a request that timed out
once, so that one late answer does not hide a device. `lss`: a slave
left in the configuration state by a lost selection acknowledgement no
longer takes the next device's node-ID.

## v0.3.1 (2026-10-30)

`master`: a network boot-up cancels the boot slave processes that run
when it starts, before its reset communication (the reset undid their
configuration and they failed with status K); a software update in
progress is completed rather than left with the program stopped.

## v0.3.0 (2026-10-29)

New packages `lss` (CiA 305 LSS master and slave with Fastscan and
AssignAll), `mpdo` (multiplexed PDOs, DAM and SAM, scanner and dispatcher
lists), `usdo` (the CANopen FD USDO client and server), `store`
(parameter persistence behind 0x1010/0x1011), `cdcf` (concise DCF) and
`program` (CiA 302-3 program download, device side). CANopen FD in `pdo`
(64-byte PDOs), `sync`, `emcy` (20-byte FD emergency) and `nmt`; the
synchronous window 0x1007 enforced for synchronous PDOs. `node` integrates
all of it: `WithLSS` (unconfigured start with node-ID 0xFF, node-ID
persisted in the store), MPDO producers and consumers for every PDO
configured as an MPDO, `WithFD` (USDO server in place of the SDO server),
`WithStore`, `WithProgram`. `master` is a CiA 302 manager with
`WithDictionary` (network boot-up, boot slave process with error status
letters, concise DCF, program download, store/restore requests), has an
LSS master (`LSS`, optional LSS step `WithLSSBoot`), MPDO list helpers,
and runs every procedure over USDO with `WithFD`. `dissect` decodes USDO
and FD emergencies. Fixed: a data race between concurrent NMT
subscriptions (shared filter option); an LSS configuration that could
reach a second slave left selected by a lost frame (`Configure` now
confirms the selection); boot-up events timed by processing instead of
reception (a late boot-up booted a slave twice); boot slave processes
failing on a single SDO timeout (the repeatable requests are retried);
`BootNetwork` returning "context canceled" when superseded by another
network boot-up (it returns that boot-up's result). Interoperability suite on
canopen-interop v0.2.0 (contract 1.1): LSS (go-canopen's master against
CANopenNode's and Lely's slaves, the three stacks' masters against
go-canopen nodes), MPDO DAM and SAM with Lely both ways, the CiA 302
boot-up (go-canopen's manager booting each stack's device, also with
the LSS step; Lely's boot master booting a go-canopen node), concise
DCFs byte-exact against Lely's dcfgen and dictionary dump, the SYNC
window (python canopen's RPDO sender; CANopenNode's and Lely's
devices); 1,620 scenarios, no failure (INTEROPERABILITY.md). Fixed,
found by it: `lss.Master.AssignAll` failed with `ErrNoNodeID` when the
last device took node-ID 127, and gave every node-ID to a device that
keeps answering Fastscan with a pending node-ID (Lely); a node answered
an SDO request received after an NMT reset command from its state
before the reset and lost the next one, and could answer a request
received during the reset before its boot-up message (new
`sdo.WithResetFence`, `sdo.WithFenced`, `sdo.Server.Fence` and
`nmt.WithBootUpHandler`,
used by `node`); the SYNC window was measured on the wall clock (now the
monotonic clock).

## v0.2.0 (2026-10-29)

Interoperability suite (`interop/`, `make interop`, Interop workflow on
amd64 and arm64) against CANopenNode, Lely CANopen and python canopen
through the canopen-interop v0.1.0 images: go-canopen as master/client of
each stack's device, each stack as master/client of a go-canopen node
(SDO, NMT, heartbeat, EMCY, PDO, SYNC, CiA 402 with python canopen's
BaseNode402), and the EDS checkers against the eds package; the matrix
and the known differences are in INTEROPERABILITY.md. Fixed, found by the
suite: an SDO block transfer of empty data that ends without a segment
(Lely's server on upload, python canopen's client on download) timed
out in go-canopen's client and server; both now accept it.

## v0.1.0 (2026-10-29)

First release. A pure Go CANopen (CiA 301) stack on go-canbus v0.7.0:
the object dictionary with hooks and watchers; SDO client and server
with expedited, segmented and block transfer; NMT master and slave with
both resets, heartbeat producer and consumer, node and life guarding;
PDOs with every transmission type, the CiA 301 reconfiguration rules and
remote configuration; SYNC, EMCY and TIME; a complete device (`node`)
built from a dictionary and gated by NMT state; a network manager
(`master`) with scan, boot procedure, DCF configuration, remote PDOs and
one ordered event stream; EDS/DCF (CiA 306) parse, validate, write and
dictionary conversion; a Go code generator; the CiA 402 drive profile
(simulated drive and controller); a frame dissector checked against
Wireshark. No interoperability results against other CANopen stacks
yet; nothing tested on real hardware.
