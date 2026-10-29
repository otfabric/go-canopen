// SPDX-License-Identifier: MIT

// Package nmt implements CANopen network management (CiA 301 7.2.8 and
// 7.3.2): NMT module control and NMT error control.
//
// Module control:
//   - [Master] sends the NMT commands (start, stop, enter pre-operational,
//     reset node, reset communication) to one node or to all.
//   - [Slave] is a node's NMT state machine: boot-up message and
//     Pre-operational after Start, the transitions the commands request,
//     and the two resets, which restore the dictionary areas (reset node:
//     0x2000..0x9FFF and then 0x1000..0x1FFF; reset communication:
//     0x1000..0x1FFF), apply a pending node-ID and call a reset handler
//     before the new boot-up message.
//
// Error control:
//   - [HeartbeatProducer] sends a node's state every producer heartbeat
//     time (0x1017); a change of the period takes effect at once.
//   - [HeartbeatConsumer] tracks the state of every node, supervises the
//     configured ones (explicitly, or from the consumer heartbeat time
//     entries of 0x1016) and reports boot-ups, state changes, timeouts
//     (once per loss) and resumption. [HeartbeatConsumer.WaitState] and
//     [HeartbeatConsumer.WaitBootUp] help an NMT master sequence its
//     commands.
//   - [GuardMaster] and [GuardResponder] implement the legacy node
//     guarding and life guarding protocol (remote frames with a toggling
//     state response).
//
// Every long-running service has Start, which subscribes before it
// returns so that no frame is missed afterwards, and Close, which stops it
// and waits for its goroutine. Callbacks (handler options) run on the
// service's goroutine and must not block for long. Services configured
// [WithDictionary] read their parameters from the standard objects and
// follow writes of them, as an SDO server makes them, while running.
package nmt
