// SPDX-License-Identifier: MIT

package sdo

import (
	"encoding/binary"
	"errors"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// Protocol constants (CiA 301 7.2.4.3).
const (
	// MaxBlockSize is the largest number of segments in a block (blksize).
	MaxBlockSize = 127
	// DefaultMaxSize is the default bound on the data of one transfer.
	DefaultMaxSize = 1 << 20

	segData = 7 // data bytes in a segment

	// Client command specifiers (bits 7..5 of byte 0).
	ccsDownloadSegment  = 0
	ccsInitiateDownload = 1
	ccsInitiateUpload   = 2
	ccsUploadSegment    = 3
	csAbort             = 4 // abort, either direction
	ccsBlockUpload      = 5
	ccsBlockDownload    = 6

	// Server command specifiers.
	scsUploadSegment    = 0
	scsDownloadSegment  = 1
	scsInitiateUpload   = 2
	scsInitiateDownload = 3
	scsBlockDownload    = 5
	scsBlockUpload      = 6

	// Sub-commands of the block services (bits 1..0 of byte 0; block
	// download client and block upload server use bit 0 only).
	blkInitiate = 0
	blkEnd      = 1
	blkAck      = 2
	blkStart    = 3

	// Byte 0 masks of block commands: specifier, sub-command.
	blkMask = 0xE3

	abortByte = 0x80 // byte 0 of an abort transfer request
)

// frame is the 8-byte payload of an SDO message.
type frame [8]byte

// cs returns the command specifier.
func (f *frame) cs() byte { return f[0] >> 5 }

// mux returns the multiplexer (index, sub-index) of an initiate or abort
// message.
func (f *frame) mux() (uint16, uint8) {
	return binary.LittleEndian.Uint16(f[1:3]), f[3]
}

// u32 returns bytes 4..7 (size, expedited data, abort code).
func (f *frame) u32() uint32 { return binary.LittleEndian.Uint32(f[4:8]) }

// muxFrame returns a message with byte 0, the multiplexer and bytes 4..7.
func muxFrame(b0 byte, index uint16, sub uint8, v uint32) frame {
	var f frame
	f[0] = b0
	binary.LittleEndian.PutUint16(f[1:3], index)
	f[3] = sub
	binary.LittleEndian.PutUint32(f[4:8], v)
	return f
}

// abortFrame returns an abort transfer request.
func abortFrame(index uint16, sub uint8, code canopen.AbortCode) frame {
	return muxFrame(abortByte, index, sub, uint32(code))
}

// isAbort reports whether f is an abort transfer request. In the states
// where block segments may arrive, only byte 0 = 0x80 is one: a segment
// with sequence number 1..127 and the last flag reads as 0x81..0xFF.
func (f *frame) isAbort() bool { return f[0] == abortByte }

// ackFrame returns a block acknowledgement (client or server: both are
// specifier 5, sub-command 2).
func ackFrame(seq, blksize uint8) frame {
	return frame{ccsBlockUpload<<5 | blkAck, seq, blksize}
}

// payload returns the first 8 bytes of a received frame, zero-padded when
// shorter. ok is false for frames that cannot carry SDO (remote, error).
func payload(f *canbus.Frame) (out frame, ok bool) {
	if f.IsRemote() || f.IsError() {
		return out, false
	}
	copy(out[:], f.Payload())
	return out, true
}

// canFrame builds the 8-byte CAN frame of an SDO message on cob.
func canFrame(cob canopen.COBID, f frame) canbus.Frame {
	var cf canbus.Frame
	cf.ID = cob.ID()
	if cob.Extended() {
		cf.Flags = canbus.FlagExtended
	}
	cf.Len = 8
	copy(cf.Data[:], f[:])
	return cf
}

// abortCodeOf maps an error from the object dictionary to the abort code
// sent for it: an [canopen.AbortCode] unchanged, anything else
// [canopen.AbortGeneral].
func abortCodeOf(err error) canopen.AbortCode {
	var c canopen.AbortCode
	if errors.As(err, &c) {
		return c
	}
	return canopen.AbortGeneral
}

var crcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC16 returns the CRC of block transfers (CiA 301 7.2.4.3.16):
// CRC-16-CCITT, polynomial 0x1021, initial value 0, no reflection, no
// final XOR (also known as CRC-16/XMODEM).
func CRC16(data []byte) uint16 { return crcUpdate(0, data) }

func crcUpdate(crc uint16, data []byte) uint16 {
	for _, b := range data {
		crc = crc<<8 ^ crcTable[byte(crc>>8)^b]
	}
	return crc
}
