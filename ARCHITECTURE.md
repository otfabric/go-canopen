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
 │  (services chosen from the objects, │  configuration, scan, remote PDOs,  │
 │  NMT gating, CiA 301 emergencies,   │  SDO clients, one event stream)     │
 │  CiA 402 drive)                     │                                     │
 ├─────────┬─────────┬────────┬────────┴──┬────────┬───────────┬────────────┤
 │ sdo     │ nmt     │ pdo    │ sync      │ emcy   │ timestamp │ cia402     │
 │ client, │ slave,  │ TPDO,  │ (syncobj) │ prod., │ TIME prod.│ drive,     │
 │ server  │ master, │ RPDO,  │ prod.,    │ cons.  │ and cons. │ controller │
 │         │ heart-  │ codec, │ cons.     │        │           │            │
 │         │ beat,   │ rules  │           │        │           │            │
 │         │ guarding│        │           │        │           │            │
 ├─────────┴─────────┴────────┴───────────┴────────┴───────────┴────────────┤
 │ od: object dictionary (objects, values, access, limits, defaults,        │
 │     read/write hooks, watchers)            eds: EDS/DCF ⇄ od.Dictionary  │
 ├───────────────────────────────────────────────────────────────────────────┤
 │ canopen (core): NodeID, COBID, predefined connection set, DataType       │
 │  codec, AbortCode/AbortError, NMT states and commands, Bus, errors       │
 ├───────────────────────────────────────────────────────────────────────────┤
 │ go-canbus: canbus.Bus (fan-out, subscriptions with filters) on SocketCAN, │
 │  slcan or the virtual bus                                                 │
 └───────────────────────────────────────────────────────────────────────────┘
   dissect: frames → CANopen messages (passive; uses only canopen and canbus)
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
objects exist; a CiA 402 drive with `WithDrive`.

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
soon as it sees it.

### Resets

The slave runs a reset as Initialising → dictionary restore (reset node:
0x2000..0x9FFF, then 0x1000..0x1FFF; reset communication: 0x1000..0x1FFF)
with a pending node-ID applied first → reset handler → boot-up message →
Pre-operational. The node's reset handler gives the heartbeat producer
and the guarding responder the new node-ID, restarts the heartbeat
consumer's supervision, clears the error state and renews the SDO server
(it reads its COB-IDs at start). The other services need nothing: they
see the restored values through their watchers.

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
CiA 301 order; EMCY/SYNC/TIME COB-ID changes released first).

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
| `sdo.Server`, nmt, pdo, sync, emcy, timestamp services | yes | one each after `Start` |
| `node.Node` | yes | its services' plus the drive loop |
| `master.Master` | yes | its services', the event dispatcher, one per running boot procedure |

Tests run with the race detector and check for goroutine leaks
([TESTING.md](TESTING.md)).
