// SPDX-License-Identifier: MIT

package emcy

import (
	"encoding/binary"
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/timestamp"
)

// FDSize is the payload length of a CANopen FD emergency message (CiA
// 1301 EMCY write protocol): 20 bytes, a valid CAN FD length.
const FDSize = 20

// FDLayout selects the byte positions of the error register and the
// emergency error code in a CANopen FD emergency message. The public
// sources disagree on them (see the package documentation), so the layout
// is selectable; every other field has the same position in both.
type FDLayout uint8

// CANopen FD emergency layouts.
const (
	// FDLayoutCiA (the default) follows CiA's CANopen FD poster (CiA 1301,
	// 2026 edition): EEC in bytes 4..5, error register in byte 6, so that
	// bytes 4..11 are the classical 8-byte emergency message.
	FDLayoutCiA FDLayout = iota
	// FDLayoutRegisterFirst follows the HMS/Ixxat CANopen FD whitepaper:
	// error register in byte 4, EEC in bytes 5..6.
	FDLayoutRegisterFirst
)

// String names the layout.
func (l FDLayout) String() string {
	switch l {
	case FDLayoutCiA:
		return "CiA"
	case FDLayoutRegisterFirst:
		return "register-first"
	}
	return fmt.Sprintf("FDLayout(%d)", uint8(l))
}

// FDMessage is the content of a CANopen FD emergency message (CiA 1301
// EMCY write protocol), 20 bytes:
//
//	byte  0      logical device number
//	byte  1      reserved (0)
//	bytes 2..3   CiA specification number defining the EEC
//	bytes 4..5   emergency error code (EEC)        } FDLayoutCiA; with
//	byte  6      error register (object 1001h)     } FDLayoutRegisterFirst
//	                                                 register 4, EEC 5..6
//	bytes 7..11  device-specific error information
//	byte  12     status (type of error)
//	byte  13     reserved (0)
//	bytes 14..19 time (TIME_OF_DAY)
//
// Multi-byte fields are little-endian, as all CANopen values; the sources
// do not state it for this message. The encoding of Status ("type of
// error, e.g. recoverable/non recoverable") is not public: it is carried
// as a raw byte.
type FDMessage struct {
	LogicalDevice uint8
	CiASpec       uint16
	Code          uint16
	Register      ErrorRegister
	Specific      [5]byte
	Status        uint8
	Time          timestamp.TimeOfDay
}

// Message returns the classical part: code, register and the
// device-specific field as MSEF.
func (m FDMessage) Message() Message {
	return Message{Code: m.Code, Register: m.Register, MSEF: m.Specific}
}

// IsReset reports an error reset message (code 0x0000).
func (m FDMessage) IsReset() bool { return m.Code == CodeNoError }

// Bytes returns the 20-byte payload in the [FDLayoutCiA] layout.
func (m FDMessage) Bytes() [FDSize]byte { return m.BytesLayout(FDLayoutCiA) }

// BytesLayout returns the 20-byte payload in a layout.
func (m FDMessage) BytesLayout(l FDLayout) [FDSize]byte {
	var b [FDSize]byte
	b[0] = m.LogicalDevice
	binary.LittleEndian.PutUint16(b[2:], m.CiASpec)
	if l == FDLayoutRegisterFirst {
		b[4] = byte(m.Register)
		binary.LittleEndian.PutUint16(b[5:], m.Code)
	} else {
		binary.LittleEndian.PutUint16(b[4:], m.Code)
		b[6] = byte(m.Register)
	}
	copy(b[7:12], m.Specific[:])
	b[12] = m.Status
	t := m.Time.Bytes()
	copy(b[14:], t[:])
	return b
}

// Frame returns the CAN FD frame carrying the message on a COB-ID's
// identifier, with bit rate switching when brs is true.
func (m FDMessage) Frame(cob canopen.COBID, brs bool, l FDLayout) canbus.Frame {
	b := m.BytesLayout(l)
	f, _ := canbus.NewFD(cob.ID(), cob.Extended(), brs, b[:])
	return f
}

// String formats the message.
func (m FDMessage) String() string {
	return fmt.Sprintf("%s, logical device %d, CiA %d, status 0x%02X, time %s",
		m.Message(), m.LogicalDevice, m.CiASpec, m.Status, m.Time)
}

// ParseFDMessage decodes a CANopen FD emergency payload in the
// [FDLayoutCiA] layout.
func ParseFDMessage(data []byte) (FDMessage, error) { return ParseFDMessageLayout(data, FDLayoutCiA) }

// ParseFDMessageLayout decodes a CANopen FD emergency payload: at least
// [FDSize] bytes (more, CAN FD padding, are ignored); fewer are an
// [canopen.ErrProtocol] error. Reserved bytes are not checked, and the
// reserved bits of the time are ignored.
func ParseFDMessageLayout(data []byte, l FDLayout) (FDMessage, error) {
	if len(data) < FDSize {
		return FDMessage{}, fmt.Errorf("%w: CANopen FD emergency message with %d bytes", canopen.ErrProtocol, len(data))
	}
	m := FDMessage{LogicalDevice: data[0], CiASpec: binary.LittleEndian.Uint16(data[2:]), Status: data[12]}
	if l == FDLayoutRegisterFirst {
		m.Register, m.Code = ErrorRegister(data[4]), binary.LittleEndian.Uint16(data[5:])
	} else {
		m.Code, m.Register = binary.LittleEndian.Uint16(data[4:]), ErrorRegister(data[6])
	}
	copy(m.Specific[:], data[7:12])
	m.Time = timestamp.TimeOfDay{Ms: binary.LittleEndian.Uint32(data[14:]) & 0x0FFFFFFF, Days: binary.LittleEndian.Uint16(data[18:])}
	return m, nil
}

// ParseFDFrame decodes an emergency frame of a CANopen FD network: a CAN
// FD data frame of at least [FDSize] bytes is a CANopen FD message (fd
// is non-nil, and m its classical part); a classical frame, or a CAN FD
// frame of up to 8 bytes, is decoded as [ParseMessage] does.
func ParseFDFrame(f *canbus.Frame, l FDLayout) (m Message, fd *FDMessage, err error) {
	if f.Flags&(canbus.FlagRemote|canbus.FlagError) != 0 {
		return Message{}, nil, fmt.Errorf("%w: not an emergency data frame", canopen.ErrProtocol)
	}
	data := f.Payload()
	if f.IsFD() && len(data) > 8 {
		x, err := ParseFDMessageLayout(data, l)
		if err != nil {
			return Message{}, nil, err
		}
		return x.Message(), &x, nil
	}
	m, err = ParseMessage(data)
	return m, nil, err
}
