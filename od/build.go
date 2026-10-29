// SPDX-License-Identifier: MIT

package od

import (
	"fmt"

	"github.com/otfabric/go-canopen"
)

// Sub returns a sub-object description for [NewRecord] and [NewArray]; its
// Index is filled in by them. def is a Go value encoded with dt
// ([canopen.Encode]), or nil for a zero default.
func Sub(sub uint8, name string, dt canopen.DataType, access Access, def any) *Variable {
	return &Variable{SubIndex: sub, Name: name, DataType: dt, Access: access, Default: mustEncode(dt, def)}
}

func mustEncode(dt canopen.DataType, def any) []byte {
	if def == nil {
		return make([]byte, dt.Size())
	}
	b, err := canopen.Encode(dt, def)
	if err != nil {
		panic(fmt.Sprintf("od: default %v: %v", def, err))
	}
	return b
}

// NewVar returns a VAR object. def as for [Sub].
func NewVar(index uint16, name string, dt canopen.DataType, access Access, def any) *Object {
	v := Sub(0, name, dt, access, def)
	v.Index = index
	return &Object{Index: index, Name: name, Type: ObjectVar, Subs: []*Variable{v}}
}

// NewRecord returns a RECORD object with the sub-objects, sorted by the
// caller. Sub-index 0 (highest sub-index supported, UNSIGNED8, ro) is
// added unless subs has one.
func NewRecord(index uint16, name string, subs ...*Variable) *Object {
	o := &Object{Index: index, Name: name, Type: ObjectRecord}
	if len(subs) == 0 || subs[0].SubIndex != 0 {
		hi := uint8(0)
		if len(subs) > 0 {
			hi = subs[len(subs)-1].SubIndex
		}
		o.Subs = append(o.Subs, Sub(0, "Highest sub-index supported", canopen.Unsigned8, AccessConst, hi))
	}
	o.Subs = append(o.Subs, subs...)
	for _, v := range o.Subs {
		v.Index = index
	}
	return o
}

// NewArray returns an ARRAY object with n elements of one type, named
// "<name> <i>", and sub-index 0 (number of elements, UNSIGNED8) with the
// access sub0Access.
func NewArray(index uint16, name string, dt canopen.DataType, access Access, n int, def any, sub0Access Access) *Object {
	o := &Object{Index: index, Name: name, Type: ObjectArray}
	o.Subs = append(o.Subs, Sub(0, "Highest sub-index supported", canopen.Unsigned8, sub0Access, uint8(n)))
	for i := 1; i <= n; i++ {
		o.Subs = append(o.Subs, Sub(uint8(i), fmt.Sprintf("%s %d", name, i), dt, access, def))
	}
	for _, v := range o.Subs {
		v.Index = index
	}
	return o
}

// Indices of the communication profile objects (CiA 301 7.5.2).
const (
	IndexDeviceType        uint16 = 0x1000
	IndexErrorRegister     uint16 = 0x1001
	IndexErrorField        uint16 = 0x1003
	IndexCOBIDSync         uint16 = 0x1005
	IndexCyclePeriod       uint16 = 0x1006
	IndexSyncWindow        uint16 = 0x1007
	IndexDeviceName        uint16 = 0x1008
	IndexHardwareVersion   uint16 = 0x1009
	IndexSoftwareVersion   uint16 = 0x100A
	IndexGuardTime         uint16 = 0x100C
	IndexLifeTimeFactor    uint16 = 0x100D
	IndexStore             uint16 = 0x1010
	IndexRestore           uint16 = 0x1011
	IndexCOBIDTime         uint16 = 0x1012
	IndexCOBIDEmcy         uint16 = 0x1014
	IndexInhibitEmcy       uint16 = 0x1015
	IndexConsumerHeartbeat uint16 = 0x1016
	IndexProducerHeartbeat uint16 = 0x1017
	IndexIdentity          uint16 = 0x1018
	IndexSyncOverflow      uint16 = 0x1019
	IndexSDOServer         uint16 = 0x1200 // .. 0x127F
	IndexSDOClient         uint16 = 0x1280 // .. 0x12FF
	IndexRPDOComm          uint16 = 0x1400 // .. 0x15FF
	IndexRPDOMapping       uint16 = 0x1600 // .. 0x17FF
	IndexTPDOComm          uint16 = 0x1800 // .. 0x19FF
	IndexTPDOMapping       uint16 = 0x1A00 // .. 0x1BFF
	MaxPDOs                       = 512
	MaxMappedObjects              = 64
)

// Config selects the communication objects [Standard] creates.
type Config struct {
	NodeID     canopen.NodeID
	DeviceType uint32
	// Identity (0x1018).
	VendorID, ProductCode, Revision, SerialNumber uint32
	// DeviceName (0x1008), HardwareVersion (0x1009), SoftwareVersion
	// (0x100A); omitted when empty.
	DeviceName, HardwareVersion, SoftwareVersion string
	// HeartbeatMs is the default producer heartbeat time (0x1017).
	HeartbeatMs uint16
	// HeartbeatConsumers is the number of 0x1016 entries (0 omits it).
	HeartbeatConsumers int
	// ErrorHistory is the length of the pre-defined error field 0x1003
	// (0 omits it).
	ErrorHistory int
	// RPDOs and TPDOs are the numbers of receive and transmit PDOs, each
	// with MappingEntries mapping entries (default 8).
	RPDOs, TPDOs   int
	MappingEntries int
	// SDOClients is the number of SDO client parameter objects (0x1280..).
	SDOClients int
	// NodeGuarding adds guard time and life time factor (0x100C, 0x100D).
	NodeGuarding bool
	// Time adds COB-ID TIME (0x1012).
	Time bool
}

// Standard returns a dictionary with the communication profile objects of
// cfg, in their CiA 301 default configuration: 0x1000, 0x1001, 0x1005,
// 0x1006, 0x1007, 0x1014, 0x1015, 0x1017, 0x1018, 0x1019, 0x1200 always;
// the others as cfg selects. PDO COB-IDs follow the predefined connection
// set for PDOs 1..4 (valid) and are invalid (bit 31) beyond. Add the
// application objects to the result.
func Standard(cfg Config) (*Dictionary, error) {
	if cfg.MappingEntries == 0 {
		cfg.MappingEntries = 8
	}
	if cfg.RPDOs > MaxPDOs || cfg.TPDOs > MaxPDOs || cfg.MappingEntries > MaxMappedObjects || cfg.SDOClients > 128 ||
		cfg.HeartbeatConsumers > 127 || cfg.ErrorHistory > 254 {
		return nil, fmt.Errorf("od: configuration out of range")
	}
	d := New(cfg.NodeID)
	u8, u16, u32 := canopen.Unsigned8, canopen.Unsigned16, canopen.Unsigned32
	objs := []*Object{
		NewVar(IndexDeviceType, "Device type", u32, AccessRO, cfg.DeviceType),
		NewVar(IndexErrorRegister, "Error register", u8, AccessRO, nil),
		NewVar(IndexCOBIDSync, "COB-ID SYNC", u32, AccessRW, uint32(canopen.FuncSYNC)),
		NewVar(IndexCyclePeriod, "Communication cycle period", u32, AccessRW, nil),
		NewVar(IndexSyncWindow, "Synchronous window length", u32, AccessRW, nil),
		nodeRelative(NewVar(IndexCOBIDEmcy, "COB-ID EMCY", u32, AccessRW, uint32(canopen.FuncEMCY))),
		NewVar(IndexInhibitEmcy, "Inhibit time EMCY", u16, AccessRW, nil),
		NewVar(IndexProducerHeartbeat, "Producer heartbeat time", u16, AccessRW, cfg.HeartbeatMs),
		NewRecord(IndexIdentity, "Identity object",
			Sub(1, "Vendor-ID", u32, AccessRO, cfg.VendorID),
			Sub(2, "Product code", u32, AccessRO, cfg.ProductCode),
			Sub(3, "Revision number", u32, AccessRO, cfg.Revision),
			Sub(4, "Serial number", u32, AccessRO, cfg.SerialNumber)),
		NewVar(IndexSyncOverflow, "Synchronous counter overflow value", u8, AccessRW, nil),
		NewRecord(IndexSDOServer, "SDO server parameter",
			nodeRelativeSub(Sub(1, "COB-ID client to server", u32, AccessRO, uint32(canopen.FuncSDORx))),
			nodeRelativeSub(Sub(2, "COB-ID server to client", u32, AccessRO, uint32(canopen.FuncSDOTx)))),
	}
	objs[0].Subs[0].Access = AccessRO
	objs[1].Subs[0].PDOMappable = true
	if cfg.DeviceName != "" {
		objs = append(objs, NewVar(IndexDeviceName, "Manufacturer device name", canopen.VisibleString, AccessConst, cfg.DeviceName))
	}
	if cfg.HardwareVersion != "" {
		objs = append(objs, NewVar(IndexHardwareVersion, "Manufacturer hardware version", canopen.VisibleString, AccessConst, cfg.HardwareVersion))
	}
	if cfg.SoftwareVersion != "" {
		objs = append(objs, NewVar(IndexSoftwareVersion, "Manufacturer software version", canopen.VisibleString, AccessConst, cfg.SoftwareVersion))
	}
	if cfg.ErrorHistory > 0 {
		objs = append(objs, NewArray(IndexErrorField, "Pre-defined error field", u32, AccessRO, cfg.ErrorHistory, nil, AccessRW))
	}
	if cfg.NodeGuarding {
		objs = append(objs,
			NewVar(IndexGuardTime, "Guard time", u16, AccessRW, nil),
			NewVar(IndexLifeTimeFactor, "Life time factor", u8, AccessRW, nil))
	}
	if cfg.Time {
		objs = append(objs, NewVar(IndexCOBIDTime, "COB-ID TIME", u32, AccessRW, uint32(canopen.FuncTIME)))
	}
	if cfg.HeartbeatConsumers > 0 {
		objs = append(objs, NewArray(IndexConsumerHeartbeat, "Consumer heartbeat time", u32, AccessRW, cfg.HeartbeatConsumers, nil, AccessConst))
	}
	for i := 0; i < cfg.SDOClients; i++ {
		objs = append(objs, NewRecord(IndexSDOClient+uint16(i), fmt.Sprintf("SDO client parameter %d", i+1),
			Sub(1, "COB-ID client to server", u32, AccessRW, uint32(canopen.COBInvalid)),
			Sub(2, "COB-ID server to client", u32, AccessRW, uint32(canopen.COBInvalid)),
			Sub(3, "Node-ID of the SDO server", u8, AccessRW, nil)))
	}
	for i := 0; i < cfg.RPDOs; i++ {
		comm, mapping := PDOObjects(false, i+1, cfg.MappingEntries)
		objs = append(objs, comm, mapping)
	}
	for i := 0; i < cfg.TPDOs; i++ {
		comm, mapping := PDOObjects(true, i+1, cfg.MappingEntries)
		objs = append(objs, comm, mapping)
	}
	for _, o := range objs {
		if err := d.Add(o); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// PDOObjects returns the communication and mapping parameter objects of
// TPDO (transmit) or RPDO n (1..512) in their default configuration.
func PDOObjects(transmit bool, n, entries int) (comm, mapping *Object) {
	u8, u16, u32 := canopen.Unsigned8, canopen.Unsigned16, canopen.Unsigned32
	commIdx, mapIdx, kind := IndexRPDOComm, IndexRPDOMapping, "RPDO"
	if transmit {
		commIdx, mapIdx, kind = IndexTPDOComm, IndexTPDOMapping, "TPDO"
	}
	cob := Sub(1, "COB-ID used by "+kind, u32, AccessRW, uint32(canopen.COBInvalid))
	if n <= 4 {
		base := canopen.DefaultRPDO(n, 0)
		if transmit {
			base = canopen.DefaultTPDO(n, 0)
		}
		cob = nodeRelativeSub(Sub(1, "COB-ID used by "+kind, u32, AccessRW, uint32(base)))
	}
	subs := []*Variable{
		cob,
		Sub(2, "Transmission type", u8, AccessRW, uint8(255)),
		Sub(3, "Inhibit time", u16, AccessRW, nil),
		Sub(4, "Compatibility entry", u8, AccessRW, nil),
		Sub(5, "Event timer", u16, AccessRW, nil),
	}
	if transmit {
		subs = append(subs, Sub(6, "SYNC start value", u8, AccessRW, nil))
	}
	comm = NewRecord(commIdx+uint16(n-1), fmt.Sprintf("%s communication parameter %d", kind, n), subs...)
	mapping = NewArray(mapIdx+uint16(n-1), fmt.Sprintf("%s mapping parameter %d", kind, n), u32, AccessRW, entries, nil, AccessRW)
	mapping.Type = ObjectRecord
	mapping.Subs[0].Default = []byte{0}
	mapping.Subs[0].Name = "Number of mapped objects"
	for i, v := range mapping.Subs[1:] {
		v.Name = fmt.Sprintf("Mapped object %d", i+1)
	}
	return comm, mapping
}

func nodeRelative(o *Object) *Object {
	o.Subs[0].NodeIDRelative = true
	return o
}

func nodeRelativeSub(v *Variable) *Variable {
	v.NodeIDRelative = true
	return v
}
