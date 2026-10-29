// SPDX-License-Identifier: MIT

// Package master is a CANopen network manager (an NMT master with the
// services a CiA 302 manager uses): NMT commands, heartbeat supervision,
// emergency reception, SDO clients for every node, network scan, node
// configuration (from entries or a DCF), a boot procedure, remote PDOs,
// SYNC and TIME production and CiA 402 drive control.
//
//	m, _ := master.New(bus, master.WithEventHandler(func(e master.Event) { log.Print(e) }))
//	_ = m.Start()
//	defer m.Close()
//	nodes, _ := m.Scan(ctx, nil)
//	_ = m.Configure(ctx, 5, []master.Entry{{Index: 0x1017, Data: []byte{0xE8, 0x03}}})
//	_ = m.Monitor(5, 1500*time.Millisecond)
//	_ = m.StartNode(ctx, 5)
//
// # Events
//
// Everything the master observes is delivered as an [Event] to the single
// handler of [WithEventHandler]: boot-ups, state changes, heartbeat losses
// and resumptions (of nodes supervised with [Master.Monitor] or a boot
// configuration), emergency messages and the end of boot procedures.
// Events are delivered in order, one at a time, on a goroutine of the
// master's own through an unbounded queue, so the handler may use the
// master (SDO, NMT) without stalling the network services; it must not
// call [Master.Close].
//
// # SDO
//
// [Master.SDO] returns the cached client of a node (default COB-IDs).
// Transfers to one node are serialised by its client; different nodes are
// served in parallel. [Master.Scan] uses short-lived clients with a short
// timeout instead, so that absent nodes do not delay it.
//
// # Configuration
//
// A configuration is a list of [Entry] values (index, sub-index, encoded
// value): the neutral form a DCF ([DCFEntries]), a tool or code produces.
// [Master.Configure] writes it; the entries of PDO parameters are applied
// per PDO with pdo.Configure, so the sequence of CiA 301 (invalidate,
// clear the mapping, map, enable, make valid) holds however the entries
// are ordered. [Master.ConfigureFromDCF] does both steps for a DCF.
//
// # Boot procedure
//
// [Master.SetBoot] registers a [BootConfig] for a node: each boot-up of
// the node then checks its device type and identity, configures it,
// supervises its heartbeat and starts it, in the background, reporting
// EventBootDone or EventBootFailed. [Master.Boot] runs the same procedure
// on demand.
//
// # PDOs
//
// [Master.ListenTPDO] reads a node's TPDO configuration over SDO and
// decodes the PDOs it sends; [Master.RPDOSender] produces a node's RPDO.
// [Master.Listen] and [Master.Sender] take the COB-ID and mapping directly.
// Decoding and encoding need a dictionary describing the node's objects
// (its EDS, or the dictionary it was built from).
//
// # SYNC, TIME, heartbeat
//
// [Master.SYNC] and [Master.TIME] are the master's producers on the
// default COB-IDs; set a SYNC period to drive synchronous PDOs. With
// [WithHeartbeat] the master sends its own heartbeat, for nodes that
// supervise it.
//
// # CiA 402
//
// [Master.Drive] returns a cia402.Controller operating a node's drive
// over the node's SDO client.
package master
