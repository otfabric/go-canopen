# Limitations

What go-canopen does not do, or does differently from what one might
expect. Grouped into gaps and follow-ups, with what is not verified:
[ROADMAP.md](ROADMAP.md).

## Standards and conformance

- The CiA specifications (CiA 301, 302, 305, 306, 402, 1301) are
  documents of CAN in Automation, available to members. go-canopen is
  implemented from public material and the behaviour of open-source
  stacks; there is **no CiA conformance claim**, no conformance test run
  and no certification.
- Interoperability is tested against CANopenNode, Lely CANopen and
  python canopen on Linux vcan through the canopen-interop images
  ([INTEROPERABILITY.md](INTEROPERABILITY.md), with the known differences);
  only what their contract (1.2) exposes is covered: LSS (CANopenNode
  and Lely slaves, all three masters), MPDOs (Lely only), the CiA 302
  boot-up (go-canopen's manager with every stack's device; Lely's boot
  master), concise DCFs (Lely's generators) and the SYNC window (python
  canopen's RPDO sender; the RPDOs of CANopenNode's and Lely's devices)
  are; CANopen FD, store/restore, program download, TIME, node guarding
  and SRDO are not, and CiA 402 only against python canopen's
  BaseNode402.
- **No interoperability oracle for CANopen FD.** The reference stacks of
  canopen-interop (CANopenNode v4, Lely CANopen 2.3.5, python canopen
  2.4.1) do not implement CANopen FD in the versions used, and no
  independent USDO implementation or complete public trace was available
  to test against: the USDO, the FD emergency and the FD PDOs are tested
  against go-canopen itself only.
- **Nothing has been tested on real CAN hardware.** Every test runs on
  go-canbus's virtual bus or Linux vcan: no arbitration, bit timing, error
  frames, bus load or controller differences.

## Not implemented

- **CiA 302 beyond the network boot-up, configuration and program
  download**: no flying master and no redundancy (0x1F80 bit 5 is
  ignored), no SDO manager (CiA 302-5: dynamic SDO connections, 0x1F10
  and the SDO client channels it hands out), no DCF storage in 0x1F20
  (the concise DCF 0x1F22 is supported), no program download objects
  0x1F52..0x1F54 (verify application software, date and time). The public
  sources were insufficient to implement the flying master and the SDO
  manager without guessing their protocols.
- **CANopen FD**: remote (routed, 81h..FEh) USDO services are not
  supported (their layout with network-IDs is not public; a server
  ignores and counts them), nor transfers of several objects in one
  service, nor segmented transfers to several servers (bulk is used). The
  CiA 1301 object dictionary changes beyond the PDO length are not
  implemented. TIME and MPDOs stay classical frames on a CANopen FD
  network; node guarding does not exist there (no remote frames).
- **LSS**: the "slowscan" binary search over identify remote slave (Lely)
  is not implemented (Fastscan is); LSS bit timing is passed to the
  application's handlers, the stack never changes the bit rate itself.
- **One SDO server channel** per node (0x1200). Additional server
  channels (0x1201..) are not served, and the SDO client parameter
  objects (0x1280.., `od.Config.SDOClients`) are created but not used by
  the node.
- **Dynamic object dictionaries**, generic pre-defined connection set
  variants and the device profiles other than CiA 402 (CiA 401 I/O, 406
  encoders, 4xx...): applications define those objects themselves.
- **Error behaviour** 0x1029: only sub-index 1 (communication error) is
  applied by `node`.
- **Emergency consumer object** 0x1028 (the EMCY consumers a node
  configures) is not interpreted; `emcy.Consumer` takes its COB-IDs from
  options.

## Interpretations (public sources incomplete)

- **USDO segmented transfers**: the public sources show the bulk and
  expedited layouts only; the segmented initiate, segment, confirmation
  and end layouts and the counter rules are inferred by analogy (`usdo`
  package documentation, "Inferred"). They make go-canopen's client and
  server interoperate, not necessarily other stacks. The unit of the bulk
  processing time t_proc is unknown (`usdo.WithTProcUnit`); the client
  waits at most its response timeout between two bulk segments, whatever
  t_proc a server reports.
- **CANopen FD emergency layout**: CiA's CANopen FD poster and the
  HMS/Ixxat whitepaper disagree on the positions of the error register
  and the error code; `emcy.FDLayoutCiA` (the default) follows CiA,
  `FDLayoutRegisterFirst` HMS. Both ends must use the same layout; a
  device of the other layout is misread without any error. The status
  byte's encoding is not public (carried raw).
- **MPDO dispatcher entry**: the block size is read at bits 56..63 (Lely
  reads bit 54, apparently a slip; `mpdo` package documentation).
- **LSS** configure node-ID is addressed to every slave in the
  configuration state, and all slaves answer on one identifier: a master
  cannot tell an answer lost on the way from a slave that did not answer.
  `AssignAll` switches the slaves to waiting and checks with inquire
  node-ID before each selection, again after a lost selection
  acknowledgement, and inquires the whole address after it. A slave that
  stays in the configuration state through all of that (its waiting
  switches lost and its answers to every check lost) would take the next
  node-ID too. CAN retransmits a frame until it is received, so this needs
  a link that loses frames, as the chaos suite injects.
- **LSS**: where CANopenNode and Lely differ (order of switch state
  selective, Fastscan answered by unconfigured devices only, inquire
  node-ID answers the active node-ID, bit timing refusal codes), the
  `lss` package documentation lists the choice made.
- **CiA 302 boot-up**: the order and the error status letters A..O follow
  Lely lely-core's NMT master (CiA 302-2 is a member document). The LSS
  step of the boot-up (`master.WithLSSBoot`) assigns node-IDs by matching
  the expected identity (0x1F85..0x1F88); it does not check that a
  configured device already uses the node-ID. `lss.Master.Configure`
  confirms that only the addressed slave is selected before it
  configures, but a wrong assignment that happened anyway (two devices
  with one node-ID) is not repaired by a later boot-up: the LSS step only
  sees unconfigured devices.
- **Boot slave process retries**: SDO requests that time out are
  repeated (`BootTiming.SDORetries`), including writes of the concise DCF;
  an entry whose write is not idempotent (a command object) could be
  executed twice when only its confirmation was lost.

## Behaviour to be aware of

- **Evidence from real devices is recorded traffic of Copley drives
  only** ([testdata/captures](testdata/captures/README.md)): expedited SDO,
  node guarding, PDOs, EMCY, SYNC and CiA 402 state changes of three
  drives. Segmented and block SDO, SDO aborts, heartbeat, LSS, MPDO,
  TIME and CANopen FD have no recording of a real device; their
  independent evidence is the interop suite against software stacks.
- **`dissect.Decode` cannot tell a node guarding answer with the toggle
  bit clear from a heartbeat**: the frames are identical. Use a
  `dissect.Tracker`, which sees the request before it.
- **`dissect.Tracker` learns PDO COB-IDs from SDO traffic only**:
  11-bit COB-IDs of 0x1400..0x15FF and 0x1800..0x19FF sub-index 1 in
  expedited transfers it sees. A configuration made before the recording,
  by a DCF or by a segmented transfer is not known; PDO mappings are not
  decoded.
- **CiA 402 shortcut.** Recorded Copley drives go from Switch on disabled
  to Operation enabled on a single Enable operation controlword. `cia402.Drive`
  follows the state diagram (Shutdown, Switch on, Enable operation) and
  stays in Switch on disabled; with `StrictControlword` it refuses the
  write. A controller driving such a device works either way;
  `cia402.Path` gives the commands the diagram needs.

- **Node-relative defaults must fit node-ID 127.** `od.Dictionary.Add`
  refuses an `od.Variable` with `NodeIDRelative` whose default plus 127
  exceeds its data type (UNSIGNED8 0x81 and above), so no valid node-ID
  wraps it. With a node-ID above 127 (LSS "unconfigured", 0xFF) a default
  whose sum would overflow keeps its plain value.
- **Fastscan silence is "no".** A query unanswered within its window
  counts as no match, so an answer later than the window can hide a
  device. Where silence contradicts an earlier answer (a selection or a
  listed prefix), the step is repeated with the window doubled, up to
  8 times `WithFastscanTimeout`; a device that is slow on every answer
  needs a longer `WithFastscanTimeout`.

- **Segmented SDO cannot detect corruption.** CiA 301 relies on the CAN
  CRC; a corrupted segment is stored as received. Block transfers with
  the CRC detect corruption; even there a corrupted command byte can turn
  a request into another valid one, and a corrupted initiate can disable
  the CRC for that transfer (`sdo` package documentation, "Error
  detection").
- **A failed download can still be applied later on a reordering bus.**
  SDO and USDO servers act on each request when it arrives. When the bus
  holds back the last request of a download (an expedited download, the
  last segment) and loses the client's abort, the client reports the
  download as failed, and the request reaches the server after the
  client's next request: the server stores the failed download's data
  after the next transfer started. An upload that follows returns that
  data. CiA 301 and CiA 1301 have no sequence number across transfers to
  detect it; USDO session-IDs do not help, because the late request
  carries its own session-ID, which the server accepts. A device that
  must not see a value twice or late needs an application check (a read
  back, a counter). The `usdo` chaos test accepts exactly this case.
- SDO interpretations where CiA 301 leaves room (a new initiate abandons
  a running transfer, out-of-sequence block segments are ignored, short
  frames are zero-padded): listed in the `sdo` package documentation.
- PDO reconfiguration follows CANopenNode v4 with deviations: 29-bit
  COB-IDs accepted, sub-byte mappings supported, mapping lengths shorter
  than the data type refused, dummy entries refused in TPDOs (`pdo`
  package documentation).
- SYNC timing: the producer uses a `time.Ticker`; every SYNC is subject
  to Go runtime and OS scheduling latency (tens to hundreds of
  microseconds, more under load). Suited to cycle periods of
  milliseconds, not tight motion control loops. The same applies to
  heartbeats, inhibit times and event timers.
- TIME is converted in UTC (CiA 301 defines no time zone).
- The CiA 402 `Drive` is a deterministic simulation (a virtual axis with
  trapezoidal profiles) for tests and examples, not a motor controller.
  Its supported modes are PP, PV, PT, HM, CSP, CSV, CST.
- **Unconfigured nodes** (`node.WithLSS` with node-ID 0xFF) build their
  services at `New` but start them only when LSS (or `SetNodeID` and a
  reset) gives them a node-ID; until then they send nothing and execute
  only NMT reset commands. A node made unconfigured again by LSS keeps its
  services running but silent (NMT Initialising). The LSS settings are
  stored under the pseudo index 0x0000 of the parameter store.
- **CANopen FD nodes** serve the USDO instead of the SDO (both answer on
  580h + node-ID); a classical SDO client gets no answer from them.
  `master.WithFD` makes every master procedure use the USDO; `Master.SDO`
  still returns classical clients for classical devices.
- **SYNC window**: measured from the reception of each SYNC (its frame
  timestamp, or the time it was processed), so the Go scheduling latency
  of the consumer counts against the window.
- A node's SDO server is enabled just before its boot-up message (so
  that a master's immediate request is answered); if that message
  cannot be sent the node stays Initialising with the server enabled.
  The USDO server of a CANopen FD node is enabled at the same moment. It
  ignores the requests it processes before that; unlike the SDO server,
  which goes by the receive timestamp of a request, it answers a request
  that arrived just before the boot-up message when it gets to it only
  afterwards.
- **A node answers node guarding while it produces a heartbeat.** A node
  whose dictionary has 0x100C and 0x100D runs the guarding responder
  whatever 0x1017 holds, although CiA 301 has a device use one error
  control service only (`nmt.GuardResponder` says so). Configure the
  heartbeat (0x1017) or the guarding by the master, not both; making the
  responder silent while 0x1017 is not 0 is planned for v0.6.0.
- **Error behaviour 0x1029** changes the state from Pre-operational or
  Operational only: a node that is Initialising (resetting, or waiting
  for a node-ID) or Stopped stays as it is when a supervised node is
  lost. The state is read and then set in two steps: a reset that starts
  in between is not held back.
- **Close from a callback.** A handler, and a dictionary hook or watcher
  run by a service, may close that service, its node or the master: Close
  then does not wait for the goroutine the callback runs on. A handler
  that runs inside `Start` (the NMT state handler of the boot-up) closes
  the node or slave when `Start` is done, and `Start` returns `ErrClosed`.
  Cases that still wait for ever, and are the application's to avoid: a
  callback that blocks; two callbacks on different goroutines that each
  close a service the other runs on; a dictionary hook that closes its
  node while an NMT reset of that node is replacing the SDO server the
  hook runs on (the reset waits for the hook, and Close for the reset).
- **Callbacks that do not return normally** are stopped and logged
  (`callback did not return normally`, OBSERVABILITY.md) where the
  library calls them on its own goroutines, and in the handler calls of
  `Start`, `SetState` and `Reset`. Not stopped: a dictionary hook or
  watcher run by the application's own `Read`, `Write`, `Set` or `Reset`
  call (it ends that call); the callbacks of a `cia402.Drive` used
  without a node; a `canopen.Bus` implementation; a `program.Handler` or
  `store.Store` called outside a remote access (`New`, `SaveParameters`,
  `RestoreDefaults`).
- **Operations racing Close.** A single frame an application sends itself
  (`emcy.Producer.Send`, `syncobj.Producer.Send`, `timestamp.Producer.Send`,
  `nmt.Master`, a `pdo.Sender`) and that is already being sent when Close
  is called is still sent, within its send timeout of 1 s; calls made
  after Close fail with `ErrClosed`. An SDO or USDO transfer, a TPDO
  `Transmit`, an MPDO waiting for its inhibit time, an NMT reset, a
  network boot-up, a boot slave process and a scan end with `ErrClosed`.
- The heartbeat consumer restarts its supervision at a reset
  communication (each supervised node waits for its first heartbeat
  again); a node lost before the reset is no longer reported as lost and
  its resumption is not reported.

## EDS and DCF (CiA 306)

- Modular-device sections (`[SupportedModules]`, `[MxSubExtxxxx]`,
  ...), tool sections and vendor extensions are kept verbatim in
  `File.Unknown`, **not interpreted**: no modular device is assembled.
- Objects below 0x1000 (DEFTYPE and DEFSTRUCT definitions) are skipped:
  variables of custom or structured data types cannot be held.
- Comments, blank lines, key order in standard sections, the spelling of
  numbers and duplicate keys (the last wins) are not kept by a round trip.
- Text is kept as the file's bytes (no Latin-1 conversion).
- Limits apply to integer and float types only. A float value or
  default may be `NaN`, `Inf` or `-Inf`; a float limit may be infinite
  but not NaN (an error of `Validate` and of the build). A finite REAL32
  literal beyond the range of REAL32 is an error, not rounded to ±Inf.
- Files are read up to 16 MiB, and a dictionary is built with at most
  65536 sub-objects (compact storage counted expanded); both bounds are
  fixed.
- `codegen` does not support limits with `$NODEID`.
- A mappable VISIBLE_STRING, OCTET_STRING or UNICODE_STRING (the Copley
  Accelnet EDS has 15) is a warning: CiA 306 allows it, but this
  library's PDOs map fixed-length types only (`pdo.ValidateEntry`). A
  mappable DOMAIN is an error.
- An `NrOfRXPDO`/`NrOfTXPDO` that does not match the PDO objects is a
  warning, not an error (as Lely CANopen and python canopen accept such
  files).

## Master

- `Scan` reads 0x1000 and 0x1018 by SDO with a short timeout, repeated
  once: a node whose SDO server is not answering (Stopped) is not found,
  and neither is one that misses both tries; a node that is not found is
  not an error.
- `Configure` writes entries in order with a single SDO channel per node;
  the CiA 302 boot-up checks the configuration date and time (0x1020
  against 0x1F26/0x1F27) and stores the configuration with
  `WithStoreAfterConfigure`; `Configure` itself does neither.
- A boot procedure interrupted by a new boot-up ends without an event;
  only the new procedure reports.
- `BootSlave` is cancelled by a network boot-up that starts while it
  runs (the reset of that boot-up would undo what it configured): it
  returns an error that matches `context.Canceled`, and the network
  boot-up boots the slave again.
- An in-progress `BootNetwork`, `BootSlave` or `Scan` ends at `Close`
  with an error that matches both `canopen.ErrClosed` and
  `context.Canceled` (the latter is what they returned before v0.5.4).
