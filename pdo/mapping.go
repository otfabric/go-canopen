// SPDX-License-Identifier: MIT

package pdo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Limits of a classical CAN PDO.
const (
	// MaxBits is the largest total length of the mapped objects of a PDO:
	// the 8 data bytes of a classical CAN frame.
	MaxBits = 64
	// MaxEntries is the largest number of mapping entries (CiA 301
	// 7.5.2.36: sub-indices 01h..40h).
	MaxEntries = od.MaxMappedObjects
	// MaxBitsFD is the largest total length of the mapped objects of a
	// CANopen FD PDO (CiA 1301): the 64 data bytes of a CAN FD frame.
	MaxBitsFD = 512
)

// ErrLength reports PDO data shorter than its mapping.
var ErrLength = errors.New("pdo: data shorter than the mapping")

// Entry is one PDO mapping entry (CiA 301 7.5.2.36): the object mapped
// and its length in bits. On the bus and in the dictionary it is the
// UNSIGNED32 index<<16 | sub-index<<8 | length.
type Entry struct {
	Index    uint16
	SubIndex uint8
	Bits     uint8
}

// ParseEntry splits the UNSIGNED32 encoding of a mapping entry.
func ParseEntry(u uint32) Entry {
	return Entry{Index: uint16(u >> 16), SubIndex: uint8(u >> 8), Bits: uint8(u)}
}

// Uint32 returns the UNSIGNED32 encoding of the entry.
func (e Entry) Uint32() uint32 {
	return uint32(e.Index)<<16 | uint32(e.SubIndex)<<8 | uint32(e.Bits)
}

// IsDummy reports whether the entry maps a data type definition
// (0x0001..0x0007, sub-index 0): a dummy entry, which skips bits of a
// received PDO (CiA 301 7.4.7.1).
func (e Entry) IsDummy() bool {
	return e.Index >= 0x0001 && e.Index <= 0x0007 && e.SubIndex == 0
}

// String formats the entry as "IIII:SS/bits".
func (e Entry) String() string {
	return fmt.Sprintf("%04X:%02X/%d", e.Index, e.SubIndex, e.Bits)
}

// Mapping is the ordered list of mapping entries of a PDO. The first
// entry occupies the least significant bits of the first data byte.
type Mapping []Entry

// Bits returns the total length of the mapped objects in bits.
func (m Mapping) Bits() int {
	n := 0
	for _, e := range m {
		n += int(e.Bits)
	}
	return n
}

// Len returns the PDO data length in bytes: Bits rounded up.
func (m Mapping) Len() int { return (m.Bits() + 7) / 8 }

// String lists the entries.
func (m Mapping) String() string {
	parts := make([]string, len(m))
	for i, e := range m {
		parts[i] = e.String()
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// Validate checks the mapping against the dictionary of the device that
// owns the PDO: every entry as [ValidateEntry] checks it, at most
// [MaxEntries] entries and [MaxBits] bits ([canopen.AbortPDOLength]).
func (m Mapping) Validate(d *od.Dictionary, transmit bool) error {
	return m.validate(d, transmit, MaxBits)
}

// ValidateFD checks the mapping of a CANopen FD PDO as [Mapping.Validate]
// does, with up to [MaxBitsFD] bits.
func (m Mapping) ValidateFD(d *od.Dictionary, transmit bool) error {
	return m.validate(d, transmit, MaxBitsFD)
}

func (m Mapping) validate(d *od.Dictionary, transmit bool, maxBits int) error {
	if len(m) > MaxEntries {
		return canopen.AbortPDOLength
	}
	for _, e := range m {
		if err := ValidateEntry(d, e, transmit); err != nil {
			return err
		}
	}
	if m.Bits() > maxBits {
		return canopen.AbortPDOLength
	}
	return nil
}

// ValidateEntry checks one mapping entry of a TPDO (transmit) or an RPDO
// against a dictionary and returns the SDO abort code a write of it gets:
//
//   - [canopen.AbortNoObject]: the object or sub-object does not exist
//   - [canopen.AbortNotMappable]: a zero length; the object is not
//     PDO-mappable, not readable (TPDO) or not writable (RPDO); a dummy
//     entry in a TPDO; a length that differs from the data type's
//
// The length must be the data type's [canopen.DataType.Bits]; a BOOLEAN
// may be mapped with 1 bit (CiA 301) or 8 bits (as tools that only map
// whole bytes write it). Variable-length types cannot be mapped. Dummy
// entries (data types 0x0001..0x0007) are accepted in RPDOs only, with
// their type's length; the dictionary need not hold the type objects.
func ValidateEntry(d *od.Dictionary, e Entry, transmit bool) error {
	if e.Bits == 0 {
		return canopen.AbortNotMappable
	}
	if e.IsDummy() {
		if transmit || !lengthFits(canopen.DataType(e.Index), int(e.Bits)) {
			return canopen.AbortNotMappable
		}
		return nil
	}
	v, err := d.Variable(e.Index, e.SubIndex)
	if err != nil {
		return canopen.AbortNoObject
	}
	if !v.PDOMappable {
		return canopen.AbortNotMappable
	}
	if transmit && !v.Access.Readable() || !transmit && !v.Access.Writable() {
		return canopen.AbortNotMappable
	}
	if !lengthFits(v.DataType, int(e.Bits)) {
		return canopen.AbortNotMappable
	}
	return nil
}

// lengthFits reports whether a mapping length suits a data type.
func lengthFits(t canopen.DataType, bits int) bool {
	b := t.Bits()
	return b != 0 && (bits == b || t == canopen.Boolean && bits == 8)
}

// Pack packs values, one per entry in the encoding of the object's data
// type (little-endian, as [canopen.Encode] returns it), into PDO data of
// [Mapping.Len] bytes. Each value contributes its low Bits bits, least
// significant first, at the bit position following the previous entry's.
// A nil value (for dummy entries, say) packs as zeros. Mappings longer
// than [MaxBits] are refused with [canopen.AbortPDOLength]; [PackFD]
// takes CANopen FD mappings.
func Pack(m Mapping, values [][]byte) ([]byte, error) { return pack(m, values, MaxBits) }

// PackFD packs the values of a CANopen FD PDO as [Pack] does, for
// mappings of up to [MaxBitsFD] bits.
func PackFD(m Mapping, values [][]byte) ([]byte, error) { return pack(m, values, MaxBitsFD) }

func pack(m Mapping, values [][]byte, maxBits int) ([]byte, error) {
	if len(values) != len(m) {
		return nil, fmt.Errorf("pdo: %d values for %d mapping entries", len(values), len(m))
	}
	total := m.Bits()
	if total > maxBits {
		return nil, canopen.AbortPDOLength
	}
	out := make([]byte, (total+7)/8)
	pos := 0
	for i, e := range m {
		v := values[i]
		if v != nil {
			if need := (int(e.Bits) + 7) / 8; len(v) < need {
				return nil, fmt.Errorf("pdo: entry %s: %d bytes, %d needed", e, len(v), need)
			}
			putBits(out, pos, v, int(e.Bits))
		}
		pos += int(e.Bits)
	}
	return out, nil
}

// putBits copies the low n bits of src (least significant bit of src[0]
// first) into dst from bit position pos; dst is zero there.
func putBits(dst []byte, pos int, src []byte, n int) {
	if pos%8 == 0 && n%8 == 0 {
		copy(dst[pos/8:], src[:n/8])
		return
	}
	for i := 0; i < n; i++ {
		if src[i/8]>>(uint(i)%8)&1 != 0 {
			p := pos + i
			dst[p/8] |= 1 << (uint(p) % 8)
		}
	}
}

// getBits returns n bits of src from bit position pos as (n+7)/8 bytes,
// least significant first.
func getBits(src []byte, pos, n int) []byte {
	out := make([]byte, (n+7)/8)
	if pos%8 == 0 && n%8 == 0 {
		copy(out, src[pos/8:pos/8+n/8])
		return out
	}
	for i := 0; i < n; i++ {
		p := pos + i
		if src[p/8]>>(uint(p)%8)&1 != 0 {
			out[i/8] |= 1 << (uint(i) % 8)
		}
	}
	return out
}

// Unpack splits PDO data into the values of the entries: each value has
// Bits/8 bytes rounded up, little-endian, which for every mappable length
// is the encoding of the object's data type (a 1-bit BOOLEAN is one byte,
// 0 or 1). Data beyond the mapping is ignored; data shorter than the
// mapping is an error wrapping [ErrLength]. Mappings longer than
// [MaxBits] are refused with [canopen.AbortPDOLength]; [UnpackFD] takes
// CANopen FD mappings.
func Unpack(m Mapping, data []byte) ([][]byte, error) { return unpack(m, data, MaxBits) }

// UnpackFD splits the data of a CANopen FD PDO as [Unpack] does, for
// mappings of up to [MaxBitsFD] bits.
func UnpackFD(m Mapping, data []byte) ([][]byte, error) { return unpack(m, data, MaxBitsFD) }

func unpack(m Mapping, data []byte, maxBits int) ([][]byte, error) {
	total := m.Bits()
	if total > maxBits {
		return nil, canopen.AbortPDOLength
	}
	if len(data)*8 < total {
		return nil, fmt.Errorf("%w: %d bytes for %d bits", ErrLength, len(data), total)
	}
	out := make([][]byte, len(m))
	pos := 0
	for i, e := range m {
		out[i] = getBits(data, pos, int(e.Bits))
		pos += int(e.Bits)
	}
	return out, nil
}
