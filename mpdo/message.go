// SPDX-License-Identifier: MIT

package mpdo

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/otfabric/go-canopen"
)

// Size is the length of an MPDO in bytes.
const Size = 8

// addrDAM is bit 7 of the address byte: destination address mode.
const addrDAM = 0x80

// ErrLength reports MPDO data shorter than [Size] bytes.
var ErrLength = errors.New("mpdo: MPDO shorter than 8 bytes")

// Message is the content of an MPDO.
type Message struct {
	// DAM is true in destination address mode, false in source address
	// mode.
	DAM bool `json:"dam"`
	// Node is the consumer's node-ID in DAM (0: every consumer), the
	// producer's in SAM. Only bits 0..6 are carried.
	Node canopen.NodeID `json:"node"`
	// Index and SubIndex name the object: the consumer's in DAM, the
	// producer's in SAM.
	Index    uint16 `json:"index"`
	SubIndex uint8  `json:"subIndex"`
	// Data is the value, padded with zeros.
	Data [4]byte `json:"data"`
}

// Bytes encodes the message. Bit 7 of Node is dropped.
func (m Message) Bytes() [Size]byte {
	var b [Size]byte
	b[0] = byte(m.Node) & 0x7F
	if m.DAM {
		b[0] |= addrDAM
	}
	binary.LittleEndian.PutUint16(b[1:], m.Index)
	b[3] = m.SubIndex
	copy(b[4:], m.Data[:])
	return b
}

// String describes the message.
func (m Message) String() string {
	mode := "SAM from"
	if m.DAM {
		mode = "DAM to"
	}
	return fmt.Sprintf("%s node %d %04X:%02X % X", mode, m.Node, m.Index, m.SubIndex, m.Data[:])
}

// ParseMessage decodes an MPDO. It uses the first 8 bytes of data and
// fails with [ErrLength] when there are fewer.
func ParseMessage(data []byte) (Message, error) {
	if len(data) < Size {
		return Message{}, fmt.Errorf("%w: %d bytes", ErrLength, len(data))
	}
	m := Message{
		DAM:      data[0]&addrDAM != 0,
		Node:     canopen.NodeID(data[0] & 0x7F),
		Index:    binary.LittleEndian.Uint16(data[1:]),
		SubIndex: data[3],
	}
	copy(m.Data[:], data[4:Size])
	return m, nil
}
