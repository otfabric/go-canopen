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
  only what their contract (1.1) exposes is covered: LSS (CANopenNode
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
  processing time t_proc is unknown (`usdo.WithTProcUnit`).
- **CANopen FD emergency layout**: CiA's CANopen FD poster and the
  HMS/Ixxat whitepaper disagree on the positions of the error register
  and the error code; `emcy.FDLayoutCiA` (the default) follows CiA,
  `FDLayoutRegisterFirst` HMS. Both ends must use the same layout; a
  device of the other layout is misread without any error. The status
  byte's encoding is not public (carried raw).
- **MPDO dispatcher entry**: the block size is read at bits 56..63 (Lely
  reads bit 54, apparently a slip; `mpdo` package documentation).
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

- **Segmented SDO cannot detect corruption.** CiA 301 relies on the CAN
  CRC; a corrupted segment is stored as received. Block transfers with
  the CRC detect corruption; even there a corrupted command byte can turn
  a request into another valid one, and a corrupted initiate can disable
  the CRC for that transfer (`sdo` package documentation, "Error
  detection").
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
- Limits apply to integer and float types only.
- `codegen` does not support limits with `$NODEID`.
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
