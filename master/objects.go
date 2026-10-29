// SPDX-License-Identifier: MIT

package master

import (
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
)

// Objects of a CiA 302 manager's dictionary (CiA 302-2 network management,
// CiA 302-3 configuration and program download). The arrays have one
// sub-index per node-ID (1..127).
const (
	// IndexNMTStartup is the NMT start-up value (UNSIGNED32, bits
	// Startup*).
	IndexNMTStartup uint16 = 0x1F80
	// IndexSlaveAssignment is the NMT slave assignment of each node
	// (UNSIGNED32, see [Assignment]).
	IndexSlaveAssignment uint16 = 0x1F81
	// IndexRequestNMT requests an NMT command for a node (UNSIGNED8, the
	// state to reach: 4 stopped, 5 operational, 6 reset node, 7 reset
	// communication, 127 pre-operational; sub-index 128: all nodes); a
	// read returns the node's last known state (0: unknown).
	IndexRequestNMT uint16 = 0x1F82
	// IndexRequestGuarding starts (1) or stops (0) node guarding of a
	// node (UNSIGNED8; sub-index 128: all nodes); a read returns 1 while
	// the node is guarded.
	IndexRequestGuarding uint16 = 0x1F83
	// IndexExpectedDeviceType .. IndexExpectedSerialNumber are the
	// expected identification of each node (UNSIGNED32, 0: not checked),
	// compared with the node's 0x1000 and 0x1018 sub-indices 1..4.
	IndexExpectedDeviceType   uint16 = 0x1F84
	IndexExpectedVendorID     uint16 = 0x1F85
	IndexExpectedProductCode  uint16 = 0x1F86
	IndexExpectedRevision     uint16 = 0x1F87
	IndexExpectedSerialNumber uint16 = 0x1F88
	// IndexBootTime is the time (UNSIGNED32, ms) the manager waits for the
	// mandatory slaves to boot; 0 waits without limit.
	IndexBootTime uint16 = 0x1F89
	// IndexRestoreConfiguration is, for each node with the restore bit
	// of its assignment, the sub-index of the node's 0x1011 the manager
	// writes "load" to before configuring it (UNSIGNED8, 0: none).
	IndexRestoreConfiguration uint16 = 0x1F8A
)

// Bits of the NMT start-up value 0x1F80 (CiA 302-2, as Lely lely-core
// implements them).
const (
	// StartupMaster: the device is the NMT master (else an NMT slave:
	// nothing of the network boot-up runs).
	StartupMaster uint32 = 1 << 0
	// StartupStartAll: start all slaves with one broadcast NMT start
	// (when every slave booted and may be started), else one by one.
	StartupStartAll uint32 = 1 << 1
	// StartupNoAutoOperational: the master does not enter Operational
	// itself after the boot-up; the application does it.
	StartupNoAutoOperational uint32 = 1 << 2
	// StartupNoStartSlaves: the master does not start the slaves; the
	// application does it.
	StartupNoStartSlaves uint32 = 1 << 3
	// StartupResetAllOnError: an error control event of a mandatory
	// slave resets all nodes, the master included (else the slave
	// alone is reset).
	StartupResetAllOnError uint32 = 1 << 4
	// StartupFlyingMaster: the device takes part in the flying master
	// process (CiA 302-2). Not supported: the bit is ignored.
	StartupFlyingMaster uint32 = 1 << 5
	// StartupStopAllOnError: an error control event of a mandatory slave
	// stops all nodes, the master included (takes precedence over
	// StartupResetAllOnError).
	StartupStopAllOnError uint32 = 1 << 6
)

// Bits of an NMT slave assignment (0x1F81).
const (
	// AssignSlave: the node is in the network list.
	AssignSlave uint32 = 1 << 0
	// AssignBoot: the master boots the node (identity check, software
	// and configuration download) and may start it; without it only the
	// error control is started.
	AssignBoot uint32 = 1 << 2
	// AssignMandatory: the network boot-up waits for the node and fails
	// without it.
	AssignMandatory uint32 = 1 << 3
	// AssignKeepAlive: the master does not reset the node at its own
	// boot-up; a node found operational keeps running (boot status L).
	AssignKeepAlive uint32 = 1 << 4
	// AssignVersionCheck: the node's application software
	// identification (0x1F56) is checked against 0x1F55.
	AssignVersionCheck uint32 = 1 << 5
	// AssignSoftwareUpdate: a node with another software is updated
	// from 0x1F58 (needs AssignVersionCheck, not AssignKeepAlive).
	AssignSoftwareUpdate uint32 = 1 << 6
	// AssignRestore: the node's parameters are restored (0x1011, the
	// sub-index of 0x1F8A) before it is configured.
	AssignRestore uint32 = 1 << 7
)

// Assignment is an NMT slave assignment (0x1F81): the Assign* bits in bits
// 0..7, the retry factor (node guarding life time factor) in bits 8..15
// and the guard time in ms in bits 16..31.
type Assignment uint32

// NewAssignment returns an assignment with the Assign* bits of flags, a
// guard time (truncated to ms, at most 65535 ms) and a retry factor.
func NewAssignment(flags uint32, guardTime time.Duration, retryFactor uint8) Assignment {
	ms := guardTime.Milliseconds()
	if ms > 0xFFFF {
		ms = 0xFFFF
	}
	if ms < 0 {
		ms = 0
	}
	return Assignment(flags&0xFF | uint32(retryFactor)<<8 | uint32(ms)<<16)
}

// Has reports whether every bit of flags is set.
func (a Assignment) Has(flags uint32) bool { return uint32(a)&flags == flags }

// RetryFactor returns bits 8..15.
func (a Assignment) RetryFactor() uint8 { return uint8(a >> 8) }

// GuardTime returns bits 16..31 as a duration.
func (a Assignment) GuardTime() time.Duration { return time.Duration(a>>16) * time.Millisecond }

// String formats the assignment's flags and guarding parameters.
func (a Assignment) String() string {
	names := []string{"slave", "", "boot", "mandatory", "keep-alive", "version-check", "software-update", "restore"}
	s := ""
	for i, n := range names {
		if n != "" && a&(1<<i) != 0 {
			if s != "" {
				s += "|"
			}
			s += n
		}
	}
	if s == "" {
		s = "none"
	}
	if a>>8 != 0 {
		s += fmt.Sprintf(" guard %v x%d", a.GuardTime(), a.RetryFactor())
	}
	return s
}

// AddManagerObjects adds to d, where missing, the objects of a CiA 302
// manager with default values (all 0: the device is an NMT slave until
// 0x1F80 bit 0 is set, no node is assigned): 0x1F80..0x1F8A, the
// concise DCFs 0x1F22, the expected configuration date and time 0x1F26
// and 0x1F27, the expected application software identification 0x1F55
// and the program data 0x1F58. d is then ready for [WithDictionary].
func AddManagerObjects(d *od.Dictionary) error {
	const n = int(canopen.MaxNodeID)
	u8, u32, dom := canopen.Unsigned8, canopen.Unsigned32, canopen.Domain
	rw, c := od.AccessRW, od.AccessConst
	request := func(index uint16, name string) *od.Object {
		o := od.NewArray(index, name, u8, rw, n+1, nil, c)
		o.Subs[n+1].Name = name + " all nodes"
		return o
	}
	objs := []*od.Object{
		od.NewArray(cdcf.IndexConciseDCF, "Concise DCF", dom, rw, n, nil, c),
		od.NewArray(cdcf.IndexExpectedConfigDate, "Expected configuration date", u32, rw, n, nil, c),
		od.NewArray(cdcf.IndexExpectedConfigTime, "Expected configuration time", u32, rw, n, nil, c),
		od.NewArray(program.IndexExpectedIdentification, "Expected application software identification", u32, rw, n, nil, c),
		od.NewArray(program.IndexManagerData, "Program data", dom, rw, n, nil, c),
		od.NewVar(IndexNMTStartup, "NMT startup", u32, rw, nil),
		od.NewArray(IndexSlaveAssignment, "NMT slave assignment", u32, rw, n, nil, c),
		request(IndexRequestNMT, "Request NMT"),
		request(IndexRequestGuarding, "Request node guarding"),
		od.NewArray(IndexExpectedDeviceType, "Device type identification", u32, rw, n, nil, c),
		od.NewArray(IndexExpectedVendorID, "Vendor identification", u32, rw, n, nil, c),
		od.NewArray(IndexExpectedProductCode, "Product code", u32, rw, n, nil, c),
		od.NewArray(IndexExpectedRevision, "Revision number", u32, rw, n, nil, c),
		od.NewArray(IndexExpectedSerialNumber, "Serial number", u32, rw, n, nil, c),
		od.NewVar(IndexBootTime, "Boot time", u32, rw, nil),
		od.NewArray(IndexRestoreConfiguration, "Restore configuration", u8, rw, n, nil, c),
	}
	for _, o := range objs {
		if d.Object(o.Index) != nil {
			continue
		}
		if err := d.Add(o); err != nil {
			return err
		}
	}
	return nil
}
