# Architecture

go-canopen is a pure Go (no cgo) CANopen implementation on top of
[go-canbus](https://github.com/otfabric/go-canbus). It is layered so that
each CiA 301 service can be used and tested on its own, and so that a
complete device (`node`) and a network manager (`master`) are only
compositions of those services.

```
 ┌───────────────────────────────────────────────────────────────────────────┐
 │ applications: devices, network managers, tools (canopenctl), generated   │
 │ code (codegen)                                                            │
 ├─────────────────────────────────────┬─────────────────────────────────────┤
 │ node: a device from a dictionary    │ master: network manager (NMT, boot, │
 │  (services chosen from the objects, │  CiA 302 manager: network boot-up,  │
 │  NMT gating, CiA 301 emergencies,   │  configuration, program download,   │
 │  MPDOs, LSS slave, CANopen FD mode, │  LSS master, scan, remote PDOs,     │
 │  store/restore, program download,   │  SDO/USDO access, one event stream) │
 │  CiA 402 drive)                     │                                     │
 ├────────┬────────┬────────┬────────┬─┴──────┬────────┬─────────┬──────────┤
 │ sdo    │ usdo   │ nmt    │ pdo,   │ sync,  │ lss    │ store,  │ cia402   │
 │ client,│ client,│ slave, │ mpdo   │ emcy,  │ master,│ cdcf,   │ drive,   │
 │ server │ server │ master,│ TPDO,  │ time-  │ slave  │ program │ control- │
 │        │ (FD)   │ heart- │ RPDO,  │ stamp  │        │         │ ler      │
 │        │        │ beat,  │ MPDO,  │        │        │         │          │
 │        │        │ guard  │ rules  │        │        │         │          │
 ├────────┴────────┴────────┴────────┴────────┴────────┴─────────┴──────────┤
 │ od: object dictionary (objects, values, access, limits, defaults,        │
 │     read/write hooks, watchers)            eds: EDS/DCF ⇄ od.Dictionary  │
 ├───────────────────────────────────────────────────────────────────────────┤
 │ canopen (core): NodeID, COBID, predefined connection set, DataType       │
 │  codec, AbortCode/AbortError, NMT states and commands, Bus, errors       │
 ├───────────────────────────────────────────────────────────────────────────┤
 │ go-canbus: canbus.Bus (fan-out, subscriptions with filters) on SocketCAN, │
 │  slcan or the virtual bus; classical CAN and CAN FD frames                │
 └───────────────────────────────────────────────────────────────────────────┘
   dissect: frames → CANopen messages (passive)
```

## The bus

Every service takes a `canopen.Bus`:

```go
type Bus interface {
    Send(ctx context.Context, f *canbus.Frame) error
    Subscribe(opts ...canbus.SubscribeOption) (*canbus.Subscription, error)
}
```

`*canbus.Bus` implements it on every go-canbus transport. Each service
subscribes with a kernel-style filter for the COB-IDs it needs (SDO
server: 0x600+node; heartbeat consumer: 0x700..0x77F; ...), so services
share one bus without seeing each other's traffic. A node does not
receive its own transmissions, as on a real bus: two peers in one process
need two buses (two virtual ports, or two SocketCAN sockets). The node
processes its own SYNC through its gated bus (below).

## Core package `canopen`

Shared types only, no goroutines: `NodeID` (1..127), `COBID` (CAN-ID plus
the flag bits 31/30/29 of the communication objects), the predefined
connection set (`FuncNMT` .. `FuncHeartbeat`, `DefaultTPDO`,
`DefaultRPDO`, `Classify`), `DataType` with `Encode`/`Decode`/
`ParseValue`/`FormatValue` (little-endian, CiA 301 7.1), `AbortCode`
(also the error the dictionary returns, so an SDO server forwards a
refusal unchanged) and `*AbortError`, `NMTState`/`NMTCommand`, and the
sentinel errors ([ERRORS.md](ERRORS.md)).

## Object dictionary (`od`)

A `Dictionary` holds `Object`s (VAR, ARRAY, RECORD, DOMAIN) of
`Variable`s and their current values. It distinguishes:

| Access | Calls | Checks | Hooks | Watchers |
|--------|-------|--------|-------|----------|
| Remote (from the bus: SDO server, RPDO) | `Read`, `Write` | access right, length, limits | read hooks, write hooks (may refuse with an `AbortCode`) | yes |
| Local (the application) | `Get`, `Set`, `GetValue`, `SetValue`, `GetUint`, `SetUint` | data type length | no | yes |

- **Write hooks** (`OnWrite(index, sub, h)`, `sub = -1` for every
  sub-index) are how services enforce CiA 301 rules on remote writes: PDO
  reconfiguration (`pdo.Protect`), EMCY/SYNC/TIME COB-ID changes, 0x1016
  duplicates, 0x1019 while SYNC runs, CiA 402 controlword and mode
  checks. Specific hooks run before index-wide ones, in registration
  order.
- **Read hooks** (`OnRead`) compute values on demand.
- **Watchers** (`Watch`) see every successful change, remote or local,
  after it is stored: services follow their parameters through them (a
  new 0x1017 changes the heartbeat period at once, a new PDO COB-ID
  resubscribes).
- `OnRead`, `OnWrite` and `Watch` return a function that removes the
  hook. Every service removes what it installed when it is closed, so a
  dictionary can be handed to a new node after the old one closed;
  `HookCount` reports the number registered (diagnostic; a node test
  checks it stays stable over repeated create/close cycles).
- Hooks and watchers run **without the dictionary lock**: they may read
  and write the dictionary. Watchers run on the goroutine that made the
  change.
- `Reset(from, to)` restores defaults (as the NMT resets do);
  node-ID-relative defaults (`$NODEID+x`) follow `SetNodeID`.
- `od.Standard(Config)` builds the CiA 301 communication objects in
  their default configuration.

## Services

Every long-running service follows one pattern:

- `NewX(bus, ..., opts...)` validates and reads its configuration (from
  options, or from the dictionary with `WithDictionary`), installs its
  write hooks, and does nothing on the bus.
- `Start()` subscribes **before it returns** (no frame is missed after
  it) and runs one goroutine. A second `Start` fails with `ErrBusy`;
  `Start` after `Close` with `ErrClosed`.
- `Close()` stops the goroutine, waits for it, and removes the hooks and
  watchers. It is idempotent.
- Handlers (`WithHandler`, `WithHeartbeatHandler`, ...) run on the
  service's goroutine and must not block for long.
- `Stats()` returns a JSON-taggable snapshot of counters
  ([OBSERVABILITY.md](OBSERVABILITY.md)).

The SDO client is not a goroutine: transfers run on the caller's
goroutine and are serialised per client; it keeps one subscription for
its lifetime. The SDO server runs one goroutine per channel, one transfer
at a time.

## Node (`node`)

`node.New(bus, d)` looks at which objects the dictionary has and builds
the services accordingly: NMT slave, SDO server and EMCY producer always;
heartbeat producer with 0x1017, consumer with 0x1016; guarding responder
with 0x100C/0x100D; SYNC consumer with 0x1005, producer with 0x1006 and
0x1019 too; TIME with 0x1012; every RPDO and TPDO whose two parameter
objects exist; an MPDO producer or consumer for every PDO whose mapping
sub-index 00h is 0xFE (SAM) or 0xFF (DAM), at `New` and whenever a
mapping becomes an MPDO later (a dictionary watcher; the classical PDO
service of that number is then inactive, `pdo` treats MPDO mappings as
such); a CiA 402 drive with `WithDrive`; an LSS slave with `WithLSS`;
the parameter store with `WithStore`; the program download objects with
`WithProgram`.

### CANopen FD mode

`node.WithFD` passes the FD option to every service (`pdo.WithFD`,
`syncobj.WithFD`, `emcy.WithFD` with logical device, CiA specification
and clock, `nmt.WithFD`) and replaces the SDO server by a `usdo.Server`:
both would answer on 580h + node-ID. The USDO server is long-lived: it is
enabled and disabled with the NMT state like the SDO server and follows
the node-ID at resets (`SetNodeID`). The guarding responder is left out
(CAN FD has no remote frames). `master.WithFD` does the same on the
manager side and routes every procedure that accesses a node's
dictionary through an internal access interface backed by the master's
USDO client (USDO aborts are reported as SDO aborts), so the CiA 302
boot-up, configuration and program download (bulk transfer) work
unchanged on a CANopen FD network.

### LSS and unconfigured nodes

With `WithLSS` the node builds an `lss.Slave` from 0x1018 with handlers
wired to itself: configure node-ID sets the NMT slave's pending node-ID;
an unconfigured device that leaves the LSS configuration state resets
communication on its own (CANopenNode's behaviour); after every reset
the LSS slave learns the applied node-ID; store configuration writes
the pending settings under the pseudo index 0x0000 of the parameter
store, which `New` reads back. A dictionary node-ID of 0xFF starts the
node unconfigured: the NMT slave stays in Initialising without boot-up
and executes only reset commands, and the node builds its services with
a placeholder node-ID but starts them only in the reset that applies a
real node-ID (`deferred`), re-reading what they cached at construction
(the EMCY COB-ID).

### NMT gating

The NMT slave's state handler enables and disables the services as CiA
301 table 79 requires:

| State | SDO server | PDOs | EMCY, SYNC, TIME producers | Heartbeat, guarding |
|-------|------------|------|-----------------------------|---------------------|
| Initialising | no | no | suppressed | no heartbeat |
| Pre-operational | yes | no | yes | yes |
| Operational | yes | yes | yes | yes |
| Stopped | no | no | suppressed (counted in `Stats.Suppressed`) | yes |

The SDO server and the PDOs have `SetEnabled`. The EMCY, SYNC and TIME
producers send through a *gated bus*: a `canopen.Bus` wrapper that drops
(and counts) frames while the node is not Pre-operational or Operational,
and that hands the node's own SYNC frames to its PDOs. The SDO server is
created enabled just before each boot-up message, because the boot-up
announces Pre-operational and a master may send its first SDO request as
soon as it sees it. MPDO producers send through the gated bus as well;
MPDO producers and consumers are enabled in Operational only.

### SYNC and the synchronous window

A SYNC received by the node's SYNC consumer carries the end of its
synchronous window (0x1007, measured from the frame's reception); the
node passes it to every RPDO and TPDO (`OnSyncWindow`): a synchronous
TPDO not sent before the end is discarded (`WindowMissed`), an RPDO
received after it is discarded until the next SYNC (`OutsideWindow`).
The node's own SYNC (its SYNC producer) opens the window at the time of
processing.

### Resets

The slave runs a reset as Initialising → dictionary restore (reset node:
0x2000..0x9FFF, then 0x1000..0x1FFF; reset communication: 0x1000..0x1FFF)
with a pending node-ID applied first → reset handler → boot-up message →
Pre-operational. The node's reset handler gives the heartbeat producer
and the guarding responder the new node-ID, restarts the heartbeat
consumer's supervision, clears the error state and renews the SDO server
(it reads its COB-IDs at start; in FD mode the USDO server takes the new
node-ID instead), loads the stored parameters of the reset areas, and
tells the LSS slave the applied node-ID. The other services need
nothing: they see the restored values through their watchers.

### Emergencies

The node raises the CiA 301 communication emergencies itself (heartbeat
or life guarding loss 0x8130 with the 0x1029 error behaviour, PDO length
0x8210/0x8220, RPDO timeout 0x8250, SYNC length 0x8240, the drive's
codes) and clears each when its cause disappears.

## Master (`master`)

A `Master` composes an NMT master, a heartbeat consumer (state of every
node, supervision with `Monitor`), an EMCY consumer, SYNC and TIME
producers, an optional heartbeat producer, and a cached SDO client per
node. Everything it observes becomes an `Event` on **one handler**,
delivered in order on the master's own goroutine through an unbounded
queue, so the handler may itself call the master (SDO, NMT) without
stalling the network services. Boot procedures (`SetBoot`) run in a
goroutine per node, started by a boot-up; a new boot-up cancels the
running one, whose end is then not reported. `Configure` writes entries
(PDO parameters grouped per PDO and written with `pdo.Configure` in the
CiA 301 order; EMCY/SYNC/TIME COB-ID changes released first). The
master also owns an LSS master (`LSS()`), started and closed with it,
and, with `WithFD` or `WithUSDOClient`, one USDO client (`USDO()`).

### CiA 302 manager

With `WithDictionary` the master builds a `node.Node` on the manager's
dictionary (its own NMT slave, SDO server, heartbeat producer...) and is
driven by the manager objects 0x1F80..0x1F89. At every boot-up of that
node with 0x1F80 bit 0 set, the network boot-up runs: the optional LSS
step (`WithLSSBoot`: Fastscan, node-ID by expected identity, repeated
while a round assigns node-IDs; boot-ups during the step are not
booted), the reset of the slaves, then one boot slave process per slave
(goroutines of the master, in the order of Lely lely-core's NMT master:
identity, software check and program download, configuration date check,
restore, concise DCF, store, error control, NMT start), the wait for the
mandatory slaves, and Operational. Results are events with the CiA 302
error status letters.

## EDS, code generation, dissection

`eds` parses CiA 306 files into a model that round-trips through `Write`,
validates them into diagnostics, and converts to and from an
`od.Dictionary`. `codegen` generates typed Go from that model (golden
files in `internal/golden`, regenerated by `make generate`). `dissect`
decodes frames without state (`Decode`) or follows SDO transfers
(`Tracker`); it does not use the services.

## Concurrency summary

| Object | Safe for concurrent use | Goroutines |
|--------|-------------------------|------------|
| `od.Dictionary` | yes (RWMutex; hooks and watchers run unlocked) | none |
| `sdo.Client` | yes; transfers serialised | none (caller's) |
| `sdo.Server`, nmt, pdo, sync, emcy, timestamp services, `mpdo.Consumer`, `lss.Slave`, `usdo.Server` | yes | one each after `Start` |
| `mpdo.Producer`, `lss.Master` | yes; requests serialised | none (caller's) |
| `usdo.Client` | yes; several transfers at once (sessions) | one dispatcher |
| `node.Node` | yes | its services' plus the drive loop |
| `master.Master` | yes | its services', the event dispatcher, one per running boot procedure |

Tests run with the race detector and check for goroutine leaks
([TESTING.md](TESTING.md)).
