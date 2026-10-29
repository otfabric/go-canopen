# Observability

Three channels: structured logs (`log/slog`), counters (`Stats()` on
every service), and events (handlers, and the master's single event
stream). Nothing is exported to a metrics system directly; the `Stats`
structs carry JSON tags, so they can be published as they are.

## Logging

Every service, `node` and `master` take `WithLogger(*slog.Logger)`. The
default logs nothing. `node.WithLogger` and `master.WithLogger` pass the
logger to their services; the node adds the field `node` (its node-ID),
PDO services `pdo` ("TPDO 1", "RPDO 2"). Messages are constant strings
with fields:

| Message | Level | Package |
|---------|-------|---------|
| `nmt boot-up`, `nmt state` | info | nmt (slave): node, from, to |
| `node reset` | info | node: kind, nodeId |
| `emcy raise`, `emcy` | info | emcy (producer, consumer) |
| `heartbeat timeout` | warn | nmt (consumer): node, previous |
| `heartbeat send`, `node guarding`, `node guarding request`, `node guarding response`, `nmt reset` | warn | nmt: send failures, guarding losses, failed resets |
| `emcy send`, `sync send`, `time send` | warn | send failures |
| `sync re-subscribe`, `time re-subscribe` | warn | a COB-ID change could not be followed |
| `sdo: server send failed` | warn | sdo |
| `PDO configuration not usable, PDO inactive`, `read mapped object` | warn | pdo |
| `node reset: SDO server` | warn | node: the renewed SDO server could not start |
| `subscribe`, `subscription ended` | error | pdo: the PDO cannot receive |
| `nmt command`, `nmt command sent`, `nmt invalid command frame`, `heartbeat event`, `emcy sent`, `emcy invalid frame` | debug | nmt, emcy |
| `sdo: server started`, `sdo: server abort`, `sdo: server aborted`, `sdo: client aborted`, `sdo: retrying after timeout` | debug | sdo |
| `write mapped object`, `decode PDO`, `send` | debug | pdo |

## Counters

`Stats()` returns a snapshot. Counters only grow, except a node's SDO
server counters, which restart when the server is renewed at an NMT
reset.

| Service | Stats type | Counters |
|---------|------------|----------|
| SDO client | `sdo.ClientStats` | uploads, downloads, blockTransfers, bytesUploaded, bytesDownloaded, failed, abortsSent, abortsReceived, timeouts, retries, reacks, ignored |
| SDO server | `sdo.ServerStats` | requests, uploads, downloads, blockTransfers, protocolSwitch, bytesUploaded, bytesDownloaded, abortsSent, abortsReceived, timeouts, restarts, reacks, ignored |
| NMT master | `nmt.MasterStats` | sent |
| NMT slave | `nmt.SlaveStats` | commands, ignored, invalid, bootUps, resets |
| Heartbeat producer | `nmt.HeartbeatProducerStats` | sent, skipped (Initialising), errors |
| Heartbeat consumer | `nmt.HeartbeatConsumerStats` | received, invalid, bootUps, timeouts |
| Guard master / responder | `nmt.GuardStats` | requests, responses, missed, toggleErrors, timeouts, errors |
| TPDO | `pdo.TPDOStats` | sent, sendErrors, readErrors, unchanged, syncs, rtrs, eventTimers |
| RPDO | `pdo.RPDOStats` | received, applied, ignored, short, long, overwritten, writeErrors, timeouts |
| PDO listener (master) | `pdo.ListenerStats` | received, errors |
| SYNC producer / consumer | `syncobj.ProducerStats`, `ConsumerStats` | sent, errors / received, lengthErrors, counterErrors |
| EMCY producer / consumer | `emcy.ProducerStats`, `ConsumerStats` | sent, queued, suppressed, dropped, errors / received, resets, invalid |
| TIME producer / consumer | `timestamp.ProducerStats`, `ConsumerStats` | sent, errors / received, ignored, invalid |
| Node | `node.Stats` | nodeId, state, resets, suppressed (EMCY/SYNC/TIME not sent outside Pre-operational and Operational), and the stats of every service above (TPDO and RPDO keyed by number) |
| Master | `master.Stats` | nmt, heartbeat, emcy, sync, time, and the SDO client stats per node |

## Events

| Source | Handler option | Event |
|--------|----------------|-------|
| `nmt.Slave` | `WithStateHandler`, `WithResetHandler` | `StateChange` (from, to, command), `ResetKind` |
| `nmt.HeartbeatConsumer` | `WithHeartbeatHandler` | `HeartbeatEvent`: boot-up, state-change, timeout, resumed |
| `nmt.GuardMaster`, `GuardResponder` | `WithGuardHandler` | `GuardEvent`: timeout, resumed, state-change, toggle-error, boot-up |
| `emcy.Consumer` | `WithHandler` | `emcy.Event` (node, message, time) |
| `syncobj.Consumer` | `WithHandler` | `syncobj.Event` (counter, length error, window) |
| `timestamp.Consumer` | `WithHandler` | `timestamp.Event` |
| `pdo.RPDO` | `WithErrorHandler`, `WithReceiveHandler` | `pdo.ErrorEvent` (code, PDO, active), received data |
| `node.Node` | `WithStateHandler`, `WithHeartbeatHandler`, `WithTimeHandler` | as the services above |
| `master.Master` | `WithEventHandler` | `master.Event`: boot-up, state-change, heartbeat-lost, heartbeat-resumed, emcy, boot-done, boot-failed |

Handlers run on the service's goroutine and must not block for long. The
master's handler is the exception: its events are queued (unbounded) and
delivered in order on a goroutine of their own, so it may call the master.

## On the wire

`dissect` decodes captured CANopen traffic (for example a go-canbus
capture, or a candump log) into JSON-taggable messages, following SDO
transfers with a `Tracker`: useful to see what a node and a master
exchanged.
