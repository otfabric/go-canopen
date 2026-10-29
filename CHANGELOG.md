# Changelog

Release notes with details are in [RELEASE.md](RELEASE.md).

## v0.2.0 (2026-10-29)

Interoperability suite (`interop/`, `make interop`, Interop workflow on
amd64 and arm64) against CANopenNode, Lely CANopen and python canopen
through the canopen-interop v0.1.0 images: go-canopen as master/client of
each stack's device, each stack as master/client of a go-canopen node
(SDO, NMT, heartbeat, EMCY, PDO, SYNC, CiA 402 with python canopen's
BaseNode402), and the EDS checkers against the eds package; the matrix
and the known differences are in INTEROPERABILITY.md. Fixed, found by the
suite: an SDO block transfer of empty data that ends without a segment
(Lely's server on upload, python canopen's client on download) timed
out in go-canopen's client and server; both now accept it.

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
