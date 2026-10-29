// SPDX-License-Identifier: MIT

// Package master is a CANopen network manager (an NMT master with the
// services a CiA 302 manager uses): NMT commands, heartbeat supervision,
// emergency reception, SDO clients for every node, network scan, node
// configuration (from entries or a DCF), a boot procedure, remote PDOs,
// SYNC and TIME production, CiA 402 drive control, an LSS master, MPDO
// configuration, CANopen FD (USDO) and the CiA 302 manager.
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
//
// # CiA 302 manager
//
// With [WithDictionary] the master is a CANopen device of its own (a
// node.Node on the given dictionary) and a CiA 302 manager driven by the
// objects of that dictionary ([AddManagerObjects] adds them):
//
//   - 0x1F80 NMT start-up (Startup* bits): bit 0 makes the device the
//     NMT master; the others select whether the slaves are started all
//     at once, whether the master and the slaves are started by the
//     application, and the reaction to an error control event of a
//     mandatory slave (reset or stop all nodes).
//   - 0x1F81 NMT slave assignment per node ([Assignment]): network list,
//     boot, mandatory, keep-alive, software version check and update,
//     restore, and the node guarding parameters.
//   - 0x1F82 request NMT and 0x1F83 request node guarding: writes send
//     NMT commands and start or stop guarding; reads return the node's
//     state and guarding.
//   - 0x1F84..0x1F88 expected identification, 0x1F89 boot time.
//   - 0x1F22 concise DCF, 0x1F26/0x1F27 expected configuration date and
//     time, 0x1F8A restore configuration (CiA 302-3 configuration).
//   - 0x1F55 expected application software identification, 0x1F58
//     program data (CiA 302-3 program download).
//
// At every boot-up of the master's node (Start, and its resets) with bit
// 0 of 0x1F80 set, the network boot-up of CiA 302-2 runs ([Master.BootNetwork]):
// the slaves are reset (communication), and each slave of the network
// list goes through the boot slave process ([Master.BootSlave]) when its
// boot-up arrives (a mandatory slave without one is booted after
// BootTiming.ResetTimeout, a keep-alive slave at once): identity check,
// software check and update ([Master.DownloadProgram]), configuration
// (skipped when the slave's 0x1020 matches 0x1F26/0x1F27; else restore
// and the concise DCF, written in order with [Master.ConfigureConcise],
// optionally followed by a store request, [WithStoreAfterConfigure]),
// start of the error control (heartbeat from 0x1016, else node guarding
// from 0x1F81), then NMT start as 0x1F80 allows. A slave that does not
// answer is retried (a mandatory one within the boot time 0x1F89). Once
// every mandatory slave booted the master enters Operational (unless
// 0x1F80 bit 2) and starts the slaves; a failing mandatory slave halts
// the boot-up. Results are events (EventBootDone, EventBootFailed with a
// *BootError holding the CiA 302 error status letter,
// EventNetworkBootDone, EventNetworkBootFailed); a lost slave
// (heartbeat or node guarding) is reset, or all nodes are reset or
// stopped, as 0x1F80 says (EventNodeError), and booted again when it
// returns.
//
// The manager's objects are in the communication area: an NMT reset of
// the master's node restores their defaults. Give the dictionary its
// configuration as defaults (e.g. built from the manager's DCF), or keep
// it with node.WithStore and save it.
//
// With [WithLSSBoot] the network boot-up starts with an LSS step: the
// master lists the unconfigured devices with LSS Fastscan and gives each
// the node-ID of the slave of 0x1F81 whose expected identity
// (0x1F85..0x1F88) it has (EventLSSAssigned); the devices then reset with
// it and are booted like the others.
//
// The behaviour follows Lely lely-core's NMT master (src/co/nmt.c,
// nmt_boot.c, nmt_cfg.c), CiA 302 being a member document. Not
// implemented: the flying master and redundancy (0x1F80 bit 5 is
// ignored), the SDO manager of CiA 302-5 (dynamic SDO connections), the
// configuration file 0x1F20 (DCF text) and the program objects
// 0x1F52..0x1F54.
//
// # LSS
//
// [Master.LSS] is an LSS master (package lss) started and closed with the
// master: Fastscan, AssignAll, node-ID and bit timing configuration.
//
// # MPDO
//
// [Master.ConfigureMPDO] makes a node's PDO a multiplexed PDO;
// [Master.WriteScannerList] and [Master.WriteDispatcherList] write its
// object scanner and dispatcher lists.
//
// # CANopen FD
//
// With [WithFD] the master sends NMT commands, SYNC and its heartbeat in
// CAN FD frames, accepts CAN FD heartbeats and 20-byte emergencies, and
// reads and writes the nodes' dictionaries with its USDO client
// ([Master.USDO]) in every procedure (scan, configuration, boot
// procedures, the CiA 302 boot-up, program download with bulk transfer,
// remote PDO configuration, CiA 402), since CANopen FD devices serve the
// USDO in place of the SDO; USDO aborts are reported as SDO aborts
// (errors.As finds a *canopen.AbortError). Listen and Sender use CANopen
// FD PDOs.
package master
