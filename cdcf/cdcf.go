// SPDX-License-Identifier: MIT

// Package cdcf is the concise DCF of CiA 302-3: the binary form of a node
// configuration that a CANopen manager keeps in its object 0x1F22 (one
// DOMAIN sub-object per node) and writes to the node at boot-up.
//
// A concise DCF is a little-endian UNSIGNED32 number of entries followed
// by the entries, each an UNSIGNED16 index, an UNSIGNED8 sub-index, an
// UNSIGNED32 data size and that many bytes of data (the value in the
// encoding of the object's data type). The entries are written to the
// node in the order they appear.
//
//	entries, _ := cdcf.FromFile(dcf, 5)
//	blob, _ := cdcf.Encode(entries)
//	_ = managerDict.Set(0x1F22, 5, blob)
//
// [Entries] extracts the configuration of a DCF (eds.File) as entries;
// [FromFile] also orders the PDO parameters in the sequence CiA 301
// requires (invalidate the PDO, clear the mapping, write the mapping,
// set the number of entries, make the PDO valid), so that the result can
// be written as it is. [PDOSequence] does that ordering for any entry
// list.
//
// Sources: the format is that of Lely lely-core (co_dev_read_dcf,
// co_csdo_dn_dcf_req and the dcfgen tool), CANopenNode and the CiA 302-3
// object descriptions published by CiA members' device manuals.
package cdcf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Object indices of CiA 302-3 that hold or describe concise DCFs.
const (
	// IndexConciseDCF is the concise DCF of each node (ARRAY of DOMAIN,
	// sub-index = node-ID) in a CANopen manager.
	IndexConciseDCF uint16 = 0x1F22
	// IndexExpectedConfigDate is the expected configuration date of
	// each node (UNSIGNED32, days since 1984-01-01), compared with the
	// node's 0x1020 sub-index 1.
	IndexExpectedConfigDate uint16 = 0x1F26
	// IndexExpectedConfigTime is the expected configuration time of
	// each node (UNSIGNED32, ms after midnight), compared with the
	// node's 0x1020 sub-index 2.
	IndexExpectedConfigTime uint16 = 0x1F27
	// IndexVerifyConfiguration is a node's verify configuration object
	// (CiA 301): sub-index 1 configuration date, sub-index 2
	// configuration time.
	IndexVerifyConfiguration uint16 = 0x1020
)

// headerSize is the size of the entry count; entrySize the size of an
// entry without its data.
const (
	headerSize = 4
	entrySize  = 2 + 1 + 4
)

// ErrFormat reports a malformed concise DCF.
var ErrFormat = errors.New("cdcf: malformed concise DCF")

// Entry is one value to write into a node's dictionary.
type Entry struct {
	Index    uint16
	SubIndex uint8
	// Data is the value in the encoding of the object's data type
	// (canopen.Encode).
	Data []byte
}

// String formats the entry as "IIII:SS=hex".
func (e Entry) String() string { return fmt.Sprintf("%04X:%02X=% X", e.Index, e.SubIndex, e.Data) }

// Size returns the size of the concise DCF of entries.
func Size(entries []Entry) int {
	n := headerSize
	for _, e := range entries {
		n += entrySize + len(e.Data)
	}
	return n
}

// Encode returns the concise DCF of entries. It fails only when an
// entry's data or the number of entries exceeds the UNSIGNED32 range.
func Encode(entries []Entry) ([]byte, error) {
	if uint64(len(entries)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: %d entries", ErrFormat, len(entries))
	}
	b := make([]byte, 0, Size(entries))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(entries)))
	for _, e := range entries {
		if uint64(len(e.Data)) > math.MaxUint32 {
			return nil, fmt.Errorf("%w: %04X:%02X: %d bytes of data", ErrFormat, e.Index, e.SubIndex, len(e.Data))
		}
		b = binary.LittleEndian.AppendUint16(b, e.Index)
		b = append(b, e.SubIndex)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(e.Data)))
		b = append(b, e.Data...)
	}
	return b, nil
}

// Decode parses a concise DCF. An empty input is an empty configuration
// (an unset 0x1F22 sub-object); otherwise the input must hold exactly the
// number of entries its header announces, without trailing bytes. The
// entries' data are copies.
func Decode(b []byte) ([]Entry, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if len(b) < headerSize {
		return nil, fmt.Errorf("%w: %d bytes, the header needs %d", ErrFormat, len(b), headerSize)
	}
	n := binary.LittleEndian.Uint32(b)
	b = b[headerSize:]
	// Each entry takes at least entrySize bytes: bound the allocation by
	// the input, not by the announced count.
	if uint64(n) > uint64(len(b)/entrySize) {
		return nil, fmt.Errorf("%w: %d entries announced in %d bytes", ErrFormat, n, len(b))
	}
	out := make([]Entry, 0, n)
	for i := uint32(0); i < n; i++ {
		if len(b) < entrySize {
			return nil, fmt.Errorf("%w: entry %d truncated", ErrFormat, i+1)
		}
		e := Entry{Index: binary.LittleEndian.Uint16(b), SubIndex: b[2]}
		size := binary.LittleEndian.Uint32(b[3:])
		b = b[entrySize:]
		if uint64(size) > uint64(len(b)) {
			return nil, fmt.Errorf("%w: entry %d (%04X:%02X) announces %d bytes, %d left", ErrFormat, i+1, e.Index, e.SubIndex, size, len(b))
		}
		e.Data = append([]byte{}, b[:size]...)
		b = b[size:]
		out = append(out, e)
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("%w: %d bytes after the last entry", ErrFormat, len(b))
	}
	return out, nil
}
