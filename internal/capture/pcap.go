// SPDX-License-Identifier: MIT

package capture

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
)

// ReadSocketCANPCAP reads a classic pcap file of link type
// LINKTYPE_CAN_SOCKETCAN (227) holding classical CAN frames: the 11- or
// 29-bit identifier with its RTR bit, the DLC and the data bytes the
// record holds.
//
// It is not go-canbus's capture.PCAPReader on purpose. The Wireshark wiki's
// CANopen sample capture has 0x7F in byte 5 of classical frames of 16
// bytes or fewer, where a CAN FD frame has its flags; go-canbus reads them
// as CAN FD frames because the FDF bit (0x04) is set, Wireshark 4.6 as
// classical frames because a record of at most 16 bytes cannot hold a CAN
// FD frame. This reader follows Wireshark, and refuses records longer
// than 16 bytes (CAN FD) and error frames, which the corpus does not hold.
func ReadSocketCANPCAP(b []byte) ([]canbus.Frame, error) {
	if len(b) < 24 {
		return nil, fmt.Errorf("pcap: %d bytes", len(b))
	}
	var order binary.ByteOrder
	switch binary.LittleEndian.Uint32(b) {
	case 0xA1B2C3D4:
		order = binary.LittleEndian
	case 0xD4C3B2A1:
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("pcap: not a microsecond pcap file")
	}
	if lt := order.Uint32(b[20:]); lt != 227 {
		return nil, fmt.Errorf("pcap: link type %d, want 227 (SocketCAN)", lt)
	}
	var out []canbus.Frame
	for o := 24; o < len(b); {
		if len(b)-o < 16 {
			return out, fmt.Errorf("pcap: record header cut at offset %d", o)
		}
		sec, us, n := order.Uint32(b[o:]), order.Uint32(b[o+4:]), int(order.Uint32(b[o+8:]))
		o += 16
		if n < 8 || n > 16 || len(b)-o < n {
			return out, fmt.Errorf("pcap: record of %d bytes at offset %d", n, o)
		}
		r := b[o : o+n]
		o += n
		id := binary.BigEndian.Uint32(r)
		dlc := int(r[4])
		if id&0x20000000 != 0 || dlc > 8 {
			return out, fmt.Errorf("pcap: error frame or DLC %d at offset %d", dlc, o-n)
		}
		var f canbus.Frame
		var err error
		ext := id&0x80000000 != 0
		cid := canbus.ID(id & 0x7FF)
		if ext {
			cid = canbus.ID(id & 0x1FFFFFFF)
		}
		switch {
		case id&0x40000000 != 0:
			f, err = canbus.NewRemote(cid, ext, uint8(dlc))
		case ext:
			f, err = canbus.NewExtended(cid, r[8:8+min(dlc, n-8)])
		default:
			f, err = canbus.NewStandard(cid, r[8:8+min(dlc, n-8)])
		}
		if err != nil {
			return out, err
		}
		f.Timestamp = time.Unix(int64(sec), int64(us)*1000).UTC()
		out = append(out, f)
	}
	return out, nil
}
