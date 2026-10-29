# API overview

The main types and calls of each package. The reference is
[pkg.go.dev](https://pkg.go.dev/github.com/otfabric/go-canopen); runnable
programs are in [examples/](examples) and the `Example` functions.

Conventions shared by every package:

- Services are created with `NewX(bus, ..., opts...)` and run between
  `Start()` and `Close()`; `Stats()` returns a counter snapshot.
- Options are functional options (`WithLogger`, `WithDictionary`,
  `WithHandler`, ...). A service given `WithDictionary(d)` reads its
  parameters from the standard objects of `d` and follows their changes.
- Calls that use the bus take a `context.Context`.
- Errors are described in [ERRORS.md](ERRORS.md).

## `canopen` (core)

| Type / function | Purpose |
|-----------------|---------|
| `Bus` | `Send` + `Subscribe`; implemented by `*canbus.Bus` |
| `NodeID` | 1..127; `Check`, `Valid`; `BroadcastNodeID` (0) for NMT |
| `COBID` | CAN-ID plus flag bits: `ID`, `Extended`, `Valid`, `Filter`, `NewCOBID`; `COBInvalid` (31), `COBGenerate` (30), `COBConsume` (31, TIME), `COBExtended` (29) |
| `Classify(id)` | Service, node-ID and PDO number of a CAN-ID in the predefined connection set; `DefaultTPDO`, `DefaultRPDO`; `FuncNMT` .. `FuncHeartbeat` |
| `DataType` | CiA 301 data types (`Boolean` .. `Unsigned64`, `Real32`, `Real64`, `VisibleString`, `OctetString`, `UnicodeString`, `Domain`, `TimeOfDay`, `TimeDifference`, ...); `Size`, `IsInteger`, `String` |
| `Encode`, `Decode`, `EncodeInt`, `DecodeUint`, `DecodeInt` | Little-endian value encoding with range checks |
| `ParseValue`, `FormatValue`, `ParseInt`, `ParseUint` | EDS literals (decimal, `0x`, octal, `$NODEID+x`) |
| `AbortCode`, `*AbortError` | SDO abort codes (`Text`, `Error`) and an abort with object and side |
| `NMTState`, `NMTCommand` | NMT states and commands |
| `ErrInvalidNodeID`, `ErrTimeout`, `ErrClosed`, `ErrDataType`, `ErrProtocol`, `ErrBusy` | Sentinels |

## `od`

| Type / function | Purpose |
|-----------------|---------|
| `New(node)`, `Standard(Config)` | Empty dictionary; dictionary with the CiA 301 communication objects (`Config`: identity, device name, heartbeat, consumers, error history, PDO counts, SDO clients, node guarding, TIME) |
| `NewVar`, `NewRecord`, `NewArray`, `Sub` | Build objects; `Dictionary.Add`, `Remove` |
| `Object`, `Variable`, `ObjectType`, `Access` | The model: index, sub-index, type, access, PDO mappability, default (`NodeIDRelative`), limits, `MaxSize` |
| `Read`, `Write` | Remote access: access rights, limits, hooks; refusals are `canopen.AbortCode` |
| `Get`, `Set`, `GetValue`, `SetValue`, `GetUint`, `SetUint` | Local access (application) |
| `OnRead`, `OnWrite`, `Watch` | Hooks and watchers; each returns its remove function |
| `HookCount` | Number of hooks and watchers registered (diagnostic) |
| `Reset(from, to)`, `SetNodeID`, `DefaultValue` | Defaults and resets |
| `Index*` constants | The standard object indices (`IndexDeviceType` .. `IndexTPDOMapping`) |

## `sdo`

| Type / function | Purpose |
|-----------------|---------|
| `NewClient(bus, node, opts...)` | Client for a node's default SDO channel, or `WithCOBIDs` |
| `Client.Upload`, `Client.Download` | Raw transfers; per call `UseBlockMode` |
| `ReadUint`, `ReadInt`, `ReadString`, `ReadValue`, `WriteUint8/16/32`, `WriteUint`, `WriteValue` | Typed helpers |
| `NewServer(bus, d, opts...)` | Server for a dictionary (COB-IDs from 0x1200 or defaults); `SetEnabled`, `COBIDs` |
| Options | `WithTimeout`, `WithLogger`, `WithCOBIDs`, `WithBlockSize`, `WithCRC`, `WithMaxSize` (both); `WithBlockMode` (`BlockNever`, `BlockAlways`, `BlockAuto`), `WithBlockThreshold`, `WithProtocolSwitch`, `WithRetries` (client); `WithResetFence` (server: no answer after an NMT reset command, used by `node`); `Server.Fence` (ignore requests received earlier, by receive timestamp) |
| `CRC16` | The block transfer CRC (CRC-16-CCITT) |
| `ClientStats`, `ServerStats` | Counters |

## `nmt`

| Type / function | Purpose |
|-----------------|---------|
| `NewMaster(bus)` | `Send(ctx, node, cmd)`: NMT commands (node 0 = all) |
| `NewSlave(bus, node, opts...)` | State machine: `Start` (boot-up), `State`, `SetState`, `Reset(ctx, ResetApplication or ResetCommunication)` (reset node, reset communication), `SetNodeID` (pending); `WithStateHandler`, `WithResetHandler`, `WithBootUpHandler` (just before every boot-up message); node-ID `Unconfigured` (0xFF): no boot-up until a reset applies a pending node-ID |
| `WithFD(brs)` | CANopen FD: commands, boot-up and heartbeats in CAN FD frames, CAN FD frames accepted (master, slave, heartbeat producer and consumer) |
| `NewHeartbeatProducer(bus, node, stateFunc, opts...)` | Heartbeat every `WithHeartbeatPeriod` or 0x1017; `SetPeriod`, `SetNodeID` |
| `NewHeartbeatConsumer(bus, opts...)` | State of every node; supervision (`Monitor`, or 0x1016); `State`, `Status`, `Nodes`, `WaitState`, `WaitBootUp`, `Restart`; `HeartbeatEvent` (boot-up, state change, timeout, resumed) |
| `NewGuardMaster`, `NewGuardResponder` | Node guarding and life guarding; `GuardEvent` |
| `CommandFrame`, `ParseCommand`, `ParseCommandFD`, `ErrorControlFrame`, `ParseErrorControl`, `ParseErrorControlFD`, `ToFD`, `GuardRequest`, `ConsumerEntry`, `ParseConsumerEntry` | Frame and entry codecs (the `FD` parsers also accept CAN FD frames) |

## `pdo`

| Type / function | Purpose |
|-----------------|---------|
| `Entry`, `Mapping`, `Pack`, `Unpack`, `PackFD`, `UnpackFD`, `ValidateEntry` | Mapping entries and bit-exact packing (≤ 64 bits; FD ≤ 512 bits) |
| `Config`, `TransmissionType`, `Load`, `CommIndex`, `MappingIndex` | A PDO's configuration and its objects |
| `NewTPDO(bus, d, n, opts...)` | Transmit PDO: `Start`, `SetEnabled`, `OnSync`, `OnSyncWindow` (SYNC window end), `Trigger`, `Transmit`, `Config`, `Stats` |
| `NewRPDO(bus, d, n, opts...)` | Receive PDO: writes mapped objects, deadline monitoring, `OnSync`, `OnSyncWindow`; `WithErrorHandler` (`ErrorEvent`), `WithReceiveHandler` |
| `WithFD(brs)`, `WithClock` | CANopen FD PDOs (CAN FD frames, up to 64 bytes, no RTR); the clock of the SYNC window |
| `Protect(d, transmit, n)`, `ProtectFD` | Installs the CiA 301 reconfiguration rules; returns `unprotect` |
| `Configure`, `ReadConfig` | Master side: write/read a node's PDO over an `SDO` |
| `NewDecoder`, `NewEncoder`, `NewListener`, `NewSender` | Master side: decode a node's PDOs into `Value`s, produce PDOs |

## `sync` (package `syncobj`), `emcy`, `timestamp`

| Type / function | Purpose |
|-----------------|---------|
| `syncobj.NewProducer` | SYNC every `WithPeriod` (or 0x1006), counter (`WithOverflow`, 0x1019); `SetPeriod`, `SetOverflow`, `Send`, `Producing` |
| `syncobj.NewConsumer` | SYNC `Event`s (counter, length error, window end); `WithHandler`, `WithWindow`, `Window`, `InWindow` |
| `syncobj.WithFD(brs)` | SYNC in CAN FD frames (producer), CAN FD frames accepted (consumer) |
| `emcy.NewProducer(bus, node, opts...)` | `Raise`, `Clear`, `ClearAll`, `ClearHistory`, `Send`, `SendFD`, `Register`, `Active`, `History`; `WithInhibit`, `WithHistory`, `WithCOBID`; CANopen FD: `WithFD`, `WithFDInfo`, `WithFDLayout`, `WithFDClock` |
| `emcy.NewConsumer(bus, opts...)` | Emergency `Event`s of every node (or `WithProducer`), `Last`; with `WithFD` also 20-byte FD emergencies (`Event.FD`) |
| `emcy.FDMessage`, `ParseFDMessage`, `ParseFDFrame`, `FDLayout` | The 20-byte CANopen FD emergency |
| `emcy.CodeText`, `ErrorRegister`, `Message` | Error codes (CiA 301 table 21), register bits, message codec |
| `timestamp.NewProducer`, `NewConsumer` | TIME on request (`Send`) or every `WithPeriod`; `Event`s |
| `timestamp.TimeOfDay`, `TimeDifference`, `FromTime`, `Frame`, `ParseFrame` | CiA 301 time types |

## `node`

| Type / function | Purpose |
|-----------------|---------|
| `New(bus, d, opts...)` | A device from a dictionary ([ARCHITECTURE.md](ARCHITECTURE.md#node-node)) |
| `Start`, `Close` | Boot-up and services; stop and remove hooks |
| `State`, `SetState`, `Reset`, `NodeID`, `SetNodeID` | Local NMT control |
| `EMCY`, `HeartbeatConsumer`, `SYNCProducer`, `TPDO(i)`, `RPDO(i)`, `MPDOProducer(i)`, `MPDOConsumer(i)`, `LSS`, `USDOServer`, `Program`, `Drive`, `Dictionary`, `SendTime` | Access to the services |
| `SaveParameters(group)`, `RestoreDefaults(group)`, `StoredLSS` | Store and restore (0x1010, 0x1011), the LSS settings found in the store |
| `Stats` | Counters of every service |
| Options | `WithLogger`, `WithStateHandler`, `WithHeartbeatHandler`, `WithTimeHandler`, `WithTimePeriod`, `WithSDOServerOptions`, `WithDrive`, `WithAutoStart`, `WithStore`, `WithProgram`, `WithoutHeartbeatConsumer`, `WithLSS(lss.SlaveOption...)` (LSS slave; node-ID 0xFF starts unconfigured), `WithFD(brs)` (CANopen FD: FD PDOs, SYNC, EMCY, NMT, USDO server instead of SDO), `WithUSDOServerOptions`, `WithEMCYOptions` |

## `master`

| Type / function | Purpose |
|-----------------|---------|
| `New(bus, opts...)`, `Start`, `Close` | The manager; `WithEventHandler`, `WithLogger`, `WithSDOOptions`, `WithScanOptions`, `WithScanParallelism`, `WithHeartbeat`, `WithDictionary` (CiA 302 manager), `WithBootTiming`, `WithStoreAfterConfigure`, `WithFD` (CANopen FD, USDO for every procedure), `WithUSDOClient`, `WithLSSOptions`, `WithLSSBoot` (LSS step of the network boot-up) |
| `NMT`, `StartNode`, `StopNode`, `PreOperational`, `ResetNode`, `ResetCommunication` | NMT commands |
| `Monitor`, `State`, `Status`, `Nodes`, `WaitState`, `WaitBootUp`, `LastEMCY` | Supervision |
| `SDO(node)`, `USDO()`, `LSS()` | The node's cached `*sdo.Client`; the master's `*usdo.Client`; the master's `*lss.Master` |
| `Scan`, `Identity` | Find nodes (`NodeInfo`), read 0x1018 |
| `Configure(ctx, node, []Entry)`, `ConfigureFromDCF`, `DCFEntries`, `ConfigurePDO`, `ReadPDO`, `Overlay` | Configuration |
| `SetBoot`, `Boot`, `BootConfig` | Boot procedure (`EventBootDone`, `EventBootFailed`) |
| `AddManagerObjects`, `Node`, `BootNetwork`, `BootSlave`, `SlaveStatus`, `Assignment`, `NewAssignment`, `BootStatus`, `BootError`, `BootTiming` | CiA 302 manager: objects 0x1F80..0x1F89, 0x1F22, 0x1F26/27, 0x1F55, 0x1F58, 0x1F8A; network boot-up and boot slave process |
| `ConfigureConcise`, `StoreParameters`, `RestoreDefaults`, `DownloadProgram`, `ProgramOptions` | Concise DCF download, store/restore requests, program download (CiA 302-3) |
| `ConfigureMPDO`, `WriteScannerList`, `WriteDispatcherList` | MPDO configuration of a node |
| `ListenTPDO`, `RPDOSender`, `Listen`, `Sender` | Remote PDOs |
| `SYNC`, `TIME`, `Drive` | Producers; a `cia402.Controller` for a node |
| `Event`, `EventKind`, `Stats` | Observations and counters |

## `lss`

| Type / function | Purpose |
|-----------------|---------|
| `NewMaster(bus, opts...)` | `SwitchStateGlobal`, `SwitchStateSelective`, `Configure(ctx, addr, fn)`, `ConfigureNodeID`, `SetNodeID`, `SetBitTiming`, `SetBitTimingTable`, `ActivateBitTiming`, `Store`, `Inquire`, `InquireAddress`, `InquireNodeID`, `Identify`, `IdentifyNonConfigured`, `Fastscan`, `FastscanSelect`, `AssignAll` (`AssignAvoid`, `AssignMax`, `AssignStore`, `AssignFastscan`, `AssignHandler`); `WithTimeout`, `WithRetries`, `WithInhibit`, `WithFastscanTimeout`, `WithFastscanRetries` |
| `NewSlave(bus, addr, node, opts...)` | LSS state machine of a device; `WithNodeIDHandler`, `WithResetHandler`, `WithBitTimingHandler`, `WithActivateHandler`, `WithStoreHandler`, `WithStateHandler`; `SetNodeID`, `Pending`, `State` |
| `Address`, `AddressFromDictionary`, `Part`, `BitTiming`, `BitTimingFor`, `Settings`, `Command`, `Unconfigured` | The model |

## `mpdo`

| Type / function | Purpose |
|-----------------|---------|
| `NewProducer(bus, d, n, opts...)` | TPDO n as MPDO: `SendDAM(ctx, dest, index, sub, value)`, `Send(ctx, index, sub)` (SAM, scanner list checked) |
| `NewConsumer(bus, d, n, opts...)` | RPDO n as MPDO: writes DAM values and dispatched SAM values; `WithReceiveHandler`, `WithErrorHandler` |
| `ScannerEntry`, `DispatcherEntry`, `NewScannerList`, `NewDispatcherList`, `ProtectLists`, `ProtectPDO`, `Numbers`, `ModeOf`, `Dispatch`, `Scanned` | Lists, validation and configuration helpers |
| `Message`, `ParseMessage` | The 8-byte MPDO frame |

## `usdo`

| Type / function | Purpose |
|-----------------|---------|
| `NewClient(bus, node, opts...)` | `Upload`, `Download` (one server), `UploadMulti`, `DownloadMulti` (a group or all); per call `UseBulk`, `Expect`, `WithCallTimeout`, `WithDataType` |
| `NewServer(bus, d, opts...)` | Serves a dictionary to every client; `SetEnabled`, `SetNodeID`, `Sessions`; `WithGroups`, `WithMaxSessions`, `WithMaxSize`, `WithProcessingTime`, `WithTProcUnit` |
| `AbortCode`, `AbortError`, `FromSDO` | One-byte USDO aborts and their SDO equivalents |
| `Message`, `Decode`, `Frame`, `Command`, `Address` | The protocol |

## `store`, `cdcf`, `program`

| Type / function | Purpose |
|-----------------|---------|
| `store.Store`, `NewMemory`, `NewFile`, `AddObjects`, `Snapshot`, `Apply`, `Merge`, `Without`, `Range`, `Parameter` | Parameter persistence for `node.WithStore` |
| `cdcf.Encode`, `Decode`, `FromFile`, `Entries`, `PDOSequence`, `Size` | The concise DCF (0x1F22) |
| `program.AddObjects`, `Attach`, `Config`, `Handler`, `Device`, `FlashStatus` | Program download objects 0x1F50, 0x1F51, 0x1F56, 0x1F57 on the device side |

## `eds`

| Type / function | Purpose |
|-----------------|---------|
| `Parse`, `ParseBytes`, `ParseFile` | Read an EDS or DCF into a `File` |
| `File.Validate` | `[]Diagnostic` (`Error`, `Warning`); `HasErrors` |
| `Write` | Canonical, deterministic output (round trip) |
| `File.Dictionary(node)`, `File.BuildDictionary(node, BuildOptions)` | To an `od.Dictionary` (strict or lenient) |
| `FromDictionary(d, ExportOptions)` | From a dictionary (EDS or DCF) |

## `codegen`

`Generate(f *eds.File, Options) ([]byte, error)`: a Go package for an EDS
or DCF with index and sub-index constants, `Objects` and `NewDictionary`
(no parsing at run time), a typed local accessor over an `od.Dictionary`
and a typed remote accessor over an SDO client; deterministic and gofmt'd.
`Ident` makes Go identifiers from names.

## `cia402`

| Type / function | Purpose |
|-----------------|---------|
| `State`, `Command`, `Mode`, `Next`, `Path`, `Transitions`, `DecodeStatusword`, `DecodeControlword` | The profile |
| `Objects`, `AddObjects` | The common 0x6xxx objects |
| `NewDrive(d, DriveConfig)` | Simulated drive on a dictionary: `Step`, `Run`, `InjectFault`, `State`, `Position`, `Close` |
| `NewController(SDO, *ControllerOptions)` | Remote drive control: `Enable`, `Disable`, `QuickStop`, `FaultReset`, `SetMode`, `MoveTo`, `SetVelocity`, `SetTorque`, `Home`, `WaitTargetReached`; errors are `*Error` |

## `dissect`

`Decode(f) Message` (stateless) and `NewTracker().Decode(f)` (follows SDO
transfers) turn frames into `Message` values with one service field
(`NMT`, `SYNC`, `TIME`, `EMCY` (with `FD` for CANopen FD), `PDO`, `SDO`,
`USDO`, `ErrorControl`, `LSS`) and JSON tags.
