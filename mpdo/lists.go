// SPDX-License-Identifier: MIT

package mpdo

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Object ranges of the scanner and dispatcher lists (CiA 301).
const (
	// IndexScanner is the object scanner list 1; lists 1..48 are
	// 0x1FA0..0x1FCF.
	IndexScanner uint16 = 0x1FA0
	// IndexScannerLast is the object scanner list 48.
	IndexScannerLast uint16 = 0x1FCF
	// IndexDispatcher is the object dispatcher list 1; lists 1..48 are
	// 0x1FD0..0x1FFF.
	IndexDispatcher uint16 = 0x1FD0
	// IndexDispatcherLast is the object dispatcher list 48.
	IndexDispatcherLast uint16 = 0x1FFF
	// MaxLists is the number of scanner (and of dispatcher) lists.
	MaxLists = 48
	// MaxListEntries is the largest number of entries of one list
	// (sub-indices 01h..FEh).
	MaxListEntries = 254
)

// ScannerEntry is an entry of an object scanner list (UNSIGNED32, bits
// 0..7 sub-index, 8..23 index, 24..31 block size): the producer may send
// sub-indices SubIndex..SubIndex+BlockSize-1 of Index in SAM-MPDOs.
type ScannerEntry struct {
	Index     uint16 `json:"index"`
	SubIndex  uint8  `json:"subIndex"`
	BlockSize uint8  `json:"blockSize"` // 0 counts as 1
}

// ParseScannerEntry decodes the UNSIGNED32 of a scanner entry.
func ParseScannerEntry(u uint32) ScannerEntry {
	return ScannerEntry{Index: uint16(u >> 8), SubIndex: uint8(u), BlockSize: uint8(u >> 24)}
}

// Uint32 encodes the entry.
func (e ScannerEntry) Uint32() uint32 {
	return uint32(e.BlockSize)<<24 | uint32(e.Index)<<8 | uint32(e.SubIndex)
}

// Contains reports whether the entry covers a sub-object. Like Lely, a
// block that would pass sub-index FFh ends there.
func (e ScannerEntry) Contains(index uint16, sub uint8) bool {
	return e.Index == index && inBlock(e.SubIndex, e.BlockSize, sub)
}

// Validate checks the entry as the write hooks do: 0 (unused) is valid;
// otherwise the index must not be 0 and the block must end at sub-index
// FFh at the latest. A refusal is [canopen.AbortInvalidValue].
func (e ScannerEntry) Validate() error {
	if e == (ScannerEntry{}) {
		return nil
	}
	if e.Index == 0 || !blockFits(e.SubIndex, e.BlockSize) {
		return canopen.AbortInvalidValue
	}
	return nil
}

// String formats the entry as "IIII:SS+n".
func (e ScannerEntry) String() string {
	return fmt.Sprintf("%04X:%02X+%d", e.Index, e.SubIndex, blockLen(e.BlockSize))
}

// DispatcherEntry is an entry of an object dispatcher list (UNSIGNED64,
// bits 0..7 producer node-ID, 8..15 producer sub-index, 16..31 producer
// index, 32..39 local sub-index, 40..55 local index, 56..63 block size):
// a SAM-MPDO of Producer for ProducerIndex:ProducerSubIndex+k is written
// to Index:SubIndex+k, for k < BlockSize.
type DispatcherEntry struct {
	Producer         canopen.NodeID `json:"producer"`
	ProducerIndex    uint16         `json:"producerIndex"`
	ProducerSubIndex uint8          `json:"producerSubIndex"`
	Index            uint16         `json:"index"`
	SubIndex         uint8          `json:"subIndex"`
	BlockSize        uint8          `json:"blockSize"` // 0 counts as 1
}

// ParseDispatcherEntry decodes the UNSIGNED64 of a dispatcher entry.
func ParseDispatcherEntry(u uint64) DispatcherEntry {
	return DispatcherEntry{
		Producer:         canopen.NodeID(u),
		ProducerSubIndex: uint8(u >> 8),
		ProducerIndex:    uint16(u >> 16),
		SubIndex:         uint8(u >> 32),
		Index:            uint16(u >> 40),
		BlockSize:        uint8(u >> 56),
	}
}

// Uint64 encodes the entry.
func (e DispatcherEntry) Uint64() uint64 {
	return uint64(e.BlockSize)<<56 | uint64(e.Index)<<40 | uint64(e.SubIndex)<<32 |
		uint64(e.ProducerIndex)<<16 | uint64(e.ProducerSubIndex)<<8 | uint64(e.Producer)
}

// Map returns the local object of a SAM-MPDO from producer for
// index:sub, and whether the entry covers it.
func (e DispatcherEntry) Map(producer canopen.NodeID, index uint16, sub uint8) (uint16, uint8, bool) {
	if e.Producer != producer || e.ProducerIndex != index || !inBlock(e.ProducerSubIndex, e.BlockSize, sub) {
		return 0, 0, false
	}
	local := int(e.SubIndex) + int(sub-e.ProducerSubIndex)
	if local > 0xFF {
		return 0, 0, false
	}
	return e.Index, uint8(local), true
}

// Validate checks the entry as the write hooks do: 0 (unused) is valid;
// otherwise the producer must be a node-ID 1..127 (a SAM producer
// sends its own node-ID), neither index may be 0, and both blocks must
// end at sub-index FFh at the latest. A refusal is
// [canopen.AbortInvalidValue].
func (e DispatcherEntry) Validate() error {
	if e == (DispatcherEntry{}) {
		return nil
	}
	if !e.Producer.Valid() || e.ProducerIndex == 0 || e.Index == 0 ||
		!blockFits(e.ProducerSubIndex, e.BlockSize) || !blockFits(e.SubIndex, e.BlockSize) {
		return canopen.AbortInvalidValue
	}
	return nil
}

// String formats the entry as "node N IIII:SS+n -> IIII:SS".
func (e DispatcherEntry) String() string {
	return fmt.Sprintf("node %d %04X:%02X+%d -> %04X:%02X", e.Producer, e.ProducerIndex, e.ProducerSubIndex,
		blockLen(e.BlockSize), e.Index, e.SubIndex)
}

func blockLen(n uint8) int { return max(int(n), 1) }

func blockFits(first, n uint8) bool { return int(first)+blockLen(n)-1 <= 0xFF }

func inBlock(first, n, sub uint8) bool {
	return sub >= first && int(sub) <= min(int(first)+blockLen(n)-1, 0xFF)
}

// NewScannerList returns object scanner list number list (1..48, object
// 0x1FA0+list-1): an ARRAY of size UNSIGNED32 entries (1..254), rw, the
// first ones set to entries, and sub-index 00h (rw, at most size) the
// number of entries in use, len(entries).
func NewScannerList(list, size int, entries ...ScannerEntry) (*od.Object, error) {
	if err := checkList(list, size, len(entries)); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("mpdo: scanner entry %v: %w", e, err)
		}
	}
	o := od.NewArray(IndexScanner+uint16(list-1), fmt.Sprintf("Object scanner list %d", list),
		canopen.Unsigned32, od.AccessRW, size, uint32(0), od.AccessRW)
	for i, e := range entries {
		o.Subs[i+1].Default = binary.LittleEndian.AppendUint32(nil, e.Uint32())
	}
	limitCount(o, size, len(entries))
	return o, nil
}

// NewDispatcherList returns object dispatcher list number list (1..48,
// object 0x1FD0+list-1): an ARRAY of size UNSIGNED64 entries (1..254),
// rw, the first ones set to entries, and sub-index 00h (rw, at most
// size) the number of entries in use, len(entries).
func NewDispatcherList(list, size int, entries ...DispatcherEntry) (*od.Object, error) {
	if err := checkList(list, size, len(entries)); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("mpdo: dispatcher entry %v: %w", e, err)
		}
	}
	o := od.NewArray(IndexDispatcher+uint16(list-1), fmt.Sprintf("Object dispatcher list %d", list),
		canopen.Unsigned64, od.AccessRW, size, uint64(0), od.AccessRW)
	for i, e := range entries {
		o.Subs[i+1].Default = binary.LittleEndian.AppendUint64(nil, e.Uint64())
	}
	limitCount(o, size, len(entries))
	return o, nil
}

func checkList(list, size, n int) error {
	switch {
	case list < 1 || list > MaxLists:
		return fmt.Errorf("mpdo: list number %d out of range 1..%d", list, MaxLists)
	case size < 1 || size > MaxListEntries:
		return fmt.Errorf("mpdo: list size %d out of range 1..%d", size, MaxListEntries)
	case n > size:
		return fmt.Errorf("mpdo: %d entries for a list of %d", n, size)
	}
	return nil
}

func limitCount(o *od.Object, size, n int) {
	s0 := o.Subs[0]
	s0.Name = "Number of entries"
	s0.Default = []byte{uint8(n)}
	s0.High = []byte{uint8(size)}
}

// listEntries calls f with the value of each entry in use (sub-index 1 up
// to sub-index 00h) of the lists from..to present in d, in ascending
// order, until f returns true.
func listEntries(d *od.Dictionary, from, to uint16, f func(u uint64) bool) {
	for idx := uint32(from); idx <= uint32(to); idx++ {
		if d.Object(uint16(idx)) == nil {
			continue
		}
		n, err := d.GetUint(uint16(idx), 0)
		if err != nil {
			continue
		}
		for s := uint64(1); s <= n && s <= MaxListEntries; s++ {
			u, err := d.GetUint(uint16(idx), uint8(s))
			if err != nil || u == 0 {
				continue
			}
			if f(u) {
				return
			}
		}
	}
}

// ScannerEntries returns the entries in use of every object scanner list
// of d (zero entries skipped).
func ScannerEntries(d *od.Dictionary) []ScannerEntry {
	var out []ScannerEntry
	listEntries(d, IndexScanner, IndexScannerLast, func(u uint64) bool {
		out = append(out, ParseScannerEntry(uint32(u)))
		return false
	})
	return out
}

// DispatcherEntries returns the entries in use of every object dispatcher
// list of d (zero entries skipped).
func DispatcherEntries(d *od.Dictionary) []DispatcherEntry {
	var out []DispatcherEntry
	listEntries(d, IndexDispatcher, IndexDispatcherLast, func(u uint64) bool {
		out = append(out, ParseDispatcherEntry(u))
		return false
	})
	return out
}

// Scanned reports whether the object scanner lists of d allow a SAM-MPDO
// of index:sub (Lely co_dev_chk_sam_mpdo).
func Scanned(d *od.Dictionary, index uint16, sub uint8) bool {
	found := false
	listEntries(d, IndexScanner, IndexScannerLast, func(u uint64) bool {
		found = ParseScannerEntry(uint32(u)).Contains(index, sub)
		return found
	})
	return found
}

// Dispatch returns the local object a SAM-MPDO of producer for index:sub
// is written to, from the first matching entry of the object dispatcher
// lists of d (Lely co_dev_map_sam_mpdo), and whether there is one.
func Dispatch(d *od.Dictionary, producer canopen.NodeID, index uint16, sub uint8) (uint16, uint8, bool) {
	var li uint16
	var ls uint8
	found := false
	listEntries(d, IndexDispatcher, IndexDispatcherLast, func(u uint64) bool {
		li, ls, found = ParseDispatcherEntry(u).Map(producer, index, sub)
		return found
	})
	return li, ls, found
}

// ProtectLists installs write hooks on every object scanner and dispatcher
// list of d that validate remote writes of entries ([ScannerEntry.Validate],
// [DispatcherEntry.Validate]); the number of entries (sub-index 00h) is
// bounded by the object's limits. Local writes are not checked. The
// returned function removes the hooks (later calls do nothing). Lists
// added to d afterwards are not covered.
func ProtectLists(d *od.Dictionary) (unprotect func()) {
	var rms []func()
	for idx := uint32(IndexScanner); idx <= uint32(IndexDispatcherLast); idx++ {
		if d.Object(uint16(idx)) == nil {
			continue
		}
		scanner := uint16(idx) <= IndexScannerLast
		rms = append(rms, d.OnWrite(uint16(idx), -1, func(v *od.Variable, data []byte) error {
			if v.SubIndex == 0 {
				return nil
			}
			u := canopen.DecodeUint(data)
			if scanner {
				return ParseScannerEntry(uint32(u)).Validate()
			}
			return ParseDispatcherEntry(u).Validate()
		}))
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, rm := range rms {
				rm()
			}
		})
	}
}
