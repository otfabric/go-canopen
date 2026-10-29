# Examples

Small runnable programs showing go-canopen end to end. Each is a `main`
package; all but those marked `// example:hardware` run on the in-process
virtual CAN bus of go-canbus and exit successfully, so `make examples` runs
them as a check.

| Example | Shows |
| --- | --- |
| [node-master](node-master/main.go) | A device built with `od.Standard` and `node.New`, and a `master.Master`: boot-up event, identity and device name by SDO, TPDO/RPDO mapping with `ConfigurePDO`, NMT start, process data both ways, node statistics. |
| [sdo-block](sdo-block/main.go) | A 64 KiB DOMAIN downloaded and uploaded with SDO block transfer (CRC-checked), then with segmented transfer, with timings. |
| [cia402-drive](cia402-drive/main.go) | A simulated CiA 402 drive as a node (`node.WithDrive`) operated by `master.Drive`: enable, profile position moves, a fault reported by EMCY, fault reset, disable. |
| [eds-dictionary](eds-dictionary/main.go) | A device built from an EDS (`eds.ParseBytes`, `File.Dictionary`), scanned, configured from a DCF (`master.ConfigureFromDCF`), started, and exported as a DCF (`eds.FromDictionary`, `eds.Write`). |
| [socketcan-node](socketcan-node/main.go) | *Hardware*: a node on a Linux SocketCAN interface (`-iface`, `-node`, `-heartbeat`) until Ctrl-C, with a counter in TPDO 1. Try it on vcan with `cansend`/`candump`. |

Run one with `go run ./examples/<name>` from the repository root, or all
self-contained ones with `make examples`. `make build` builds every
example (including the hardware ones) into `bin/`.

The examples use the virtual bus only to stay self-contained: replace the
`virtual` ports with `socketcan.Open("can0")` (Linux) or an slcan adapter
and the same code runs on a real network.
