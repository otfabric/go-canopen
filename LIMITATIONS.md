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
- The interoperability suite against CANopenNode, Lely CANopen and
  python canopen is planned, not written: no result against them exists
  ([INTEROPERABILITY.md](INTEROPERABILITY.md)).
- **Nothing has been tested on real CAN hardware.** Every test runs on
  go-canbus's virtual bus or Linux vcan: no arbitration, bit timing, error
  frames, bus load or controller differences.

## Not implemented

- **CANopen FD** (CiA 1301): no USDO, no CAN FD PDOs (a PDO carries at
  most 64 bits, classical CAN), no CANopen FD object dictionary
  extensions.
- **LSS** (CiA 305): no LSS master or slave. `dissect` decodes LSS frames;
  `node.SetNodeID` sets a pending node-ID as an LSS slave would, applied
  at the next reset.
- **CiA 302** network management beyond the basics in `master` (boot
  procedure, configuration, heartbeat supervision): no flying master, no
  configuration manager with store/restore of configurations (concise
  DCF), no SDO manager (dynamic SDO channels), no request of the NMT
  master's own services by other nodes, no redundancy.
- **MPDO** (multiplexed PDOs: source and destination address mode,
  SAM/DAM) and the object scanner/dispatcher lists.
- **Store and restore parameters** (0x1010, 0x1011): the objects are not
  created by `od.Standard` and nothing is persisted; a reset restores the
  defaults the dictionary was built with.
- **SYNC window enforcement**: the SYNC consumer reports the window
  (0x1007, `Consumer.InWindow`) but synchronous PDOs are not cancelled
  when it ends.
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

- `Scan` reads 0x1000 and 0x1018 by SDO with a short timeout: a node
  whose SDO server is not answering (Stopped) is not found.
- `Configure` writes entries in order with a single SDO channel per node;
  it does not store the configuration in the node (no 0x1010) and does
  not check a configuration date/time (0x1020).
- A boot procedure interrupted by a new boot-up ends without an event;
  only the new procedure reports.
