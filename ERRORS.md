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
| `ErrNotProducer` | timestamp | `Send` while 0x1012 bit 30 is clear |
| `ErrNoObject` | node | A service the dictionary does not configure (`Node.SendTime` without 0x1012) |
| `ErrIdentity` | master | Boot procedure: device type or identity differs from the `BootConfig` |
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

## Timeouts and protocol errors in the other services

Services that supervise rather than request report through events, not
errors:

| Condition | Reported as |
|-----------|-------------|
| Heartbeat loss / resumption | `nmt.HeartbeatEvent` `EventTimeout` / `EventResumed` (once per loss); `master.Event` `EventHeartbeatLost` / `EventHeartbeatResumed`; in a node, EMCY 0x8130 and the 0x1029 error behaviour |
| Node or life guarding loss | `nmt.GuardEvent` `GuardTimeout`, `GuardResumed`, `GuardToggleError` |
| RPDO too short, too long, deadline missed | `pdo.ErrorEvent` (`WithErrorHandler`) with EMCY codes 0x8210, 0x8220, 0x8250; in a node, the emergency |
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
