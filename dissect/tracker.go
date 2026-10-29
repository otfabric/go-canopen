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
	lastSeg   bool // the data sender sent its last segment
}

// Tracker decodes frames like [Decode] and follows the SDO transfers on
// the default SDO channel of every node, so that it can attribute SDO
// segments to the object being transferred (SDO.HasIndex) and decode the
// data segments of block transfers (PhaseSubBlock), which [Decode] reads
// as commands. It must see every frame of a transfer, in bus order; a
// missed frame confuses it until the next initiate or abort. A Tracker is
// safe for concurrent use.
type Tracker struct {
	mu    sync.Mutex
	nodes map[canopen.NodeID]*transfer
}

// NewTracker returns a tracker with no transfers in progress.
func NewTracker() *Tracker { return &Tracker{nodes: map[canopen.NodeID]*transfer{}} }

// Reset forgets every transfer in progress.
func (t *Tracker) Reset() {
	t.mu.Lock()
	t.nodes = map[canopen.NodeID]*transfer{}
	t.mu.Unlock()
}

// Decode decodes a frame, using and updating the SDO transfer state.
func (t *Tracker) Decode(f *canbus.Frame) Message {
	m := Decode(f)
	if m.SDO == nil {
		return m
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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
