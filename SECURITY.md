# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-canopen`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-canopen/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce (ideally a minimal program, frame sequence or EDS file), the affected version(s) and the impact you see.

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

## Security Considerations

### CANopen has no security

CANopen, like CAN, has no authentication, integrity protection beyond the
CAN CRC, or encryption. Any node on the bus can send NMT commands (stop
or reset every node), write any writable object by SDO, produce PDOs that
drive outputs, or impersonate a node's heartbeat. On machines, CANopen
objects move drives, valves and power electronics: a CiA 402 controlword
write starts a motor.

This library sends only what the application asks for, plus what CiA 301
requires of the role it plays: a `node` sends its boot-up, heartbeat,
emergencies, PDOs and SYNC/TIME as configured, and answers SDO requests
within the access rights of its dictionary; a `master` sends NMT
commands, SDO requests and its producers only on request (`Scan` probes
node-IDs by SDO only when called). Test against vcan, go-canbus's virtual
bus or an isolated bench bus, never on a running machine.

### Untrusted input

Received frames, EDS and DCF files are untrusted input. The frame and
file parsers and the SDO state machines are fuzzed (see TESTING.md) and
report errors instead of panicking. Resource bounds:

- SDO server: segmented and block downloads are buffered up to
  `sdo.WithMaxSize` (beyond it: abort `AbortOutOfMemory`), and a client
  that stops sending is aborted after the server's timeout.
- SDO client: a transfer ends after the timeout when the server does not
  answer, and with `canopen.ErrProtocol` after more than four responses
  in a row that move it no further (upload segments without data that are
  not the last, block acknowledgements of no segment); an upload larger
  than the announced size is refused.
- USDO client: a repeated segment or confirmation does not extend the
  response timeout, and a bulk download waits at most the response
  timeout between two segments, whatever processing time (t_proc) a
  server reports.
- EMCY producer: at most 256 queued messages.
- Dictionary writes are checked against access rights, data type lengths,
  limits and `MaxSize` before hooks run. A float object with a limit
  refuses NaN (`AbortInvalidValue`); a NaN limit is refused when the
  object is added. A finite value beyond the range of REAL32 is refused
  when it is encoded or parsed as REAL32.
- EDS and DCF files: `eds.Parse`, `ParseFile` and `ParseBytes` refuse a
  file larger than 16 MiB (`Parse` and `ParseFile` stop reading at that
  size), and a dictionary is built with at most 65536 sub-objects,
  compact storage counted expanded; a file that describes more is
  refused before anything is built (`eds.File.BuildDictionary`,
  `cdcf.Entries`, `cdcf.FromFile`, `codegen.Generate`).
- The request objects 0x1F82 and 0x1F83 of a `master` and the program
  control 0x1F51 of `program` take a one-byte value: declared with a type
  without fixed size, a write of another length is refused.
- `master` event delivery bounds the frame-driven events (EMCY, boot-up,
  state change; `WithEventQueue`, default 4096): under a flood with a
  slow handler the oldest are dropped and counted in
  `Stats().EventsDropped`. Boot results and heartbeat/guarding events are
  not bounded; their rate is set by protocol round trips and timeouts.
- `sdo` and `usdo` clients and servers refuse transfers above
  `WithMaxSize` (1 MiB by default) before allocating; a malformed NMT
  frame on ID 0 cannot fence the SDO server.
- Application code that the services call for a received frame (handlers,
  the dictionary's hooks and watchers, the log handler) is stopped where
  it is called when it does not return normally, and logged with its
  stack; the service goes on (OBSERVABILITY.md, LIMITATIONS.md).
- A closed service, node or master sends nothing: operations fail with
  `ErrClosed` (LIMITATIONS.md lists the single frames an application
  call may still complete).
- LSS: a node-ID configuration (`lss.Master.Configure`, `AssignAll`) is
  one sequence; a service of another goroutine cannot change the
  selection in between.

### Generated code

`codegen` embeds names and comments from the EDS as identifiers
(sanitized) and comments; it contains no string evaluated at run time.
Review code generated from untrusted EDS files like any other code.
