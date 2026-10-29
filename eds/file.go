// SPDX-License-Identifier: MIT

package eds

import (
	"sort"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// KeyValue is one key=value line kept verbatim, with the key's original
// spelling.
type KeyValue struct {
	Key   string
	Value string
}

// Section is a section the package does not interpret, kept verbatim.
type Section struct {
	// Name is the section name without the brackets.
	Name string
	// Keys are the section's keys in file order.
	Keys []KeyValue
}

// File is an EDS or DCF file.
type File struct {
	FileInfo   FileInfo
	DeviceInfo DeviceInfo
	// DeviceComissioning is the DCF commissioning section
	// ([DeviceComissioning], CiA 306 spelling; [DeviceCommissioning] is
	// accepted too); nil in an EDS.
	DeviceComissioning *DeviceComissioning
	// DummyUsage is the [DummyUsage] section; nil when absent.
	DummyUsage *DummyUsage
	// Comments is the [Comments] section; nil when absent.
	Comments *Comments
	// MandatoryObjects, OptionalObjects and ManufacturerObjects are the
	// object lists; nil when absent.
	MandatoryObjects    *ObjectList
	OptionalObjects     *ObjectList
	ManufacturerObjects *ObjectList
	// Objects are the objects in ascending index order.
	Objects []*Object
	// Unknown are the sections the package does not interpret, in file
	// order.
	Unknown []*Section

	// dups are the duplicate keys and sections Parse merged.
	dups []Diagnostic
}

// FileInfo is the [FileInfo] section.
type FileInfo struct {
	FileName         string
	FileVersion      uint8
	FileRevision     uint8
	EDSVersion       string
	Description      string
	CreationTime     string
	CreationDate     string
	CreatedBy        string
	ModificationTime string
	ModificationDate string
	ModifiedBy       string
	// LastEDS names the EDS a DCF was derived from.
	LastEDS string
	// Extra are the keys not listed above, or with a value that does not
	// parse, in file order.
	Extra []KeyValue
}

// BaudRates lists the bit rates in kbit/s the DeviceInfo BaudRate_xxx keys
// stand for, in the order of [DeviceInfo.BaudRate].
var BaudRates = [8]int{10, 20, 50, 125, 250, 500, 800, 1000}

// DeviceInfo is the [DeviceInfo] section.
type DeviceInfo struct {
	VendorName     string
	VendorNumber   uint32
	ProductName    string
	ProductNumber  uint32
	RevisionNumber uint32
	OrderCode      string
	// BaudRate tells which of [BaudRates] the device supports
	// (BaudRate_10 .. BaudRate_1000).
	BaudRate           [8]bool
	SimpleBootUpMaster bool
	SimpleBootUpSlave  bool
	// Granularity is the PDO mapping granularity in bits (0: no mapping,
	// 1..64).
	Granularity uint8
	// DynamicChannelsSupported is the number of dynamic channels
	// (CiA 302).
	DynamicChannelsSupported uint8
	GroupMessaging           bool
	NrOfRXPDO                uint16
	NrOfTXPDO                uint16
	LSSSupported             bool
	CompactPDO               uint8
	// Extra are the keys not listed above, or with a value that does not
	// parse, in file order.
	Extra []KeyValue
}

// SupportedBaudRates returns the supported bit rates in kbit/s.
func (d *DeviceInfo) SupportedBaudRates() []int {
	var out []int
	for i, ok := range d.BaudRate {
		if ok {
			out = append(out, BaudRates[i])
		}
	}
	return out
}

// DeviceComissioning is the [DeviceComissioning] section of a DCF.
type DeviceComissioning struct {
	NodeID uint8
	// NodeName is the name of the device in the network.
	NodeName string
	// Baudrate is the bit rate in kbit/s.
	Baudrate    uint16
	NetNumber   uint32
	NetworkName string
	// CANopenManager tells whether the device is the CANopen manager.
	CANopenManager  bool
	LSSSerialNumber uint32
	// Extra are the keys not listed above, or with a value that does not
	// parse, in file order.
	Extra []KeyValue
}

// DummyUsage is the [DummyUsage] section: which data types (0x0001..0x0007)
// may be mapped as dummy entries into RPDOs.
type DummyUsage struct {
	// Dummy maps a data type index to its DummyXXXX value.
	Dummy map[uint16]bool
	// Extra are the other keys, in file order.
	Extra []KeyValue
}

// Comments is the [Comments] section.
type Comments struct {
	// Lines are the comment lines Line1..LineN.
	Lines []string
	// Extra are the other keys, in file order.
	Extra []KeyValue
}

// ObjectList is an object list ([MandatoryObjects], [OptionalObjects],
// [ManufacturerObjects]) or the link list of an object
// ([XXXXObjectLinks]).
type ObjectList struct {
	// Count is the declared number of entries (SupportedObjects or
	// ObjectLinks); [File.Validate] checks it against Indices.
	Count int
	// Indices are the listed indices in key order (1, 2, ...).
	Indices []uint16
	// Extra are the other keys, in file order.
	Extra []KeyValue
}

// Contains reports whether the list contains the index.
func (l *ObjectList) Contains(index uint16) bool {
	if l == nil {
		return false
	}
	for _, i := range l.Indices {
		if i == index {
			return true
		}
	}
	return false
}

// SubTable is a per-sub-index table of an object with compact storage:
// [XXXXName] (sub-object names), [XXXXValue] (sub-object values, as
// ParameterValue) or [XXXXDenotation] (sub-object denotations, DCF).
type SubTable struct {
	// NrOfEntries is the declared number of entries.
	NrOfEntries int
	// Entries maps a sub-index to its text.
	Entries map[uint8]string
	// Extra are the other keys, in file order.
	Extra []KeyValue
}

// Entry is the content of an object section [XXXX] or a sub-object
// section [XXXXsubY].
type Entry struct {
	ParameterName string
	// ObjectType is the object code; VAR when the key is absent.
	ObjectType od.ObjectType
	// DataType is the data type index; 0 when absent.
	DataType canopen.DataType
	// AccessType is 0 when absent.
	AccessType od.Access
	// DefaultValue, LowLimit and HighLimit are the raw literals, possibly
	// with $NODEID; empty when absent.
	DefaultValue string
	LowLimit     string
	HighLimit    string
	PDOMapping   bool
	// SubNumber is the number of sub-objects of an ARRAY or RECORD; 0
	// when absent.
	SubNumber uint8
	// ObjFlags are the CiA 306 object flags (bit 0: refuse write on
	// download, bit 1: refuse read on scan); 0 when absent.
	ObjFlags uint32
	// CompactSubObj is the number of sub-objects of an ARRAY in compact
	// storage; 0 when absent.
	CompactSubObj uint8
	// ParameterValue is the configured value of a DCF, valid when
	// HasParameterValue is true.
	ParameterValue    string
	HasParameterValue bool
	// Denotation is the DCF denotation (application name) of the
	// object.
	Denotation string
	// UploadFile and DownloadFile name files a DOMAIN is read into or
	// written from (DCF).
	UploadFile   string
	DownloadFile string
	// Extra are the keys not listed above, or with a value that does not
	// parse, in file order.
	Extra []KeyValue
}

// Value returns the literal a dictionary uses: ParameterValue when
// present, else DefaultValue.
func (e *Entry) Value() string {
	if e.HasParameterValue {
		return e.ParameterValue
	}
	return e.DefaultValue
}

// SubObject is a sub-object section [XXXXsubY].
type SubObject struct {
	SubIndex uint8
	Entry
}

// Object is an object: its section [XXXX], its sub-object sections and
// its auxiliary sections.
type Object struct {
	Index uint16
	// Entry is the content of [XXXX].
	Entry
	// Subs are the [XXXXsubY] sections in ascending sub-index order.
	Subs []*SubObject
	// Names, Values and Denotations are the [XXXXName], [XXXXValue] and
	// [XXXXDenotation] sections of an object with compact storage; nil
	// when absent.
	Names       *SubTable
	Values      *SubTable
	Denotations *SubTable
	// Links is the [XXXXObjectLinks] section; nil when absent.
	Links *ObjectList

	// implicit marks an object that has sub-object or auxiliary sections
	// but no [XXXX] section.
	implicit bool
}

// Sub returns the sub-object section with the sub-index, or nil.
func (o *Object) Sub(sub uint8) *SubObject {
	i := sort.Search(len(o.Subs), func(i int) bool { return o.Subs[i].SubIndex >= sub })
	if i < len(o.Subs) && o.Subs[i].SubIndex == sub {
		return o.Subs[i]
	}
	return nil
}

// Object returns the object with the index, or nil.
func (f *File) Object(index uint16) *Object {
	i := sort.Search(len(f.Objects), func(i int) bool { return f.Objects[i].Index >= index })
	if i < len(f.Objects) && f.Objects[i].Index == index {
		return f.Objects[i]
	}
	return nil
}

// IsDCF reports whether the file is a DCF: it has a [DeviceComissioning]
// section.
func (f *File) IsDCF() bool { return f.DeviceComissioning != nil }

// SortObjects sorts the objects by index and their sub-objects by
// sub-index, as Parse leaves them. Call it after adding objects out of
// order.
func (f *File) SortObjects() {
	sort.SliceStable(f.Objects, func(i, j int) bool { return f.Objects[i].Index < f.Objects[j].Index })
	for _, o := range f.Objects {
		sort.SliceStable(o.Subs, func(i, j int) bool { return o.Subs[i].SubIndex < o.Subs[j].SubIndex })
	}
}

// UpdateObjectLists rebuilds the three object lists from the objects:
// 0x1000, 0x1001 and 0x1018 are mandatory, 0x2000..0x5FFF manufacturer
// specific, everything else from 0x1000 up optional.
func (f *File) UpdateObjectLists() {
	f.MandatoryObjects, f.OptionalObjects, f.ManufacturerObjects = &ObjectList{}, &ObjectList{}, &ObjectList{}
	for _, o := range f.Objects {
		if l := listFor(o.Index, f); l != nil {
			l.Indices = append(l.Indices, o.Index)
			l.Count++
		}
	}
}

// listFor returns the object list an index belongs to, or nil for the
// data type area below 0x1000.
func listFor(index uint16, f *File) *ObjectList {
	switch {
	case index < 0x1000:
		return nil
	case isMandatory(index):
		return f.MandatoryObjects
	case index >= 0x2000 && index <= 0x5FFF:
		return f.ManufacturerObjects
	}
	return f.OptionalObjects
}

// isMandatory reports whether CiA 301 makes the object mandatory.
func isMandatory(index uint16) bool {
	return index == 0x1000 || index == 0x1001 || index == 0x1018
}
