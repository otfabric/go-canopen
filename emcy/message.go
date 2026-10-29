// SPDX-License-Identifier: MIT

package emcy

import (
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Message is the content of an emergency message (CiA 301 7.2.7.3.1):
// the error code, the error register and the manufacturer-specific error
// field (MSEF).
type Message struct {
	Code     uint16
	Register ErrorRegister
	MSEF     [5]byte
}

// Bytes returns the 8-byte payload: code (little-endian), register,
// MSEF.
func (m Message) Bytes() [8]byte {
	var b [8]byte
	b[0], b[1], b[2] = byte(m.Code), byte(m.Code>>8), byte(m.Register)
	copy(b[3:], m.MSEF[:])
	return b
}

// Frame returns the classical data frame carrying the message on a
// COB-ID's identifier.
func (m Message) Frame(cob canopen.COBID) canbus.Frame {
	b := m.Bytes()
	var f canbus.Frame
	if cob.Extended() {
		f, _ = canbus.NewExtended(cob.ID(), b[:])
	} else {
		f, _ = canbus.NewStandard(cob.ID(), b[:])
	}
	return f
}

// IsReset reports an error reset message (code 0x0000).
func (m Message) IsReset() bool { return m.Code == CodeNoError }

// String formats the message, e.g. "0x8130 life guard error or heartbeat
// error, register generic|communication, msef 0000000000".
func (m Message) String() string {
	return fmt.Sprintf("0x%04X %s, register %s, msef %X", m.Code, CodeText(m.Code), m.Register, m.MSEF[:])
}

// ParseMessage decodes an emergency payload. CiA 301 sends 8 bytes; some
// devices send fewer, which is accepted when the code and the register are
// present (the missing MSEF bytes are 0). Fewer than 3 or more than 8
// bytes are an [canopen.ErrProtocol] error.
func ParseMessage(data []byte) (Message, error) {
	if len(data) < 3 || len(data) > 8 {
		return Message{}, fmt.Errorf("%w: emergency message with %d bytes", canopen.ErrProtocol, len(data))
	}
	m := Message{Code: uint16(data[0]) | uint16(data[1])<<8, Register: ErrorRegister(data[2])}
	copy(m.MSEF[:], data[3:])
	return m, nil
}

// ParseFrame decodes an emergency frame: a classical data frame (not
// remote, not CAN FD) whose payload [ParseMessage] accepts.
func ParseFrame(f *canbus.Frame) (Message, error) {
	if f.Flags&(canbus.FlagRemote|canbus.FlagFD|canbus.FlagError) != 0 {
		return Message{}, fmt.Errorf("%w: not an emergency data frame", canopen.ErrProtocol)
	}
	n := int(f.Len)
	if n > len(f.Data) {
		n = len(f.Data)
	}
	return ParseMessage(f.Data[:n])
}
