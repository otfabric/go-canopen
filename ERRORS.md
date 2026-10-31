# Errors

Sentinels are wrapped with detail: test with `errors.Is`, extract
structure with `errors.As`.

## Sentinel errors

| Error | Package | When |
|-------|---------|------|
| `ErrInvalidNodeID` | canopen | Node-ID outside 1..127 |
| `ErrTimeout` | canopen | A protocol timeout: an SDO response that did not come, a CiA 402 step that did not complete. A context deadline is reported as the context's error instead |
| `ErrClosed` | canopen | Operation on a closed service, client, node or master; ends pending waits on Close |
| `ErrDataType` | canopen | A value that does not fit its data type (encoding, local `Set` with a wrong length) |
| `ErrProtocol` | canopen | A message that violates the protocol: an unexpected SDO command specifier or toggle bit from the server, a malformed EMCY or TIME frame |
| `ErrBusy` | canopen | `Start` on a running service; the EMCY inhibit queue (256 messages) full |
| `ErrSyntax`, `*SyntaxError` | eds | A line `Parse` cannot read (line number and reason) |
| `ErrNumber` | pdo | PDO number outside 1..512 |
| `ErrDisabled`, `ErrInactive` | pdo | `TPDO.Transmit` while the TPDO is disabled (node not Operational), or while the PDO is not active (invalid COB-ID or empty mapping); `ErrInactive` also from `master.ListenTPDO` and `RPDOSender` for a node's inactive PDO |
| `ErrLength` | pdo | `Unpack` of data shorter than the mapping |
| `ErrNoRTR` | pdo | `Request` on a CANopen FD PDO (a decoder made `WithFD`): CAN FD has no remote frames (CiA 1301), so a TPDO cannot be requested that way |
| `ErrNotProducer` | timestamp | `Send` while 0x1012 bit 30 is clear |
| `ErrNoObject` | node | A service the dictionary does not configure (`Node.SendTime` without 0x1012) |
| `ErrIdentity` | master | Boot procedure: device type or identity differs from the `BootConfig` (or, in the CiA 302 boot-up, from 0x1F84..0x1F88) |
| `ErrProgram` | master | Program download: wrong application software identification, program not started, missing program data |
| `ErrNoDictionary` | master | A CiA 302 manager call (`BootNetwork`, `BootSlave`...) without `WithDictionary` |
| `ErrNoUSDO` | master | `Master.USDO` without `WithFD` or `WithUSDOClient` |
| `*BootError` | master | A failed CiA 302 boot slave process: node, `BootStatus` (error status letter) and cause |
| `ErrNoStore` | node | `SaveParameters` / `RestoreDefaults` without `WithStore` |
| `ErrNumber`, `ErrDisabled`, `ErrInactive`, `ErrMode`, `ErrNotScanned`, `ErrNotStarted`, `ErrLength` | mpdo | No such PDO; MPDO sent while not operational; TPDO not an active MPDO; SAM call on a DAM producer (or the reverse); SAM object not in a scanner list; not started; MPDO shorter than 8 bytes |
| `ErrNoNodeID`, `ErrConflict`, `ErrFastscan`, `*AssignError`, `*ConfigError` | lss | No free node-ID for `AssignAll`; more than one slave (or the wrong one) selected; Fastscan did not converge; an assignment that failed after the configure request (names the node-ID, which is never offered again); a slave's refusal of a configuration service (error code and implementation-specific code) |
| `ErrCommand`, `ErrSessions`, `ErrNotStarted`, `*AbortError` | usdo | Unknown command specifier; no free session-ID; client not started; a USDO abort (below) |
| `ErrFormat` | cdcf | Malformed concise DCF |
| `ErrPending`, `FlashError` | program | A program handler that finishes programming later; the flash status error codes of 0x1F57 |
| `ErrFault`, `ErrNotEnabled`, `ErrHoming`, `ErrUnreachable`, `ErrStatusword` | cia402 | Controller failures, carried by `*cia402.Error` (operation, last state and statusword, fault code 0x603F) |

## SDO aborts

`canopen.AbortCode` is a `uint32` error type with the CiA 301 abort codes
as constants. It is also what the object dictionary returns for a refused
remote access, so that the SDO server sends the reason unchanged; a write
hook that returns another error is answered with `AbortGeneral`.

The SDO client reports aborts as `*canopen.AbortError`, which unwraps to
its code:

```go
err := c.WriteUint16(ctx, 0x1017, 0, 1000)
var ae *canopen.AbortError
if errors.As(err, &ae) {
    fmt.Println(ae.Code, ae.Index, ae.SubIndex, ae.Remote)
}
if errors.Is(err, canopen.AbortReadOnly) { /* ... */ }
```

| Situation | Client returns | Abort sent by the client |
|-----------|----------------|--------------------------|
| The server aborts | `*AbortError{Remote: true}` | none |
| No response within the timeout (after `WithRetries` attempts) | matches `ErrTimeout` and `*AbortError{Code: AbortTimeout, Remote: false}` | `AbortTimeout` |
| The server violates the protocol | matches `ErrProtocol` and `*AbortError{Remote: false}` with the specific code (`AbortCommand`, `AbortToggle`, `AbortCRC`, `AbortSequence`, ...) | that code |
| The caller's context ends | the context's error | `AbortGeneral` |
| The client is closed | `ErrClosed` | — |

The server aborts a transfer whose client stays silent longer than its
timeout with `AbortTimeout`, and a download larger than `WithMaxSize`
with `AbortOutOfMemory`.

### Abort codes

| Code | Constant | Meaning |
|------|----------|---------|
| 0x05030000 | `AbortToggle` | Toggle bit not alternated |
| 0x05040000 | `AbortTimeout` | SDO protocol timed out |
| 0x05040001 | `AbortCommand` | Command specifier not valid or unknown |
| 0x05040002 | `AbortBlockSize` | Invalid block size (block mode) |
| 0x05040003 | `AbortSequence` | Invalid sequence number (block mode) |
| 0x05040004 | `AbortCRC` | CRC error (block mode) |
| 0x05040005 | `AbortOutOfMemory` | Out of memory |
| 0x06010000 | `AbortUnsupported` | Unsupported access to an object (also: PDO mapping change while active) |
| 0x06010001 | `AbortWriteOnly` | Attempt to read a write-only object |
| 0x06010002 | `AbortReadOnly` | Attempt to write a read-only object |
| 0x06020000 | `AbortNoObject` | Object does not exist |
| 0x06040041 | `AbortNotMappable` | Object cannot be mapped to the PDO |
| 0x06040042 | `AbortPDOLength` | Mapped objects exceed the PDO length |
| 0x06040043 | `AbortParamIncompatible` | General parameter incompatibility (e.g. a duplicate 0x1016 entry) |
| 0x06040047 | `AbortInternal` | General internal incompatibility |
| 0x06060000 | `AbortHardware` | Access failed due to a hardware error |
| 0x06070010 | `AbortLength` | Data type or length does not match |
| 0x06070012 | `AbortLengthHigh` | Length too high |
| 0x06070013 | `AbortLengthLow` | Length too low |
| 0x06090011 | `AbortNoSubIndex` | Sub-index does not exist |
| 0x06090030 | `AbortInvalidValue` | Invalid value (e.g. a COB-ID change while in use, a reserved transmission type) |
| 0x06090031 | `AbortValueHigh` | Value too high |
| 0x06090032 | `AbortValueLow` | Value too low |
| 0x06090036 | `AbortMaxLessMin` | Maximum less than minimum |
| 0x060A0023 | `AbortNoResource` | Resource not available: SDO connection |
| 0x08000000 | `AbortGeneral` | General error |
| 0x08000020 | `AbortTransfer` | Data cannot be transferred or stored |
| 0x08000021 | `AbortLocalControl` | ... because of local control |
| 0x08000022 | `AbortDeviceState` | ... because of the present device state |
| 0x08000023 | `AbortNoDictionary` | No object dictionary present |
| 0x08000024 | `AbortNoData` | No data available |

`AbortCode.Text()` returns the description; it is empty for codes CiA 301
does not define (they are still carried and formatted).

## USDO aborts (CANopen FD)

USDO abort codes are one byte (`usdo.AbortCode`, CiA 1301 as CiA's
CANopen FD poster lists them). A transfer that ends with an abort
returns `*usdo.AbortError` (code, server node, object, `Remote`), which
unwraps to its `usdo.AbortCode`, to the closest classical
`canopen.AbortCode` (`AbortCode.SDO`) and, for a client timeout, to
`canopen.ErrTimeout`: `errors.Is(err, canopen.AbortNoObject)` holds for
a USDO "object does not exist". A server maps the dictionary's SDO abort
codes with `usdo.FromSDO`. The master's procedures (with `WithFD`) report
USDO aborts as errors that also match `*canopen.AbortError`.

| Code | Constant | Meaning |
|------|----------|---------|
| 0x10 | `AbortUnknown` | Unknown error |
| 0x11 | `AbortProtocol` | USDO protocol error |
| 0x12 | `AbortTimeout` | USDO timeout |
| 0x13 | `AbortCommand` | Unknown command specifier |
| 0x14 | `AbortCounter` | Invalid counter (segmented transfer) |
| 0x15 | `AbortSize` | Incorrect data size |
| 0x16 | `AbortOutOfMemory` | Out of memory |
| 0x19..0x1D | `AbortRouting`, `AbortDestNetwork`, `AbortDestNode`, `AbortSourceNetwork`, `AbortSourceNode` | Routing errors (remote USDO, not supported) |
| 0x1E | `AbortDLC` | Unexpected CAN DLC |
| 0x1F | `AbortMultiplexer` | Unexpected multiplexer |
| 0x20 | `AbortParallel` | Parallel access to the same sub-index |
| 0x21 | `AbortSession` | Session-ID wrong or unknown |
| 0x31..0x38 | `AbortWriteOnly`, `AbortReadOnly`, `AbortNoObject`, `AbortNoSubIndex`, `AbortHardware`, `AbortLength`, `AbortLengthHigh`, `AbortLengthLow` | As the SDO codes of the same name |
| 0x40..0x45 | `AbortInvalidValue`, `AbortValueHigh`, `AbortValueLow`, `AbortMaxLessMin`, `AbortMinMoreMax`, `AbortCANIDReadOnly` | Value errors |
| 0x50..0x56 | `AbortNotMappable`, `AbortPDOLength`, `AbortNonMappable`, `AbortRPDOToTPDO`, `AbortTPDOToRPDO`, `AbortPDOValid`, `AbortMappingValid` | PDO mapping errors |
| 0x60..0x62 | `AbortTransfer`, `AbortLocalControl`, `AbortDeviceState` | Data cannot be transferred or stored |

The full list with the mapping to SDO codes is in the `usdo` package.

## LSS errors

Confirmed LSS services answer an error code (byte 1): `lss.CodeOK` (0),
`CodeOutOfRange` (1: node-ID out of range, bit timing or store not
supported), `CodeStoreFailed` (2: storage error) and
`CodeImplementSpec` (0xFF, with an implementation-specific code). The
master returns a refusal as `*lss.ConfigError`; an unanswered confirmed
service as `canopen.ErrTimeout` after `WithRetries`. A node with
`WithLSS` answers "store configuration" with `CodeOutOfRange` when it has
no `WithStore`, and with `CodeStoreFailed` when the store fails.

## CiA 302 boot status

A boot slave process ends with a `master.BootStatus`: `BootOK` (0) or an
error status letter (as Lely lely-core reports them; CiA 302-2 is a
member document). `EventBootFailed` and `EventNetworkBootFailed` carry it
in `Status` and in their `*BootError`.

| Letter | Constant | Meaning |
|--------|----------|---------|
| A | `BootNotListed` | Not in the network list (0x1F81 bit 0) |
| B | `BootNoResponse` | No answer to the upload of 0x1000 (retried, a mandatory slave within the boot time 0x1F89) |
| C | `BootDeviceType` | 0x1000 differs from 0x1F84 |
| D | `BootVendorID` | 0x1018:1 differs from 0x1F85 |
| E | `BootHeartbeat` | No heartbeat while checking a keep-alive slave |
| F | `BootGuarding` | No node guarding response while checking a keep-alive slave |
| G | `BootProgramObjects` | Program download objects (0x1F55, 0x1F56) missing or unreadable |
| H | `BootUpdateNotAllowed` | Software update needed but not allowed by 0x1F81 |
| I | `BootUpdateFailed` | Program download failed |
| J | `BootConfiguration` | Configuration (restore, concise DCF, store) failed |
| K | `BootErrorControl` | No heartbeat when starting the error control |
| L | `BootOperational` | A keep-alive slave was found operational and left running (not a failure) |
| M | `BootProductCode` | 0x1018:2 differs from 0x1F86 |
| N | `BootRevision` | 0x1018:3 differs from 0x1F87 |
| O | `BootSerialNumber` | 0x1018:4 differs from 0x1F88 |

## Timeouts and protocol errors in the other services

Services that supervise rather than request report through events, not
errors:

| Condition | Reported as |
|-----------|-------------|
| Heartbeat loss / resumption | `nmt.HeartbeatEvent` `EventTimeout` / `EventResumed` (once per loss); `master.Event` `EventHeartbeatLost` / `EventHeartbeatResumed`; in a node, EMCY 0x8130 and the 0x1029 error behaviour |
| Node or life guarding loss | `nmt.GuardEvent` `GuardTimeout`, `GuardResumed`, `GuardToggleError` |
| RPDO too short, too long, deadline missed | `pdo.ErrorEvent` (`WithErrorHandler`) with EMCY codes 0x8210, 0x8220, 0x8250; in a node, the emergency |
| Synchronous PDO outside the synchronous window (0x1007) | counted: `RPDOStats.OutsideWindow`, `TPDOStats.WindowMissed`; no emergency |
| DAM-MPDO for a missing object, MPDO shorter than 8 bytes | `pdo.ErrorEvent` from the `mpdo.Consumer` with EMCY 0x8230 / 0x8210; in a node, the emergency |
| SYNC of the wrong length, counter gap | `syncobj.Event.LengthError`; `ConsumerStats.LengthErrors`, `CounterErrors`; in a node, EMCY 0x8240 |
| Malformed EMCY, TIME, NMT, error control frames | counted (`Invalid` in the stats), logged at debug level, otherwise ignored |
| Boot procedure failure | `master.Event` `EventBootFailed` with `Err` |
| A malformed frame in `dissect` | `Message.Error` (the fields hold what could be decoded) |

## EDS diagnostics

`eds.Parse` fails only on unreadable lines (`*SyntaxError`). Everything
else is a `eds.Diagnostic` from `File.Validate` with a `Severity` (`Error`
or `Warning`), section, key and message; `HasErrors` reports whether a
file can build a dictionary. `File.Dictionary` refuses a file with errors;
`File.BuildDictionary` with lenient options skips the offending
sub-objects and returns their diagnostics.
