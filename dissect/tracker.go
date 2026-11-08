// SPDX-License-Identifier: MIT

package dissect

import (
	"sync"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// sdoState is where a node's default SDO channel is in a transfer.
type sdoState uint8

const (
	stIdle sdoState = iota
	stDownload
	stUpload
	stBlockDownInit
	stBlockDownData
	stBlockDownEnd
	stBlockUpInit
	stBlockUpData
	stBlockUpEnd
)

// transfer is the SDO transfer a node's default SDO channel is in.
type transfer struct {
	state     sdoState
	index     uint16
	sub       uint8
	expedited bool
	lastSeg   bool   // the data sender sent its last segment
	value     []byte // data of an expedited download, applied when confirmed
}

// pdoRef names a PDO of a node, as its communication parameter
// (0x1400.. RPDO, 0x1800.. TPDO) does.
type pdoRef struct {
	node     canopen.NodeID
	number   int
	transmit bool
}

// Tracker decodes frames like [Decode] and follows the SDO transfers on
// the default SDO channel of every node, so that it can attribute SDO
// segments to the object being transferred (SDO.HasIndex) and decode the
// data segments of block transfers (PhaseSubBlock), which [Decode] reads
// as commands. It must see every frame of a transfer, in bus order; a
// missed frame confuses it until the next initiate or abort. A Tracker is
// safe for concurrent use.
//
// It also uses what the bus shows of the network's configuration:
//
//   - A node guarding reply that follows a node guarding request is a
//     [KindGuardResponse] whatever its toggle bit, where [Decode], seeing
//     the frame alone, takes a reply with the toggle bit clear for a
//     heartbeat.
//   - A PDO COB-ID that an expedited SDO transfer of a communication
//     parameter (0x1400..0x15FF RPDO, 0x1800..0x19FF TPDO, sub-index 1)
//     sets or reads names that node's PDO: frames on it decode as that PDO
//     (with [PDO.Configured] set), where the predefined connection set
//     would call them another node's PDO or nothing (a recorded Copley
//     drive sends its TPDO5 on 0x180). A COB-ID with bit 31 set (PDO not
//     valid) forgets the PDO's identifier. Downloads count once the server
//     confirms them. When a node's TPDO and another node's RPDO share an
//     identifier (the producer and a consumer of the PDO), the frame is the
//     TPDO; an identifier known only from consumers' RPDOs names the last
//     one learned.
type Tracker struct {
	mu     sync.Mutex
	nodes  map[canopen.NodeID]*transfer
	guard  map[canopen.NodeID]bool // node guarding request not yet answered
	pdoIDs map[canbus.ID]pdoRef    // identifiers learned from SDO transfers
}

// NewTracker returns a tracker with no transfers in progress.
func NewTracker() *Tracker {
	t := &Tracker{}
	t.Reset()
	return t
}

// Reset forgets every transfer in progress, outstanding node guarding
// requests and the PDO identifiers learned.
func (t *Tracker) Reset() {
	t.mu.Lock()
	t.nodes = map[canopen.NodeID]*transfer{}
	t.guard = map[canopen.NodeID]bool{}
	t.pdoIDs = map[canbus.ID]pdoRef{}
	t.mu.Unlock()
}

// Decode decodes a frame, using and updating the SDO transfer state.
func (t *Tracker) Decode(f *canbus.Frame) Message {
	m := Decode(f)
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case m.Kind == KindGuardRequest:
		t.guard[m.Node] = true
		return m
	case m.ErrorControl != nil && m.Error == "" && m.Service == canopen.ServiceHeartbeat:
		if t.guard[m.Node] {
			delete(t.guard, m.Node)
			if m.ErrorControl.State != canopen.StateInitialising || m.ErrorControl.Toggle {
				m.Kind = KindGuardResponse
			}
		}
		return m
	case !f.IsExtended() && !f.IsError() && !f.IsFD() && (m.Kind == KindUnknown || m.PDO != nil):
		if ref, ok := t.pdoIDs[f.ID]; ok {
			m.Kind, m.Service, m.Node, m.Error = KindRPDO, canopen.ServiceRPDO, ref.node, ""
			if ref.transmit {
				m.Kind, m.Service = KindTPDO, canopen.ServiceTPDO
			}
			m.PDO = &PDO{Number: ref.number, Transmit: ref.transmit, Configured: true}
		}
		return m
	case m.SDO == nil:
		return m
	}
	tr := t.nodes[m.Node]
	if tr == nil {
		tr = &transfer{}
		t.nodes[m.Node] = tr
	}
	client := m.SDO.Client
	d := []byte(m.Data)
	// In the data phase of a block transfer, the sender's frames are
	// segments, except an abort (seqno 0 is not a segment).
	if (tr.state == stBlockDownData && client || tr.state == stBlockUpData && !client) && len(d) > 0 && d[0] != 0x80 {
		m.SDO, m.Error = decodeBlockSegment(d, client)
		t.attach(tr, m.SDO)
		if m.SDO.Last {
			tr.lastSeg = true
		}
		return m
	}
	s := m.SDO
	if s.Phase == PhaseAbort {
		*tr = transfer{}
		return m
	}
	begin := func(st sdoState) {
		*tr = transfer{state: st, index: s.Index, sub: s.SubIndex, expedited: s.Expedited}
	}
	if client {
		switch {
		case s.CS == CCSInitiateDownload:
			begin(stDownload)
			if s.Expedited {
				tr.value = append([]byte(nil), s.Data...)
			}
		case s.CS == CCSInitiateUpload:
			begin(stUpload)
		case s.CS == CCSDownloadSegment && tr.state == stDownload:
			t.attach(tr, s)
			tr.lastSeg = s.Last
		case s.CS == CCSUploadSegment && tr.state == stUpload:
			t.attach(tr, s)
		case s.CS == CCSBlockDownload && s.Sub == blockSubInitiate:
			begin(stBlockDownInit)
		case s.CS == CCSBlockDownload && tr.state == stBlockDownEnd:
			t.attach(tr, s)
		case s.CS == CCSBlockUpload && s.Sub == blockSubInitiate:
			begin(stBlockUpInit)
		case s.CS == CCSBlockUpload && s.Sub == blockSubUploadStart && tr.state == stBlockUpInit:
			t.attach(tr, s)
			tr.state, tr.lastSeg = stBlockUpData, false
		case s.CS == CCSBlockUpload && s.Sub == blockSubAck && tr.state == stBlockUpData:
			t.attach(tr, s)
			if tr.lastSeg {
				tr.state = stBlockUpEnd
			}
		case s.CS == CCSBlockUpload && s.Sub == blockSubEnd && tr.state == stBlockUpEnd:
			t.attach(tr, s)
			*tr = transfer{}
		}
		return m
	}
	switch {
	case s.CS == SCSInitiateDownload && tr.state == stDownload:
		t.attach(tr, s)
		if tr.expedited {
			if s.Index == tr.index && s.SubIndex == tr.sub {
				t.learn(m.Node, tr.index, tr.sub, tr.value)
			}
			*tr = transfer{}
		}
	case s.CS == SCSDownloadSegment && tr.state == stDownload:
		t.attach(tr, s)
		if tr.lastSeg {
			*tr = transfer{}
		}
	case s.CS == SCSInitiateUpload && tr.state == stUpload:
		t.attach(tr, s)
		if s.Expedited {
			if s.Index == tr.index && s.SubIndex == tr.sub {
				t.learn(m.Node, tr.index, tr.sub, s.Data)
			}
			*tr = transfer{}
		}
	case s.CS == SCSUploadSegment && tr.state == stUpload:
		t.attach(tr, s)
		if s.Last {
			*tr = transfer{}
		}
	case s.CS == SCSBlockDownload && s.Sub == blockSubInitiate && tr.state == stBlockDownInit:
		t.attach(tr, s)
		tr.state, tr.lastSeg = stBlockDownData, false
	case s.CS == SCSBlockDownload && s.Sub == blockSubAck && tr.state == stBlockDownData:
		t.attach(tr, s)
		if tr.lastSeg {
			tr.state = stBlockDownEnd
		}
	case s.CS == SCSBlockDownload && s.Sub == blockSubEnd && tr.state == stBlockDownEnd:
		t.attach(tr, s)
		*tr = transfer{}
	case s.CS == SCSBlockUpload && s.Sub == blockSubInitiate && tr.state == stBlockUpInit:
		t.attach(tr, s)
	case s.CS == SCSBlockUpload && s.Sub == blockSubEnd && tr.state == stBlockUpEnd:
		t.attach(tr, s)
	}
	return m
}

// attach fills in the object of the transfer in progress.
func (t *Tracker) attach(tr *transfer, s *SDO) {
	if !s.HasIndex && tr.state != stIdle {
		s.Index, s.SubIndex, s.HasIndex = tr.index, tr.sub, true
	}
}

// learn records the COB-ID of a PDO communication parameter that an SDO
// transfer showed. Only 11-bit identifiers are learned; a 29-bit one
// (bit 29), a COB-ID of fewer than 4 bytes or another object is ignored.
func (t *Tracker) learn(node canopen.NodeID, index uint16, sub uint8, v []byte) {
	if sub != 1 || len(v) != 4 || index < 0x1400 || index > 0x19FF || index >= 0x1600 && index < 0x1800 {
		return
	}
	ref := pdoRef{node: node, transmit: index >= 0x1800}
	ref.number = int(index-0x1400) + 1
	if ref.transmit {
		ref.number = int(index-0x1800) + 1
	}
	for id, r := range t.pdoIDs {
		if r == ref {
			delete(t.pdoIDs, id)
		}
	}
	cob := uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16 | uint32(v[3])<<24
	if cob&(1<<31) != 0 || cob&(1<<29) != 0 {
		return
	}
	id := canbus.ID(cob & 0x7FF)
	if old, ok := t.pdoIDs[id]; ok && old.transmit && !ref.transmit {
		return // the producer names the frame, not one of its consumers
	}
	t.pdoIDs[id] = ref
}
