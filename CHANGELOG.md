# Changelog

Release notes with details are in [RELEASE.md](RELEASE.md).

## v0.1.0 (2026-10-29)

First release. A pure Go CANopen (CiA 301) stack on go-canbus v0.7.0:
the object dictionary with hooks and watchers; SDO client and server
with expedited, segmented and block transfer; NMT master and slave with
both resets, heartbeat producer and consumer, node and life guarding;
PDOs with every transmission type, the CiA 301 reconfiguration rules and
remote configuration; SYNC, EMCY and TIME; a complete device (`node`)
built from a dictionary and gated by NMT state; a network manager
(`master`) with scan, boot procedure, DCF configuration, remote PDOs and
one ordered event stream; EDS/DCF (CiA 306) parse, validate, write and
dictionary conversion; a Go code generator; the CiA 402 drive profile
(simulated drive and controller); a frame dissector checked against
Wireshark. No interoperability results against other CANopen stacks
yet; nothing tested on real hardware.
