// SPDX-License-Identifier: MIT

// Package dissect interprets CAN frames as CANopen messages by the
// predefined connection set (CiA 301 7.3.5, [canopen.Classify]): NMT,
// SYNC, TIME, EMCY, PDOs, SDO requests and responses, NMT error control
// (heartbeat, boot-up, node guarding) and LSS (CiA 305). It is meant for
// bus monitors and traces.
//
// [Decode] is stateless: each frame is decoded on its own. SDO segments
// do not carry the object they belong to, and the data segments of an SDO
// block transfer cannot be told from SDO commands by their content; a
// [Tracker] follows the SDO transfers of every node and fills in both.
//
// Devices may move COBs away from the predefined connection set (a PDO
// on another identifier, say); such frames decode by their identifier's
// default meaning, as Wireshark's CANopen dissector does.
package dissect

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Kind is what a message is, as a JSON-friendly string.
type Kind string

// Message kinds.
const (
	KindNMT           Kind = "nmt"
	KindSYNC          Kind = "sync"
	KindTIME          Kind = "time"
	KindEMCY          Kind = "emcy"
	KindTPDO          Kind = "tpdo"
	KindRPDO          Kind = "rpdo"
	KindSDORequest    Kind = "sdo-request"  // client to server (0x600 + node)
	KindSDOResponse   Kind = "sdo-response" // server to client (0x580 + node)
	KindHeartbeat     Kind = "heartbeat"
	KindBootUp        Kind = "boot-up"
	KindGuardRequest  Kind = "guard-request"  // node guarding RTR
	KindGuardResponse Kind = "guard-response" // node guarding reply (toggle bit set)
	KindLSSMaster     Kind = "lss-master"
	KindLSSSlave      Kind = "lss-slave"
	KindUnknown       Kind = "unknown"
)

// Bytes is binary data that encodes to JSON as space-separated hex.
type Bytes []byte

// MarshalText implements encoding.TextMarshaler.
func (b Bytes) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// String formats the bytes as "01 02 03".
func (b Bytes) String() string { return fmt.Sprintf("% X", []byte(b)) }

// Message is a decoded CANopen message. Exactly one of the service
// fields (NMT, SYNC, ...) is set, as Kind says, except for unknown
// frames and remote requests on PDOs.
type Message struct {
	// Time is the frame's receive time.
	Time time.Time `json:"time"`
	// ID, Extended, Remote and Data describe the frame.
	ID       canbus.ID `json:"id"`
	Extended bool      `json:"extended,omitempty"`
	Remote   bool      `json:"rtr,omitempty"`
	Data     Bytes     `json:"data"`
	// Kind and Service say what the frame is; Node is the node-ID the
	// identifier carries (0 for NMT, SYNC, TIME and LSS).
	Kind    Kind            `json:"kind"`
	Service canopen.Service `json:"-"`
	Node    canopen.NodeID  `json:"node,omitempty"`

	NMT          *NMT          `json:"nmt,omitempty"`
	SYNC         *SYNC         `json:"sync,omitempty"`
	TIME         *TIME         `json:"timeStamp,omitempty"`
	EMCY         *EMCY         `json:"emcy,omitempty"`
	PDO          *PDO          `json:"pdo,omitempty"`
	SDO          *SDO          `json:"sdo,omitempty"`
	ErrorControl *ErrorControl `json:"errorControl,omitempty"`
	LSS          *LSS          `json:"lss,omitempty"`

	// Error describes what is malformed about the frame (a wrong length,
	// say); the fields hold what could be decoded.
	Error string `json:"error,omitempty"`
}

// NMT is an NMT module control message.
type NMT struct {
	Command canopen.NMTCommand `json:"cs"`
	Name    string             `json:"command"`
	// Target is the addressed node, 0 for all nodes.
	Target canopen.NodeID `json:"target"`
}

// SYNC is a SYNC message.
type SYNC struct {
	// Counter is the SYNC counter, when the message has one.
	Counter    uint8 `json:"counter,omitempty"`
	HasCounter bool  `json:"hasCounter"`
}

// TIME is a TIME stamp message: a TIME_OF_DAY.
type TIME struct {
	// Milliseconds after midnight (28 bits).
	Milliseconds uint32 `json:"ms"`
	// Days since 1984-01-01.
	Days uint16 `json:"days"`
	// Time is the time stamp in UTC.
	Time time.Time `json:"time"`
}

// TimeOfDayEpoch is the epoch of the CANopen TIME_OF_DAY: 1984-01-01 UTC.
var TimeOfDayEpoch = time.Date(1984, 1, 1, 0, 0, 0, 0, time.UTC)

// DecodeTimeOfDay decodes the 6 bytes of a TIME_OF_DAY (CiA 301
// 7.1.6.5): milliseconds after midnight in the low 28 bits of the first
// four bytes, days since 1984-01-01 in the last two.
func DecodeTimeOfDay(b []byte) (ms uint32, days uint16, t time.Time, err error) {
	if len(b) < 6 {
		return 0, 0, time.Time{}, fmt.Errorf("%w: TIME_OF_DAY needs 6 bytes, got %d", canopen.ErrProtocol, len(b))
	}
	ms = uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3]&0x0F)<<24
	days = uint16(b[4]) | uint16(b[5])<<8
	t = TimeOfDayEpoch.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond)
	return ms, days, t, nil
}

// EMCY is an emergency message.
type EMCY struct {
	// Code is the emergency error code; 0x0000 is an error reset.
	Code uint16 `json:"code"`
	// Register is the error register (0x1001).
	Register uint8 `json:"register"`
	// MSEF is the manufacturer-specific error field.
	MSEF  Bytes `json:"msef"`
	Reset bool  `json:"reset,omitempty"`
}

// PDO is a process data object.
type PDO struct {
	// Number is the PDO number 1..4 of the predefined connection set.
	Number int `json:"number"`
	// Transmit is true for a TPDO (sent by the node), false for an RPDO.
	Transmit bool `json:"transmit"`
}

// ErrorControl is an NMT error control message of a node: heartbeat,
// boot-up or a node guarding reply.
type ErrorControl struct {
	State canopen.NMTState `json:"state"`
	Name  string           `json:"stateName"`
	// Toggle is the toggle bit of a node guarding reply.
	Toggle bool `json:"toggle,omitempty"`
}

// LSS is a layer setting services message (CiA 305).
type LSS struct {
	// CS is the command specifier, Command its name.
	CS      uint8  `json:"cs"`
	Command string `json:"command"`
	// Value is bytes 1..4 as a little-endian UNSIGNED32: the identity
	// field, node-ID, mode or error code, as the command defines.
	Value uint32 `json:"value"`
}

// MarshalJSON adds the String summary to the message's fields.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	return json.Marshal(struct {
		plain
		Summary string `json:"summary"`
	}{plain(m), m.String()})
}

// Decode interprets a frame by the predefined connection set. It never
// fails: frames it cannot interpret are KindUnknown, malformed ones carry
// an Error.
func Decode(f *canbus.Frame) Message {
	m := Message{Time: f.Timestamp, ID: f.ID, Extended: f.IsExtended(), Remote: f.IsRemote(), Kind: KindUnknown}
	m.Data = Bytes(append([]byte(nil), f.Payload()...))
	if f.IsExtended() || f.IsError() || f.IsFD() {
		return m
	}
	svc, node, pdo := canopen.Classify(f.ID)
	m.Service, m.Node = svc, node
	d := []byte(m.Data)
	if f.IsRemote() {
		switch svc {
		case canopen.ServiceTPDO, canopen.ServiceRPDO:
			m.Kind = KindTPDO
			if svc == canopen.ServiceRPDO {
				m.Kind = KindRPDO
			}
			m.PDO = &PDO{Number: pdo, Transmit: svc == canopen.ServiceTPDO}
		case canopen.ServiceHeartbeat:
			m.Kind = KindGuardRequest
		default:
			m.Error = "remote request on a COB that has none"
		}
		return m
	}
	switch svc {
	case canopen.ServiceNMT:
		m.Kind = KindNMT
		if len(d) != 2 {
			m.Error = fmt.Sprintf("NMT needs 2 bytes, got %d", len(d))
		}
		if len(d) >= 1 {
			c := canopen.NMTCommand(d[0])
			m.NMT = &NMT{Command: c, Name: c.String()}
			if !c.Valid() && m.Error == "" {
				m.Error = fmt.Sprintf("unknown NMT command 0x%02X", d[0])
			}
			if len(d) >= 2 {
				m.NMT.Target = canopen.NodeID(d[1])
			}
		}
	case canopen.ServiceSYNC:
		m.Kind = KindSYNC
		m.SYNC = &SYNC{}
		switch len(d) {
		case 0:
		case 1:
			m.SYNC.Counter, m.SYNC.HasCounter = d[0], true
		default:
			m.Error = fmt.Sprintf("SYNC with %d bytes", len(d))
		}
	case canopen.ServiceTIME:
		m.Kind = KindTIME
		ms, days, t, err := DecodeTimeOfDay(d)
		if err != nil {
			m.Error = err.Error()
			break
		}
		m.TIME = &TIME{Milliseconds: ms, Days: days, Time: t}
	case canopen.ServiceEMCY:
		m.Kind = KindEMCY
		if len(d) < 3 {
			m.Error = fmt.Sprintf("EMCY needs 8 bytes, got %d", len(d))
			break
		}
		code := uint16(d[0]) | uint16(d[1])<<8
		m.EMCY = &EMCY{Code: code, Register: d[2], MSEF: Bytes(append([]byte{}, d[3:]...)), Reset: code == 0}
		if len(d) != 8 {
			m.Error = fmt.Sprintf("EMCY needs 8 bytes, got %d", len(d))
		}
	case canopen.ServiceTPDO, canopen.ServiceRPDO:
		m.Kind = KindTPDO
		if svc == canopen.ServiceRPDO {
			m.Kind = KindRPDO
		}
		m.PDO = &PDO{Number: pdo, Transmit: svc == canopen.ServiceTPDO}
	case canopen.ServiceSDORx, canopen.ServiceSDOTx:
		client := svc == canopen.ServiceSDORx
		m.Kind = KindSDOResponse
		if client {
			m.Kind = KindSDORequest
		}
		m.SDO, m.Error = decodeSDO(d, client)
	case canopen.ServiceHeartbeat:
		if len(d) != 1 {
			m.Kind = KindHeartbeat
			m.Error = fmt.Sprintf("NMT error control needs 1 byte, got %d", len(d))
			if len(d) == 0 {
				break
			}
		}
		st := canopen.NMTState(d[0] & 0x7F)
		m.ErrorControl = &ErrorControl{State: st, Name: st.String(), Toggle: d[0]&0x80 != 0}
		switch {
		case m.Kind == KindHeartbeat:
		case d[0]&0x80 != 0:
			m.Kind = KindGuardResponse
		case st == canopen.StateInitialising:
			m.Kind = KindBootUp
		default:
			m.Kind = KindHeartbeat
		}
	case canopen.ServiceLSS:
		m.Kind = KindLSSSlave
		if f.ID == canopen.LSSMasterID {
			m.Kind = KindLSSMaster
		}
		if len(d) == 0 {
			m.Error = "empty LSS message"
			break
		}
		var v [4]byte
		if len(d) > 1 {
			copy(v[:], d[1:])
		}
		m.LSS = &LSS{CS: d[0], Command: LSSCommand(d[0]), Value: uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16 | uint32(v[3])<<24}
		if len(d) != 8 {
			m.Error = fmt.Sprintf("LSS needs 8 bytes, got %d", len(d))
		}
	default: // no special case
	}
	return m
}

var lssCommands = map[uint8]string{
	0x04: "switch state global",
	0x11: "configure node-ID",
	0x13: "configure bit timing",
	0x15: "activate bit timing",
	0x17: "store configuration",
	0x40: "switch state selective vendor-ID",
	0x41: "switch state selective product-code",
	0x42: "switch state selective revision-number",
	0x43: "switch state selective serial-number",
	0x44: "switch state selective response",
	0x46: "identify remote slave vendor-ID",
	0x47: "identify remote slave product-code",
	0x48: "identify remote slave revision-number low",
	0x49: "identify remote slave revision-number high",
	0x4A: "identify remote slave serial-number low",
	0x4B: "identify remote slave serial-number high",
	0x4C: "identify non-configured remote slave",
	0x4F: "identify slave",
	0x50: "identify non-configured slave",
	0x51: "fastscan",
	0x5A: "inquire vendor-ID",
	0x5B: "inquire product-code",
	0x5C: "inquire revision-number",
	0x5D: "inquire serial-number",
	0x5E: "inquire node-ID",
}

// LSSCommand names an LSS command specifier (CiA 305).
func LSSCommand(cs uint8) string {
	if s, ok := lssCommands[cs]; ok {
		return s
	}
	return fmt.Sprintf("LSS command 0x%02X", cs)
}

// String returns a one-line summary of the message, e.g. "NMT start node
// 5", "SDO request node 5: initiate upload 0x1018:01".
func (m Message) String() string {
	var s string
	switch {
	case m.Kind == KindNMT && m.NMT != nil:
		target := fmt.Sprintf("node %d", m.NMT.Target)
		if m.NMT.Target == 0 {
			target = "all nodes"
		}
		s = fmt.Sprintf("NMT %s %s", m.NMT.Name, target)
	case m.Kind == KindSYNC && m.SYNC != nil:
		s = "SYNC"
		if m.SYNC.HasCounter {
			s += fmt.Sprintf(" counter=%d", m.SYNC.Counter)
		}
	case m.Kind == KindTIME && m.TIME != nil:
		s = "TIME " + m.TIME.Time.Format("2006-01-02 15:04:05.000")
	case m.Kind == KindEMCY && m.EMCY != nil:
		if m.EMCY.Reset {
			s = fmt.Sprintf("EMCY node %d: error reset, register 0x%02X", m.Node, m.EMCY.Register)
		} else {
			s = fmt.Sprintf("EMCY node %d: error 0x%04X register 0x%02X msef %s", m.Node, m.EMCY.Code, m.EMCY.Register, m.EMCY.MSEF)
		}
	case m.PDO != nil:
		dir := "RPDO"
		if m.PDO.Transmit {
			dir = "TPDO"
		}
		if m.Remote {
			s = fmt.Sprintf("%s%d node %d: RTR", dir, m.PDO.Number, m.Node)
		} else {
			s = fmt.Sprintf("%s%d node %d: %s", dir, m.PDO.Number, m.Node, m.Data)
		}
	case m.SDO != nil:
		dir := "response"
		if m.SDO.Client {
			dir = "request"
		}
		s = fmt.Sprintf("SDO %s node %d: %s", dir, m.Node, m.SDO)
	case m.Kind == KindGuardRequest:
		s = fmt.Sprintf("node guarding request node %d", m.Node)
	case m.Kind == KindBootUp:
		s = fmt.Sprintf("boot-up node %d", m.Node)
	case m.ErrorControl != nil && m.Kind == KindGuardResponse:
		t := 0
		if m.ErrorControl.Toggle {
			t = 1
		}
		s = fmt.Sprintf("node guarding node %d: %s toggle=%d", m.Node, m.ErrorControl.Name, t)
	case m.ErrorControl != nil:
		s = fmt.Sprintf("heartbeat node %d: %s", m.Node, m.ErrorControl.Name)
	case m.LSS != nil:
		who := "slave"
		if m.Kind == KindLSSMaster {
			who = "master"
		}
		s = fmt.Sprintf("LSS %s: %s (% X)", who, m.LSS.Command, []byte(m.Data[1:]))
	default:
		id := fmt.Sprintf("%03X", uint32(m.ID))
		if m.Extended {
			id = fmt.Sprintf("%08X", uint32(m.ID))
		}
		if m.Remote {
			s = fmt.Sprintf("%s %s RTR", m.Kind, id)
		} else {
			s = fmt.Sprintf("%s %s: %s", m.Kind, id, m.Data)
		}
	}
	if m.Error != "" {
		s += " [" + m.Error + "]"
	}
	return s
}
