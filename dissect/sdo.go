// SPDX-License-Identifier: MIT

package dissect

import (
	"fmt"
	"strings"

	"github.com/otfabric/go-canopen"
)

// SDO command specifiers (CiA 301 7.2.4.3), the top three bits of the
// first byte.
const (
	// Client (request) command specifiers.
	CCSDownloadSegment   uint8 = 0
	CCSInitiateDownload  uint8 = 1
	CCSInitiateUpload    uint8 = 2
	CCSUploadSegment     uint8 = 3
	CSAbort              uint8 = 4 // client and server
	CCSBlockUpload       uint8 = 5
	CCSBlockDownload     uint8 = 6
	SCSUploadSegment     uint8 = 0
	SCSDownloadSegment   uint8 = 1
	SCSInitiateUpload    uint8 = 2
	SCSInitiateDownload  uint8 = 3
	SCSBlockDownload     uint8 = 5
	SCSBlockUpload       uint8 = 6
	blockSubInitiate     uint8 = 0
	blockSubEnd          uint8 = 1
	blockSubAck          uint8 = 2
	blockSubUploadStart  uint8 = 3
	blockSegmentLastFlag uint8 = 0x80
)

// Phase is the part of an SDO transfer a message belongs to.
type Phase string

// SDO phases.
const (
	PhaseInitiate Phase = "initiate"
	PhaseSegment  Phase = "segment"   // segmented transfer
	PhaseBlock    Phase = "block"     // block transfer: initiate, start, acknowledge
	PhaseSubBlock Phase = "sub-block" // block transfer: data segment (Tracker only)
	PhaseEnd      Phase = "end"       // block transfer end
	PhaseAbort    Phase = "abort"
	PhaseUnknown  Phase = "unknown"
)

// SDO is an SDO request (client to server) or response.
type SDO struct {
	// Client is true for a request (client to server, 0x600 + node).
	Client bool `json:"client"`
	// CS is the command specifier (ccs or scs, the top three bits of byte
	// 0); for block transfers Sub is the sub-command (cs/ss).
	CS  uint8 `json:"cs"`
	Sub uint8 `json:"subCommand,omitempty"`
	// Command names the message, e.g. "initiate upload", "download
	// segment", "block download ack".
	Command string `json:"command"`
	Phase   Phase  `json:"phase"`
	// Index and SubIndex address the object (HasIndex: the message
	// carries them, or a Tracker attributed them).
	Index    uint16 `json:"index"`
	SubIndex uint8  `json:"subIndex"`
	HasIndex bool   `json:"hasIndex"`
	// Expedited and SizeIndicated are the e and s bits of an initiate.
	Expedited     bool `json:"expedited,omitempty"`
	SizeIndicated bool `json:"sizeIndicated,omitempty"`
	// Size is the indicated size of the whole transfer, or the number of
	// data bytes in an expedited transfer or a segment.
	Size uint32 `json:"size,omitempty"`
	// Data are the data bytes carried (expedited, segment, sub-block).
	Data Bytes `json:"data,omitempty"`
	// Toggle is the toggle bit of a segment (0 or 1); Last is its c bit
	// ("no more segments") and that of a block segment.
	Toggle uint8 `json:"toggle,omitempty"`
	Last   bool  `json:"last,omitempty"`
	// Abort is the abort code of an abort, AbortText its description.
	Abort     canopen.AbortCode `json:"abortCode,omitempty"`
	AbortText string            `json:"abortText,omitempty"`
	// Block transfer fields: CRC support (cc/sc), block size (blksize),
	// protocol switch threshold (pst), acknowledged sequence number
	// (ackseq), segment sequence number (seqno), CRC.
	CRCSupport bool   `json:"crcSupport,omitempty"`
	BlockSize  uint8  `json:"blockSize,omitempty"`
	Threshold  uint8  `json:"threshold,omitempty"`
	AckSeq     uint8  `json:"ackSeq,omitempty"`
	Seq        uint8  `json:"seq,omitempty"`
	CRC        uint16 `json:"crc,omitempty"`
}

// sdoFrame reads an SDO frame's bytes; reading past the end marks it
// short.
type sdoFrame struct {
	d     []byte
	short bool
}

func (f *sdoFrame) b(i int) uint8 {
	if i >= len(f.d) {
		f.short = true
		return 0
	}
	return f.d[i]
}

func (f *sdoFrame) u16(i int) uint16 { return uint16(f.b(i)) | uint16(f.b(i+1))<<8 }

func (f *sdoFrame) u32(i int) uint32 { return uint32(f.u16(i)) | uint32(f.u16(i+2))<<16 }

func (f *sdoFrame) from(i, n int) Bytes {
	if i+n > len(f.d) {
		f.short = true
		n = max(len(f.d)-i, 0)
	}
	return Bytes(append([]byte{}, f.d[i:i+n]...))
}

func (f *sdoFrame) index(s *SDO) {
	s.Index, s.SubIndex, s.HasIndex = f.u16(1), f.b(3), true
}

// decodeSDO decodes an SDO frame statelessly; problem describes a
// malformed frame.
func decodeSDO(d []byte, client bool) (s *SDO, problem string) {
	f := &sdoFrame{d: d}
	s = &SDO{Client: client, Phase: PhaseUnknown}
	if len(d) == 0 {
		return s, "empty SDO frame"
	}
	b0 := d[0]
	s.CS = b0 >> 5
	unknown := false
	switch {
	case s.CS == CSAbort:
		s.Command, s.Phase = "abort", PhaseAbort
		f.index(s)
		s.Abort = canopen.AbortCode(f.u32(4))
		s.AbortText = s.Abort.Text()
	case client && s.CS == CCSInitiateDownload, !client && s.CS == SCSInitiateUpload:
		s.Command, s.Phase = "initiate download", PhaseInitiate
		if !client {
			s.Command = "initiate upload"
		}
		f.index(s)
		s.Expedited, s.SizeIndicated = b0&0x02 != 0, b0&0x01 != 0
		switch {
		case s.Expedited:
			n := 4
			if s.SizeIndicated {
				n = 4 - int(b0>>2&0x03)
			}
			s.Data = f.from(4, n)
			s.Size = uint32(n)
		case s.SizeIndicated:
			s.Size = f.u32(4)
		}
	case client && s.CS == CCSInitiateUpload:
		s.Command, s.Phase = "initiate upload", PhaseInitiate
		f.index(s)
	case !client && s.CS == SCSInitiateDownload:
		s.Command, s.Phase = "initiate download", PhaseInitiate
		f.index(s)
	case client && s.CS == CCSDownloadSegment, !client && s.CS == SCSUploadSegment:
		s.Command, s.Phase = "download segment", PhaseSegment
		if !client {
			s.Command = "upload segment"
		}
		s.Toggle, s.Last = b0>>4&1, b0&1 != 0
		n := 7 - int(b0>>1&0x07)
		s.Data = f.from(1, n)
		s.Size = uint32(n)
	case client && s.CS == CCSUploadSegment, !client && s.CS == SCSDownloadSegment:
		s.Command, s.Phase = "upload segment", PhaseSegment
		if !client {
			s.Command = "download segment"
		}
		s.Toggle = b0 >> 4 & 1
	case client && s.CS == CCSBlockUpload:
		s.Sub = b0 & 0x03
		switch s.Sub {
		case blockSubInitiate:
			s.Command, s.Phase = "block upload initiate", PhaseBlock
			f.index(s)
			s.CRCSupport = b0&0x04 != 0
			s.BlockSize, s.Threshold = f.b(4), f.b(5)
		case blockSubEnd:
			s.Command, s.Phase = "block upload end", PhaseEnd
		case blockSubAck:
			s.Command, s.Phase = "block upload ack", PhaseBlock
			s.AckSeq, s.BlockSize = f.b(1), f.b(2)
		case blockSubUploadStart:
			s.Command, s.Phase = "block upload start", PhaseBlock
		}
	case !client && s.CS == SCSBlockDownload:
		s.Sub = b0 & 0x03
		switch s.Sub {
		case blockSubInitiate:
			s.Command, s.Phase = "block download initiate", PhaseBlock
			f.index(s)
			s.CRCSupport = b0&0x04 != 0
			s.BlockSize = f.b(4)
		case blockSubEnd:
			s.Command, s.Phase = "block download end", PhaseEnd
		case blockSubAck:
			s.Command, s.Phase = "block download ack", PhaseBlock
			s.AckSeq, s.BlockSize = f.b(1), f.b(2)
		default:
			unknown = true
		}
	case client && s.CS == CCSBlockDownload, !client && s.CS == SCSBlockUpload:
		s.Sub = b0 & 0x01
		what := "block download"
		if !client {
			what = "block upload"
		}
		if s.Sub == blockSubInitiate {
			s.Command, s.Phase = what+" initiate", PhaseBlock
			f.index(s)
			s.CRCSupport = b0&0x04 != 0
			s.SizeIndicated = b0&0x02 != 0
			if s.SizeIndicated {
				s.Size = f.u32(4)
			}
		} else {
			s.Command, s.Phase = what+" end", PhaseEnd
			s.Size = uint32(7 - int(b0>>2&0x07)) // bytes in the last segment
			s.CRC = f.u16(1)
		}
	default:
		unknown = true
	}
	if unknown {
		s.Command, s.Phase = fmt.Sprintf("unknown command 0x%02X", b0), PhaseUnknown
		return s, fmt.Sprintf("unknown SDO command specifier %d", s.CS)
	}
	if f.short || len(d) != 8 {
		return s, fmt.Sprintf("SDO needs 8 bytes, got %d", len(d))
	}
	return s, ""
}

// decodeBlockSegment decodes a data segment of a block transfer.
func decodeBlockSegment(d []byte, client bool) (*SDO, string) {
	s := &SDO{Client: client, Command: "block segment", Phase: PhaseSubBlock}
	if len(d) == 0 {
		return s, "empty SDO frame"
	}
	s.Seq, s.Last = d[0]&0x7F, d[0]&blockSegmentLastFlag != 0
	s.Data = Bytes(append([]byte{}, d[1:]...))
	s.Size = uint32(len(s.Data))
	if len(d) != 8 {
		return s, fmt.Sprintf("SDO needs 8 bytes, got %d", len(d))
	}
	return s, ""
}

// String summarises the SDO message.
func (s *SDO) String() string {
	var b strings.Builder
	b.WriteString(s.Command)
	if s.HasIndex {
		fmt.Fprintf(&b, " 0x%04X:%02X", s.Index, s.SubIndex)
	}
	switch s.Phase {
	case PhaseAbort:
		fmt.Fprintf(&b, " 0x%08X", uint32(s.Abort))
		if s.AbortText != "" {
			fmt.Fprintf(&b, " (%s)", s.AbortText)
		}
		return b.String()
	case PhaseSegment:
		fmt.Fprintf(&b, " toggle=%d", s.Toggle)
	case PhaseSubBlock:
		fmt.Fprintf(&b, " seq=%d", s.Seq)
	}
	if s.Last {
		b.WriteString(" last")
	}
	if s.Expedited {
		b.WriteString(" expedited")
	} else if s.SizeIndicated {
		fmt.Fprintf(&b, " size=%d", s.Size)
	}
	if s.BlockSize != 0 {
		fmt.Fprintf(&b, " blksize=%d", s.BlockSize)
	}
	if s.AckSeq != 0 {
		fmt.Fprintf(&b, " ackseq=%d", s.AckSeq)
	}
	if s.Phase == PhaseEnd && s.CS == CCSBlockDownload { // and SCSBlockUpload: the ends with a CRC
		fmt.Fprintf(&b, " crc=0x%04X", s.CRC)
	}
	if len(s.Data) > 0 {
		fmt.Fprintf(&b, ": %s", s.Data)
	}
	return b.String()
}
