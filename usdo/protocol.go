// SPDX-License-Identifier: MIT

package usdo

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Command is a USDO command specifier (byte 1 of every USDO message).
// Local services use 01h..7Fh; the remote services (routing to another
// network segment) the same values with bit 7 set (81h..FFh).
type Command uint8

// Command specifiers of the local services, as the HMS/Ixxat CANopen FD
// whitepaper lists them (table "USDO command specifiers").
const (
	CmdDownloadExpedited   Command = 0x01 // download expedited request
	CmdDownloadInit        Command = 0x02 // download segmented initial request
	CmdDownloadSegment     Command = 0x03 // download segmented intermediate request (segment)
	CmdDownloadLast        Command = 0x04 // download segmented last request (end)
	CmdBulkInit            Command = 0x06 // download bulk transfer initial request
	CmdBulkSegment         Command = 0x07 // download bulk transfer segment
	CmdBulkLast            Command = 0x08 // download bulk transfer final
	CmdUpload              Command = 0x11 // upload request
	CmdUploadSegment       Command = 0x13 // upload segmented intermediate request (segment)
	CmdDownloadExpeditedOK Command = 0x21 // download expedited response
	CmdDownloadInitOK      Command = 0x22 // download segmented initial response
	CmdDownloadSegmentOK   Command = 0x23 // download segmented intermediate response (segment)
	CmdDownloadLastOK      Command = 0x24 // download segmented last response (end)
	CmdBulkInitOK          Command = 0x26 // download bulk transfer initial response
	CmdBulkLastOK          Command = 0x28 // download bulk transfer final response
	CmdUploadExpedited     Command = 0x31 // upload expedited response
	CmdUploadInit          Command = 0x32 // upload segmented initial response
	CmdUploadSegmentData   Command = 0x33 // upload segmented intermediate response (segment)
	CmdUploadLast          Command = 0x34 // upload segmented last response (end)
	CmdAbort               Command = 0x7F // USDO abort
	// CmdRemote is the bit that turns a local command specifier into the
	// remote one (81h..FFh).
	CmdRemote Command = 0x80
)

var cmdNames = map[Command]string{
	CmdDownloadExpedited: "download expedited", CmdDownloadInit: "download initiate", CmdDownloadSegment: "download segment",
	CmdDownloadLast: "download last segment", CmdBulkInit: "bulk initiate", CmdBulkSegment: "bulk segment",
	CmdBulkLast: "bulk last segment", CmdUpload: "upload", CmdUploadSegment: "upload segment request",
	CmdDownloadExpeditedOK: "download expedited response", CmdDownloadInitOK: "download initiate response",
	CmdDownloadSegmentOK: "download segment response", CmdDownloadLastOK: "download end response",
	CmdBulkInitOK: "bulk initiate response", CmdBulkLastOK: "bulk end response",
	CmdUploadExpedited: "upload expedited response", CmdUploadInit: "upload initiate response",
	CmdUploadSegmentData: "upload segment", CmdUploadLast: "upload last segment", CmdAbort: "abort",
}

// Remote reports whether the command is a remote (routed) service.
func (c Command) Remote() bool { return c&CmdRemote != 0 }

// String names the command, e.g. "upload" or "remote upload".
func (c Command) String() string {
	pre := ""
	if c.Remote() {
		pre = "remote "
	}
	if n, ok := cmdNames[c&^CmdRemote]; ok {
		return pre + n
	}
	return fmt.Sprintf("Command(0x%02X)", uint8(c))
}

// Address is the destination address of a USDO message (byte 0): 00h
// broadcast, 01h..7Fh a node-ID (unicast), 80h..FFh a group (multicast).
type Address uint8

// Broadcast addresses every USDO server.
const Broadcast Address = 0

// Node returns the unicast address of a node.
func Node(id canopen.NodeID) Address { return Address(id) }

// IsBroadcast reports address 00h.
func (a Address) IsBroadcast() bool { return a == Broadcast }

// IsMulticast reports a group address 80h..FFh.
func (a Address) IsMulticast() bool { return a >= 0x80 }

// NodeID returns the node-ID of a unicast address; ok is false for
// broadcast and multicast addresses.
func (a Address) NodeID() (canopen.NodeID, bool) {
	id := canopen.NodeID(a)
	return id, id.Valid()
}

// String formats the address.
func (a Address) String() string {
	switch {
	case a.IsBroadcast():
		return "broadcast"
	case a.IsMulticast():
		return fmt.Sprintf("group 0x%02X", uint8(a))
	}
	return fmt.Sprintf("node %d", uint8(a))
}

// Size limits of the protocol on 64-byte CAN FD frames.
const (
	// MaxExpedited is the largest expedited transfer: bytes 8..63.
	MaxExpedited = canbus.MaxFDLen - 8
	// SegmentSize is the data of a segment: bytes 4..63.
	SegmentSize = canbus.MaxFDLen - 4
)

// ErrCommand reports a message with an unknown command specifier.
var ErrCommand = errors.New("usdo: unknown command specifier")

// layout is the byte layout of a message.
type layout uint8

const (
	layoutUnknown   layout = iota
	layoutShort            // dest, cmd, session
	layoutCounter          // + counter (3)
	layoutObject           // + sub-index (3), index (4..5)
	layoutTProc            // object + t_proc (6..7)
	layoutAbort            // object + abort code (6)
	layoutExpedited        // object + data type (6), size (7), data (8..)
	layoutInit             // object + data type (6), reserved (7), size (8..11)
	layoutSegment          // counter + data (4..63)
	layoutLast             // size of the last segment (3), data (4..)
)

// layoutOf returns the layout of a local command.
func layoutOf(c Command) layout {
	switch c {
	case CmdDownloadExpedited, CmdUploadExpedited:
		return layoutExpedited
	case CmdDownloadInit, CmdBulkInit, CmdUploadInit:
		return layoutInit
	case CmdDownloadSegment, CmdBulkSegment, CmdUploadSegmentData:
		return layoutSegment
	case CmdDownloadLast, CmdBulkLast, CmdUploadLast:
		return layoutLast
	case CmdUpload, CmdDownloadExpeditedOK, CmdDownloadInitOK:
		return layoutObject
	case CmdBulkInitOK:
		return layoutTProc
	case CmdUploadSegment, CmdDownloadSegmentOK:
		return layoutCounter
	case CmdDownloadLastOK, CmdBulkLastOK:
		return layoutShort
	case CmdAbort:
		return layoutAbort
	default:
		return layoutUnknown
	}
}

// Message is one USDO message (the payload of one CAN FD frame), decoded.
// Which fields are meaningful depends on the command (see the package
// documentation for the byte layouts).
type Message struct {
	Dest     Address
	Cmd      Command
	Session  uint8
	SubIndex uint8
	Index    uint16
	// DataType is the data type byte of expedited and initiate messages:
	// the low byte of the CANopen data type index, 0 when unspecified.
	DataType uint8
	// Size is the data size: of the data (expedited), of the whole
	// transfer (initiate), or of the last segment.
	Size uint32
	// Counter is the segment counter.
	Counter uint8
	// TProc is the server's processing time per segment of a bulk
	// transfer, in units the client and server agree on.
	TProc uint16
	// Abort is the abort code of an abort message.
	Abort AbortCode
	// Data is the application data of expedited messages and segments.
	Data []byte
}

// String formats the message for logs.
func (m Message) String() string {
	s := fmt.Sprintf("%s to %s session %d", m.Cmd, m.Dest, m.Session)
	switch layoutOf(m.Cmd) {
	case layoutObject, layoutTProc, layoutExpedited, layoutInit:
		s += fmt.Sprintf(" object 0x%04X:%02X", m.Index, m.SubIndex)
	case layoutAbort:
		s += fmt.Sprintf(" object 0x%04X:%02X: %s", m.Index, m.SubIndex, m.Abort)
	case layoutSegment, layoutCounter:
		s += fmt.Sprintf(" counter %d", m.Counter)
	default: // nothing more to show
	}
	if len(m.Data) > 0 {
		s += fmt.Sprintf(" (%d bytes)", len(m.Data))
	}
	return s
}

// Encode returns the payload of the message, not padded (see [Frame]). It
// fails for unknown or remote commands and for data that does not fit.
func (m Message) Encode() ([]byte, error) {
	if m.Cmd.Remote() {
		return nil, fmt.Errorf("%w: remote command %s", ErrCommand, m.Cmd)
	}
	l := layoutOf(m.Cmd)
	b := []byte{byte(m.Dest), byte(m.Cmd), m.Session}
	object := func() { b = append(b, m.SubIndex, byte(m.Index), byte(m.Index>>8)) }
	switch l {
	case layoutUnknown:
		return nil, fmt.Errorf("%w: 0x%02X", ErrCommand, uint8(m.Cmd))
	case layoutShort:
	case layoutCounter:
		b = append(b, m.Counter)
	case layoutObject:
		object()
	case layoutTProc:
		object()
		b = binary.LittleEndian.AppendUint16(b, m.TProc)
	case layoutAbort:
		object()
		b = append(b, byte(m.Abort))
	case layoutExpedited:
		if len(m.Data) > MaxExpedited {
			return nil, fmt.Errorf("usdo: %d bytes do not fit an expedited message", len(m.Data))
		}
		object()
		b = append(b, m.DataType, byte(len(m.Data)))
		b = append(b, m.Data...)
	case layoutInit:
		object()
		b = append(b, m.DataType, 0)
		b = binary.LittleEndian.AppendUint32(b, m.Size)
	case layoutSegment:
		if len(m.Data) != SegmentSize {
			return nil, fmt.Errorf("usdo: segment of %d bytes, %d required", len(m.Data), SegmentSize)
		}
		b = append(b, m.Counter)
		b = append(b, m.Data...)
	case layoutLast:
		if len(m.Data) > SegmentSize {
			return nil, fmt.Errorf("usdo: last segment of %d bytes exceeds %d", len(m.Data), SegmentSize)
		}
		b = append(b, byte(len(m.Data)))
		b = append(b, m.Data...)
	}
	return b, nil
}

// minLen is the length of each layout without data.
var minLen = [...]int{
	layoutUnknown: 3, layoutShort: 3, layoutCounter: 4, layoutObject: 6, layoutTProc: 8,
	layoutAbort: 7, layoutExpedited: 8, layoutInit: 12, layoutSegment: 4 + SegmentSize, layoutLast: 4,
}

// Decode decodes a USDO payload. Bytes beyond the message (CAN FD
// padding) are ignored. A payload shorter than 3 bytes, or shorter than
// its command needs, or with a size that exceeds the data, is an
// [canopen.ErrProtocol] error; an unknown command an [ErrCommand] error
// (with Dest, Cmd and Session set, so a server can answer with an abort).
// Remote commands are returned with only Dest, Cmd and Session set and
// the rest of the payload in Data: their layout is not public.
func Decode(data []byte) (Message, error) {
	if len(data) < 3 {
		return Message{}, fmt.Errorf("%w: USDO message of %d bytes", canopen.ErrProtocol, len(data))
	}
	m := Message{Dest: Address(data[0]), Cmd: Command(data[1]), Session: data[2]}
	if m.Cmd.Remote() {
		m.Data = append([]byte(nil), data[3:]...)
		return m, nil
	}
	l := layoutOf(m.Cmd)
	if l == layoutUnknown {
		return m, fmt.Errorf("%w: 0x%02X", ErrCommand, uint8(m.Cmd))
	}
	if len(data) < minLen[l] {
		return m, fmt.Errorf("%w: %s of %d bytes, %d needed", canopen.ErrProtocol, m.Cmd, len(data), minLen[l])
	}
	if l >= layoutObject && l <= layoutInit {
		m.SubIndex, m.Index = data[3], binary.LittleEndian.Uint16(data[4:])
	}
	switch l {
	case layoutCounter:
		m.Counter = data[3]
	case layoutTProc:
		m.TProc = binary.LittleEndian.Uint16(data[6:])
	case layoutAbort:
		m.Abort = AbortCode(data[6])
	case layoutExpedited:
		m.DataType, m.Size = data[6], uint32(data[7])
		if m.Size > MaxExpedited || int(m.Size) > len(data)-8 {
			return m, fmt.Errorf("%w: expedited size %d, %d bytes of data", canopen.ErrProtocol, m.Size, len(data)-8)
		}
		m.Data = append([]byte(nil), data[8:8+m.Size]...)
	case layoutInit:
		m.DataType, m.Size = data[6], binary.LittleEndian.Uint32(data[8:])
	case layoutSegment:
		m.Counter = data[3]
		m.Data = append([]byte(nil), data[4:4+SegmentSize]...)
	case layoutLast:
		m.Size = uint32(data[3])
		if m.Size > SegmentSize || int(m.Size) > len(data)-4 {
			return m, fmt.Errorf("%w: last segment size %d, %d bytes of data", canopen.ErrProtocol, m.Size, len(data)-4)
		}
		m.Data = append([]byte(nil), data[4:4+m.Size]...)
	default: // layoutShort, layoutObject: nothing more
	}
	return m, nil
}

// Frame returns the CAN FD frame of a message sent by a node: client
// requests on 600h + the client's node-ID, server responses on 580h + the
// server's node-ID (CiA's CANopen FD poster: "CAN-ID identifies the sender
// of the USDO protocol"). The payload is padded with zeros to the next
// valid CAN FD length.
func Frame(m Message, sender canopen.NodeID, response, brs bool) (canbus.Frame, error) {
	b, err := m.Encode()
	if err != nil {
		return canbus.Frame{}, err
	}
	base := canopen.FuncSDORx
	if response {
		base = canopen.FuncSDOTx
	}
	return canbus.NewFD(base+canbus.ID(sender), false, brs, canbus.PadFD(b, 0))
}

// sender returns the node-ID that sent a USDO frame on base (600h:
// requests, 580h: responses); ok is false for other frames.
func sender(f *canbus.Frame, base canbus.ID) (canopen.NodeID, bool) {
	if f.IsExtended() || f.IsRemote() || f.IsError() || f.ID&^0x7F != base {
		return 0, false
	}
	id := canopen.NodeID(f.ID & 0x7F)
	return id, id.Valid()
}

// filter returns the receive filter for USDO frames on base: 11-bit
// identifiers base..base+7Fh, data frames.
func filter(base canbus.ID) canbus.FilterSet {
	return canbus.FilterSet{Filters: []canbus.Filter{{ID: base, Mask: 0x780, Format: canbus.FormatStandard, Remote: canbus.RemoteDataOnly}}}
}
