// SPDX-License-Identifier: MIT

package eds

import (
	"fmt"
	mbits "math/bits"
	"strconv"
	"strings"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// field binds a key of a standard section to a typed struct field: parse
// stores a value (false: the value does not parse and is kept as an Extra
// key instead), format returns the value to write and whether to write it.
type field struct {
	key    string
	parse  func(v string) bool
	format func() (string, bool)
}

// fieldMap indexes fields by lower-case key.
func fieldMap(fs []field) map[string]field {
	m := make(map[string]field, len(fs))
	for _, f := range fs {
		m[strings.ToLower(f.key)] = f
	}
	return m
}

func strField(key string, p *string, always bool) field {
	return field{key,
		func(v string) bool { *p = v; return true },
		func() (string, bool) { return *p, always || *p != "" }}
}

// uintField binds an unsigned integer; an empty value is 0. layout is the
// fmt verb the writer uses.
func uintField[T ~uint8 | ~uint16 | ~uint32](key string, p *T, layout string, always bool) field {
	bits := mbits.Len64(uint64(^T(0)))
	return field{key,
		func(v string) bool {
			if v == "" {
				*p = 0
				return true
			}
			x, err := canopen.ParseUint(v, bits)
			if err != nil {
				return false
			}
			*p = T(x)
			return true
		},
		func() (string, bool) { return fmt.Sprintf(layout, uint64(*p)), always || *p != 0 }}
}

// boolField binds a 0/1 flag; an empty value is 0.
func boolField(key string, p *bool, always bool) field {
	return field{key,
		func(v string) bool {
			if v == "" {
				*p = false
				return true
			}
			x, err := canopen.ParseUint(v, 8)
			if err != nil || x > 1 {
				return false
			}
			*p = x == 1
			return true
		},
		func() (string, bool) {
			if *p {
				return "1", true
			}
			return "0", always
		}}
}

func (fi *FileInfo) fields() []field {
	return []field{
		strField("FileName", &fi.FileName, true),
		uintField("FileVersion", &fi.FileVersion, "%d", true),
		uintField("FileRevision", &fi.FileRevision, "%d", true),
		strField("LastEDS", &fi.LastEDS, false),
		strField("EDSVersion", &fi.EDSVersion, true),
		strField("Description", &fi.Description, true),
		strField("CreationTime", &fi.CreationTime, true),
		strField("CreationDate", &fi.CreationDate, true),
		strField("CreatedBy", &fi.CreatedBy, true),
		strField("ModificationTime", &fi.ModificationTime, true),
		strField("ModificationDate", &fi.ModificationDate, true),
		strField("ModifiedBy", &fi.ModifiedBy, true),
	}
}

func (d *DeviceInfo) fields() []field {
	fs := []field{
		strField("VendorName", &d.VendorName, true),
		uintField("VendorNumber", &d.VendorNumber, "0x%08X", true),
		strField("ProductName", &d.ProductName, true),
		uintField("ProductNumber", &d.ProductNumber, "0x%08X", true),
		uintField("RevisionNumber", &d.RevisionNumber, "0x%08X", true),
		strField("OrderCode", &d.OrderCode, true),
	}
	for i, r := range BaudRates {
		fs = append(fs, boolField("BaudRate_"+strconv.Itoa(r), &d.BaudRate[i], true))
	}
	return append(fs,
		boolField("SimpleBootUpMaster", &d.SimpleBootUpMaster, true),
		boolField("SimpleBootUpSlave", &d.SimpleBootUpSlave, true),
		uintField("Granularity", &d.Granularity, "%d", true),
		uintField("DynamicChannelsSupported", &d.DynamicChannelsSupported, "%d", true),
		uintField("CompactPDO", &d.CompactPDO, "0x%02X", true),
		boolField("GroupMessaging", &d.GroupMessaging, true),
		uintField("NrOfRXPDO", &d.NrOfRXPDO, "%d", true),
		uintField("NrOfTXPDO", &d.NrOfTXPDO, "%d", true),
		boolField("LSS_Supported", &d.LSSSupported, true),
	)
}

func (c *DeviceComissioning) fields() []field {
	return []field{
		uintField("NodeID", &c.NodeID, "%d", true),
		strField("NodeName", &c.NodeName, true),
		uintField("Baudrate", &c.Baudrate, "%d", true),
		uintField("NetNumber", &c.NetNumber, "%d", true),
		strField("NetworkName", &c.NetworkName, false),
		boolField("CANopenManager", &c.CANopenManager, true),
		uintField("LSS_SerialNumber", &c.LSSSerialNumber, "%d", true),
	}
}

func (e *Entry) fields() []field {
	return []field{
		strField("ParameterName", &e.ParameterName, true),
		{"ObjectType",
			func(v string) bool {
				x, err := canopen.ParseUint(v, 8)
				if err != nil {
					return false
				}
				e.ObjectType = od.ObjectType(x)
				return true
			},
			func() (string, bool) { return fmt.Sprintf("0x%X", uint8(e.ObjectType)), true }},
		{"DataType",
			func(v string) bool {
				if v == "" {
					e.DataType = 0
					return true
				}
				t, err := canopen.ParseDataType(v)
				if err != nil {
					return false
				}
				e.DataType = t
				return true
			},
			func() (string, bool) { return fmt.Sprintf("0x%04X", uint16(e.DataType)), e.DataType != 0 }},
		{"AccessType",
			func(v string) bool {
				if v == "" {
					e.AccessType = 0
					return true
				}
				a, err := od.ParseAccess(v)
				if err != nil {
					return false
				}
				e.AccessType = a
				return true
			},
			func() (string, bool) { return e.AccessType.String(), e.AccessType != 0 }},
		{"DefaultValue",
			func(v string) bool { e.DefaultValue = v; return true },
			func() (string, bool) { return e.DefaultValue, e.DefaultValue != "" || e.DataType != 0 }},
		strField("LowLimit", &e.LowLimit, false),
		strField("HighLimit", &e.HighLimit, false),
		{"PDOMapping",
			boolField("", &e.PDOMapping, false).parse,
			func() (string, bool) {
				if e.PDOMapping {
					return "1", true
				}
				return "0", e.DataType != 0
			}},
		uintField("SubNumber", &e.SubNumber, "0x%X", false),
		uintField("ObjFlags", &e.ObjFlags, "0x%X", false),
		uintField("CompactSubObj", &e.CompactSubObj, "%d", false),
		{"ParameterValue",
			func(v string) bool { e.ParameterValue, e.HasParameterValue = v, true; return true },
			func() (string, bool) { return e.ParameterValue, e.HasParameterValue }},
		strField("Denotation", &e.Denotation, false),
		strField("UploadFile", &e.UploadFile, false),
		strField("DownloadFile", &e.DownloadFile, false),
	}
}

// knownKeys are the lower-case keys of the standard sections, for
// reporting an Extra key that holds an invalid value of a known key.
var (
	knownFileInfo   = fieldMap(new(FileInfo).fields())
	knownDeviceInfo = fieldMap(new(DeviceInfo).fields())
	knownComm       = fieldMap(new(DeviceComissioning).fields())
	knownEntry      = fieldMap(new(Entry).fields())
)
