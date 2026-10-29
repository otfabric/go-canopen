// SPDX-License-Identifier: MIT

package dissect

import (
	"fmt"
	"strings"
	"time"

	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/usdo"
)

// USDO is a USDO message of CANopen FD (CiA 1301): a request (client to
// server, CAN-ID 0x600 + the client's node-ID) or a response (server to
// client, 0x580 + the server's node-ID). The sender is [Message.Node]; the
// destination is coded in the data.
type USDO struct {
	// Client is true for a request.
	Client bool `json:"client"`
	// Dest is the destination address (byte 0): 0 broadcast, 1..127 a
	// node-ID, 0x80..0xFF a group; Destination names it.
	Dest        uint8  `json:"dest"`
	Destination string `json:"destination"`
	// CS is the command specifier (byte 1), Command its name; Remote is
	// set for the remote (routed) services, whose layout is not decoded.
	CS      uint8  `json:"cs"`
	Command string `json:"command"`
	Remote  bool   `json:"remote,omitempty"`
	// Session is the session-ID (byte 2).
	Session uint8 `json:"session"`
	// Index and SubIndex address the object (HasIndex: the message
	// carries them).
	Index    uint16 `json:"index"`
	SubIndex uint8  `json:"subIndex"`
	HasIndex bool   `json:"hasIndex"`
	// DataType is the data type byte of expedited and initiate messages.
	DataType uint8 `json:"dataType,omitempty"`
	// Size is the data size of an expedited message or a last segment,
	// or the size of the whole transfer in an initiate.
	Size uint32 `json:"size,omitempty"`
	// Counter is the segment counter.
	Counter uint8 `json:"counter,omitempty"`
	// TProc is the processing time per segment of a bulk initiate
	// response.
	TProc uint16 `json:"tProc,omitempty"`
	// Abort is the abort code of an abort, AbortText its description.
	Abort     uint8  `json:"abortCode,omitempty"`
	AbortText string `json:"abortText,omitempty"`
	// Data are the data bytes carried (expedited, segment; the rest of
	// the payload for a remote service).
	Data Bytes `json:"data,omitempty"`
}

// summaryData is how many data bytes [USDO.String] shows.
const summaryData = 16

// String summarises the message, e.g. "upload to node 5 session 3
// 0x1018:01"; it shows the first 16 data bytes.
func (u *USDO) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s to %s session %d", u.Command, u.Destination, u.Session)
	if u.HasIndex {
		fmt.Fprintf(&b, " 0x%04X:%02X", u.Index, u.SubIndex)
	}
	cmd := usdo.Command(u.CS)
	switch cmd {
	case usdo.CmdAbort:
		fmt.Fprintf(&b, " abort 0x%02X", u.Abort)
		if u.AbortText != "" {
			fmt.Fprintf(&b, " (%s)", u.AbortText)
		}
	case usdo.CmdDownloadInit, usdo.CmdBulkInit, usdo.CmdUploadInit:
		fmt.Fprintf(&b, " size %d", u.Size)
	case usdo.CmdBulkInitOK:
		fmt.Fprintf(&b, " t_proc %d", u.TProc)
	case usdo.CmdDownloadSegment, usdo.CmdBulkSegment, usdo.CmdUploadSegmentData,
		usdo.CmdUploadSegment, usdo.CmdDownloadSegmentOK:
		fmt.Fprintf(&b, " counter %d", u.Counter)
	default: // the data below says the rest
	}
	if len(u.Data) > 0 {
		fmt.Fprintf(&b, " size %d: %s", len(u.Data), u.Data[:min(len(u.Data), summaryData)])
		if len(u.Data) > summaryData {
			b.WriteString(" ...")
		}
	}
	return b.String()
}

// decodeUSDO decodes a USDO payload; problem describes a malformed one.
// It returns nil when the payload is too short for the header.
func decodeUSDO(d []byte, client bool) (u *USDO, problem string) {
	x, err := usdo.Decode(d)
	if err != nil {
		problem = err.Error()
		if len(d) < 3 {
			return nil, problem
		}
	}
	u = &USDO{
		Client: client, Dest: uint8(x.Dest), Destination: x.Dest.String(),
		CS: uint8(x.Cmd), Command: x.Cmd.String(), Remote: x.Cmd.Remote(), Session: x.Session,
		Index: x.Index, SubIndex: x.SubIndex, DataType: x.DataType, Size: x.Size,
		Counter: x.Counter, TProc: x.TProc, Abort: uint8(x.Abort), Data: Bytes(x.Data),
	}
	if !u.Remote && err == nil {
		switch x.Cmd {
		case usdo.CmdUpload, usdo.CmdDownloadExpeditedOK, usdo.CmdDownloadInitOK, usdo.CmdBulkInitOK,
			usdo.CmdDownloadExpedited, usdo.CmdUploadExpedited, usdo.CmdDownloadInit, usdo.CmdBulkInit, usdo.CmdUploadInit:
			u.HasIndex = true
		case usdo.CmdAbort:
			u.HasIndex = true
			u.AbortText = x.Abort.Text()
		default: // no object in the message
		}
	}
	return u, problem
}

// EMCYFD is the CANopen FD part of an emergency message (CiA 1301, 20
// bytes, decoded in the [emcy.FDLayoutCiA] layout); the classical part
// (code, register, device-specific field as MSEF) is in [EMCY].
type EMCYFD struct {
	LogicalDevice uint8  `json:"logicalDevice"`
	CiASpec       uint16 `json:"ciaSpec"`
	Status        uint8  `json:"status"`
	// Milliseconds after midnight and Days since 1984-01-01 of the time
	// field; Time is it in UTC.
	Milliseconds uint32    `json:"ms"`
	Days         uint16    `json:"days"`
	Time         time.Time `json:"time"`
}

// decodeFDEMCY decodes a CANopen FD emergency payload (more than 8
// bytes; bytes beyond the 20 of the message are CAN FD padding); problem
// describes a malformed one.
func decodeFDEMCY(d []byte) (e *EMCY, problem string) {
	x, err := emcy.ParseFDMessage(d)
	if err != nil {
		return nil, err.Error()
	}
	e = &EMCY{
		Code: x.Code, Register: uint8(x.Register), MSEF: Bytes(append([]byte{}, x.Specific[:]...)), Reset: x.IsReset(),
		FD: &EMCYFD{
			LogicalDevice: x.LogicalDevice, CiASpec: x.CiASpec, Status: x.Status,
			Milliseconds: x.Time.Ms, Days: x.Time.Days, Time: x.Time.Time(),
		},
	}
	return e, ""
}
