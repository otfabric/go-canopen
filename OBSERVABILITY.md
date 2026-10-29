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
| `node unconfigured: waiting for a node-ID (LSS)`, `node configured: starting the services`, `node LSS settings stored` | info | node with `WithLSS`: nodeId |
| `node reset: services`, `node reset: stored parameters`, `node: LSS reset communication`, `node: MPDO` | warn | node: deferred services that could not start, stored values not applied, a failed LSS-triggered reset, an MPDO service that could not be created |
| `lss node-ID configured, reset communication`, `lss node-ID assigned` | info | lss (slave, master) |
| `lss response` | warn | lss: a response that could not be sent |
| `master: network boot-up`, `master: mandatory slaves booted`, `master: slave booted`, `master: LSS node-ID assigned`, `master: updating the software of a slave` | info | master (CiA 302 manager): slaves, node, status, address |
| `master: network boot-up halted`, `master: boot slave failed`, `master: error control event of a slave`, `master: LSS Fastscan`, `master: LSS: configure node-ID`, `master: LSS: no single slave of the network list for an unconfigured device` | warn | master (CiA 302 manager) |
| `usdo server: subscription ended`, `resubscribe`, `subscription ended` | error | usdo, mpdo: a service that cannot receive |
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
| TPDO | `pdo.TPDOStats` | sent, sendErrors, readErrors, unchanged, syncs, rtrs, eventTimers, windowMissed (synchronous transmissions discarded at the end of the SYNC window) |
| RPDO | `pdo.RPDOStats` | received, applied, ignored, short, long, overwritten, writeErrors, timeouts, outsideWindow (synchronous PDOs received after the SYNC window) |
| MPDO producer / consumer | `mpdo.ProducerStats`, `ConsumerStats` | sent, sendErrors, refused, inhibited / received, ignored, short, wrongMode, otherNode, notDispatched, noObject, notMappable, writeErrors, applied |
| LSS master / slave | `lss.MasterStats`, `SlaveStats` | sent, responses, unexpected, timeouts, retries, queries, restarts, assigned, conflicts, refusals, inquiries, identifies, selections, activations / received, ignored, responses, sendErrors, selected, nodeIds, refused, stores |
| USDO client / server | `usdo.ClientStats`, `ServerStats` | uploads, downloads, bulk, bytesUploaded, bytesDownloaded, failed, abortsSent, abortsReceived, timeouts, ignored / requests, uploads, downloads, segmented, bulk, bytes, aborts, timeouts, restarts, busy, duplicates, remote (routed requests ignored), ignored, sendErrors |
| PDO listener (master) | `pdo.ListenerStats` | received, errors |
| SYNC producer / consumer | `syncobj.ProducerStats`, `ConsumerStats` | sent, errors / received, lengthErrors, counterErrors |
| EMCY producer / consumer | `emcy.ProducerStats`, `ConsumerStats` | sent, queued, suppressed, dropped, errors / received, resets, invalid |
| TIME producer / consumer | `timestamp.ProducerStats`, `ConsumerStats` | sent, errors / received, ignored, invalid |
| Node | `node.Stats` | nodeId, state, resets, suppressed (EMCY/SYNC/TIME not sent outside Pre-operational and Operational), and the stats of every service above (TPDO, RPDO, mpdoProducer and mpdoConsumer keyed by PDO number; lss with `WithLSS`; usdo with `WithFD`) |
| Master | `master.Stats` | nmt, heartbeat, emcy, sync, time, the SDO client stats per node, lss (the LSS master), usdo (the USDO client of `WithFD` / `WithUSDOClient`) |

## Events

| Source | Handler option | Event |
|--------|----------------|-------|
| `nmt.Slave` | `WithStateHandler`, `WithResetHandler` | `StateChange` (from, to, command), `ResetKind` |
| `nmt.HeartbeatConsumer` | `WithHeartbeatHandler` | `HeartbeatEvent`: boot-up, state-change, timeout, resumed |
| `nmt.GuardMaster`, `GuardResponder` | `WithGuardHandler` | `GuardEvent`: timeout, resumed, state-change, toggle-error, boot-up |
| `emcy.Consumer` | `WithHandler` | `emcy.Event` (node, message, time; `FD` for a 20-byte CANopen FD emergency) |
| `mpdo.Consumer` | `WithReceiveHandler`, `WithErrorHandler` | `mpdo.Event` (PDO, message, local object, data); `pdo.ErrorEvent` (0x8230, 0x8210) |
| `lss.Slave` | `WithStateHandler`, `WithNodeIDHandler`, `WithResetHandler`, `WithBitTimingHandler`, `WithActivateHandler`, `WithStoreHandler` | LSS state, configured node-ID, reset request, bit timing, activation, store |
| `lss.Master.AssignAll` | `AssignHandler` | `lss.Assignment` (address, node-ID) |
| `syncobj.Consumer` | `WithHandler` | `syncobj.Event` (counter, length error, window) |
| `timestamp.Consumer` | `WithHandler` | `timestamp.Event` |
| `pdo.RPDO` | `WithErrorHandler`, `WithReceiveHandler` | `pdo.ErrorEvent` (code, PDO, active), received data |
| `node.Node` | `WithStateHandler`, `WithHeartbeatHandler`, `WithTimeHandler` | as the services above |
| `master.Master` | `WithEventHandler` | `master.Event`: boot-up, state-change, heartbeat-lost, heartbeat-resumed, emcy, boot-done, boot-failed, network-boot-done, network-boot-failed, guarding-lost, guarding-resumed, node-error, lss-assigned (`Event.LSS`: address and node-ID); `Status` holds the CiA 302 boot status |

Handlers run on the service's goroutine and must not block for long. The
master's handler is the exception: its events are queued (unbounded) and
delivered in order on a goroutine of their own, so it may call the master.

## On the wire

`dissect` decodes captured CANopen traffic (for example a go-canbus
capture, or a candump log) into JSON-taggable messages, following SDO
transfers with a `Tracker`: useful to see what a node and a master
exchanged. CAN FD frames are decoded as USDO requests and responses
(`KindUSDORequest`, `KindUSDOResponse`, `Message.USDO`) and 20-byte
CANopen FD emergencies (`EMCY.FD`); LSS commands are named with
`lss.Command.String`.
