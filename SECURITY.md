# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-canopen`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-canopen/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce (ideally a minimal program, frame sequence or EDS file), the affected version(s) and the impact you see.

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x | Yes |

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
- SDO client: transfers end after the timeout; an upload larger than the
  announced size is refused.
- EMCY producer: at most 256 queued messages.
- Dictionary writes are checked against access rights, data type lengths,
  limits and `MaxSize` before hooks run.
- `master` event delivery uses an unbounded queue: a handler that never
  returns makes it grow with the network's traffic.

### Generated code

`codegen` embeds names and comments from the EDS as identifiers
(sanitized) and comments; it contains no string evaluated at run time.
Review code generated from untrusted EDS files like any other code.
