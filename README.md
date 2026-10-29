# go-canopen - CANopen (CiA 301) for Go

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-canopen.svg)](https://pkg.go.dev/github.com/otfabric/go-canopen)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-canopen/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-canopen/actions/workflows/ci.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-canopen/graph/badge.svg)](https://codecov.io/gh/otfabric/go-canopen)

A pure Go implementation of CANopen (CiA 301 application layer and
communication profile): the object dictionary, SDO client and server with
block transfer, NMT master and slave, heartbeat and node guarding, PDOs,
SYNC, EMCY and TIME, a complete device (`node`) built from an object
dictionary, a network manager (`master`), EDS/DCF (CiA 306) files, a Go
code generator, the CiA 402 drive profile and a frame dissector. No cgo.

It is layered on [go-canbus](https://github.com/otfabric/go-canbus)
v0.7.0: every service runs on a `canbus.Bus`, so the same code runs on
Linux SocketCAN, slcan USB adapters (any OS) and go-canbus's in-process
virtual bus, which the tests and examples use.

| Package | What it does |
|---------|--------------|
| [`canopen`](https://pkg.go.dev/github.com/otfabric/go-canopen) | Node-IDs, COB-IDs and the predefined connection set, CANopen data types and their encoding, SDO abort codes, NMT states and commands, the `Bus` interface, sentinel errors |
| [`od`](https://pkg.go.dev/github.com/otfabric/go-canopen/od) | The object dictionary: objects, access rights, limits, defaults (node-ID relative), read/write hooks and watchers, `od.Standard` for the CiA 301 communication objects |
| [`sdo`](https://pkg.go.dev/github.com/otfabric/go-canopen/sdo) | SDO client and server: expedited, segmented and block transfer (CRC, block size, protocol switch), timeouts, retries |
| [`nmt`](https://pkg.go.dev/github.com/otfabric/go-canopen/nmt) | NMT commands, the NMT slave state machine with both resets, heartbeat producer and consumer, node guarding and life guarding |
| [`pdo`](https://pkg.go.dev/github.com/otfabric/go-canopen/pdo) | Bit-exact mapping, TPDO and RPDO services (all transmission types, inhibit time, event timer, RTR, deadline monitoring), the CiA 301 reconfiguration rules, remote configuration, decoding and sending |
| [`sync`](https://pkg.go.dev/github.com/otfabric/go-canopen/sync) (package `syncobj`), [`emcy`](https://pkg.go.dev/github.com/otfabric/go-canopen/emcy), [`timestamp`](https://pkg.go.dev/github.com/otfabric/go-canopen/timestamp) | SYNC producer and consumer (counter), emergency producer (error register, error history, inhibit time) and consumer, TIME producer and consumer with TIME_OF_DAY |
| [`node`](https://pkg.go.dev/github.com/otfabric/go-canopen/node) | A complete device: chooses its services from the dictionary, gates them by NMT state, raises the CiA 301 emergencies on its own |
| [`master`](https://pkg.go.dev/github.com/otfabric/go-canopen/master) | A network manager: NMT, heartbeat supervision, EMCY, SDO clients per node, scan, configuration from entries or a DCF, boot procedure, remote PDOs, SYNC/TIME, CiA 402 drive control, one ordered event stream |
| [`eds`](https://pkg.go.dev/github.com/otfabric/go-canopen/eds) | EDS and DCF (CiA 306-1): parse, validate, write (round trip), to and from an object dictionary |
| [`codegen`](https://pkg.go.dev/github.com/otfabric/go-canopen/codegen) | Typed Go code from an EDS (deterministic, golden-tested) |
| [`cia402`](https://pkg.go.dev/github.com/otfabric/go-canopen/cia402) | CiA 402 drive profile: state machine, modes of operation, a simulated drive (device side) and a controller (master side) |
| [`dissect`](https://pkg.go.dev/github.com/otfabric/go-canopen/dissect) | Decoding of CAN frames as CANopen messages (NMT, SYNC, TIME, EMCY, PDO, SDO with transfer tracking, heartbeat, node guarding, LSS), checked against Wireshark's dissector |

## CiA 301 services

| Service | CiA 301 | Status | Where |
|---------|---------|--------|-------|
| Object dictionary, data types | 7.1, 7.4 | Implemented: all basic types (BOOLEAN to UNSIGNED64, REAL32/64, strings, DOMAIN, TIME_OF_DAY, TIME_DIFFERENCE), access rights, limits, `$NODEID` defaults | `canopen`, `od` |
| SDO (expedited, segmented, block) | 7.2.4 | Implemented, client and server, one server channel (0x1200) per node | `sdo` |
| PDO | 7.2.2 | Implemented: synchronous (cyclic, acyclic), event-driven, RTR, inhibit time, event timer, SYNC start value, RPDO deadline monitoring; classical CAN, 64 bits | `pdo` |
| SYNC | 7.2.5 | Implemented, producer and consumer with counter; the SYNC window is reported, not enforced | `sync` |
| TIME | 7.2.6 | Implemented, producer and consumer | `timestamp` |
| EMCY | 7.2.7 | Implemented, producer (0x1001, 0x1003, 0x1014, 0x1015) and consumer | `emcy` |
| NMT module control | 7.2.8 | Implemented, master and slave (both resets, pending node-ID) | `nmt` |
| Heartbeat | 7.2.8 | Implemented, producer (0x1017) and consumer (0x1016) | `nmt` |
| Node guarding, life guarding | 7.2.8 | Implemented, master and responder (0x100C, 0x100D) | `nmt` |
| Error behaviour (0x1029) | 7.5 | Implemented in `node` (sub-index 1) | `node` |
| Store/restore parameters (0x1010, 0x1011) | 7.5 | Not implemented | — |
| Multiplexed PDO (MPDO, SAM/DAM) | 7.2.3 | Not implemented | — |
| LSS (CiA 305) | — | Dissected only; no LSS master or slave | `dissect` |
| CANopen FD, USDO (CiA 1301) | — | Not implemented | — |
| CiA 302 network management | — | Partly: boot procedure, configuration and heartbeat supervision in `master`; no flying master, configuration manager or SDO manager | `master` |
| CiA 306 EDS/DCF | — | Implemented (modular-device sections kept, not interpreted) | `eds` |
| CiA 402 drives | — | Implemented: state machine, PP, PV, PT, HM, CSP, CSV, CST (simulated drive and controller) | `cia402` |

Details and every limitation: [LIMITATIONS.md](LIMITATIONS.md).

## Install

```bash
go get github.com/otfabric/go-canopen
```

Go 1.23 or later. The one dependency is go-canbus (pure Go).

## Quick start

A device and a network manager on two ports of a virtual bus (the
package example [`node.Example`](node/example_test.go), run by `go test`):

```go
vb := virtual.New(virtual.Config{Name: "quickstart"})
p1, _ := vb.Open()
p2, _ := vb.Open()
deviceBus, _ := canbus.NewBus(p1)
masterBus, _ := canbus.NewBus(p2)

// The device: the CiA 301 communication objects and a node-ID.
d, _ := od.Standard(od.Config{NodeID: 5, DeviceType: 0x000F0191, VendorID: 0xF7A, DeviceName: "quickstart", HeartbeatMs: 100})
dev, _ := node.New(deviceBus, d)

// The network manager.
m, _ := master.New(masterBus)
_ = m.Start()
defer m.Close()

since := time.Now()
_ = dev.Start() // boot-up message, Pre-operational
defer dev.Close()
_ = m.WaitBootUp(ctx, 5, since)

c, _ := m.SDO(5)
devType, _ := c.ReadUint(ctx, od.IndexDeviceType, 0)
name, _ := c.ReadString(ctx, od.IndexDeviceName, 0)
fmt.Printf("node 5: device type 0x%08X, %q\n", devType, name)

_ = m.StartNode(ctx, 5)
_ = m.WaitState(ctx, 5, canopen.StateOperational)
```

On a real network, open the bus with go-canbus instead
(`socketcan.Open("can0")` on Linux, or an slcan adapter); the CANopen code
does not change.

More in [examples/](examples): a node and a master exchanging PDOs, SDO
block transfer, a CiA 402 drive, a device built from an EDS and
configured from a DCF, and a node on SocketCAN.

## Documentation

[API.md](API.md) · [ARCHITECTURE.md](ARCHITECTURE.md) ·
[ERRORS.md](ERRORS.md) · [OBSERVABILITY.md](OBSERVABILITY.md) ·
[INTEROPERABILITY.md](INTEROPERABILITY.md) · [TESTING.md](TESTING.md) ·
[LIMITATIONS.md](LIMITATIONS.md) · [ROADMAP.md](ROADMAP.md) ·
[SECURITY.md](SECURITY.md) · [CHANGELOG.md](CHANGELOG.md) ·
[RELEASE.md](RELEASE.md) · [CONTRIBUTING.md](CONTRIBUTING.md) ·
[PROGRESS.md](PROGRESS.md)

Related: [go-canbus](https://github.com/otfabric/go-canbus) (the CAN
layer), [canopen-interop](https://github.com/otfabric/canopen-interop)
(independent CANopen stacks in containers for interoperability tests).

## Standards

The CiA specifications (CiA 301, 302, 305, 306, 402) are documents of CAN
in Automation, available to its members. This library is implemented
from public material (public descriptions of the protocol, the behaviour
of open-source stacks such as CANopenNode, Lely CANopen and python
canopen, and Wireshark's CANopen dissector) and is tested against
independent implementations: Wireshark's dissector, and CANopenNode, Lely
CANopen and python canopen through the
[canopen-interop](https://github.com/otfabric/canopen-interop) containers
in both directions ([INTEROPERABILITY.md](INTEROPERABILITY.md) has the
matrix and the known differences). It makes **no
claim of CiA conformance** and has not been certified.

## License

MIT, see [LICENSE](LICENSE).
