// SPDX-License-Identifier: MIT

package sdo

import (
	"encoding/binary"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// serverState is the state of the server's transfer.
type serverState uint8

const (
	stIdle             serverState = iota
	stDownloadSegment              // segmented download: waiting for a segment
	stUploadSegment                // segmented upload: waiting for a segment request
	stBlockDownload                // block download: receiving a sub-block
	stBlockDownloadEnd             // block download: last segment acknowledged, waiting for the end
	stBlockUploadInit              // block upload: initiated, waiting for the start
	stBlockUploadAck               // block upload: block sent, waiting for its acknowledgement
	stBlockUploadEnd               // block upload: end sent, waiting for its confirmation
	stateCount
)

// machine is the server's protocol state machine, free of time and I/O:
// the caller feeds it received messages (handle), and calls timeout when
// the transfer was idle for the timeout and reack when a block receiver
// should repeat its acknowledgement. Messages to send go to out.
type machine struct {
	d       *od.Dictionary
	out     func(frame)
	maxSize int
	blkSize uint8 // block size asked for in block downloads
	crcOK   bool  // the server supports the CRC
	st      *serverStats

	state  serverState
	index  uint16
	sub    uint8
	toggle byte
	buf    []byte // received or to-send data
	size   int    // indicated size of a download
	sized  bool
	pos    int  // segmented upload position
	crc    bool // CRC in use for this block transfer

	// Block transfer.
	seq        uint8 // download: last in-sequence segment; upload: segments sent in the block
	blk        uint8 // upload: current block size
	blockStart int   // upload: offset of the block's first segment
	final      bool  // upload: the block holds the last segment
	lastLen    int   // upload: data bytes in the last segment
	last       frame // last message sent, repeated by reack
}

// active reports whether a transfer is in progress.
func (m *machine) active() bool { return m.state != stIdle }

// receiving reports whether the server receives a block, so that it
// repeats its acknowledgement when the client goes quiet.
func (m *machine) receiving() bool {
	return m.state == stBlockDownload || m.state == stBlockDownloadEnd
}

func (m *machine) reset() {
	m.state = stIdle
	m.buf = nil
	m.toggle, m.seq, m.pos, m.blockStart, m.size, m.sized = 0, 0, 0, 0, 0, false
}

func (m *machine) send(f frame) {
	m.last = f
	m.out(f)
}

// abort sends an abort for the current transfer (or the given object)
// and ends it.
func (m *machine) abort(index uint16, sub uint8, code canopen.AbortCode) {
	m.st.abortsSent.Add(1)
	m.out(abortFrame(index, sub, code))
	m.reset()
}

func (m *machine) abortCurrent(code canopen.AbortCode) { m.abort(m.index, m.sub, code) }

// timeout aborts a transfer that saw no progress for the timeout.
func (m *machine) timeout() {
	if m.active() {
		m.st.timeouts.Add(1)
		m.abortCurrent(canopen.AbortTimeout)
	}
}

// reack repeats the acknowledgement of a block receiver: the segments
// received in sequence so far in this block (the sender resumes after
// them), or, when none arrived, the last message sent (the previous
// acknowledgement or the initiate response, which may have been lost).
func (m *machine) reack() {
	if !m.receiving() {
		return
	}
	m.st.reacks.Add(1)
	if m.state == stBlockDownload && m.seq > 0 {
		m.send(ackFrame(m.seq, m.blkSize))
		m.seq = 0
		return
	}
	m.out(m.last)
}

// isInitiate reports whether byte 0 starts a new transfer.
func isInitiate(b0 byte) bool {
	switch b0 >> 5 {
	case ccsInitiateDownload, ccsInitiateUpload:
		return true
	case ccsBlockUpload:
		return b0&3 == blkInitiate
	case ccsBlockDownload:
		return b0&1 == blkInitiate
	}
	return false
}

// handle processes a received message. It reports whether the transfer
// progressed (the inactivity timeout restarts).
func (m *machine) handle(f frame) bool {
	m.st.requests.Add(1)
	switch m.state {
	case stBlockDownload:
		return m.blockSegment(f)
	case stBlockDownloadEnd:
		return m.blockDownloadEnd(f)
	default: // no special case
	}
	if f.cs() == csAbort {
		if m.active() {
			m.st.abortsReceived.Add(1)
			m.reset()
		} else {
			m.st.ignored.Add(1)
		}
		return false
	}
	if isInitiate(f[0]) {
		if m.active() {
			// A new initiate in the middle of a transfer: the client
			// gave up on the old one (typically a retry after its
			// timeout). The old transfer ends silently.
			m.st.restarts.Add(1)
			m.reset()
		}
		return m.initiate(f)
	}
	switch m.state {
	case stDownloadSegment:
		if f.cs() == ccsDownloadSegment {
			return m.downloadSegment(f)
		}
	case stUploadSegment:
		if f.cs() == ccsUploadSegment {
			return m.uploadSegment(f)
		}
	case stBlockUploadInit:
		if f[0]&blkMask == ccsBlockUpload<<5|blkStart {
			m.sendBlock()
			m.state = stBlockUploadAck
			return true
		}
	case stBlockUploadAck:
		switch f[0] & blkMask {
		case ccsBlockUpload<<5 | blkAck:
			return m.blockUploadAck(f)
		case ccsBlockUpload<<5 | blkStart: // repeated start
			m.st.ignored.Add(1)
			return false
		}
	case stBlockUploadEnd:
		switch f[0] & blkMask {
		case ccsBlockUpload<<5 | blkEnd:
			m.st.uploads.Add(1)
			m.st.bytesUp.Add(uint64(len(m.buf)))
			m.reset()
			return true
		case ccsBlockUpload<<5 | blkAck:
			// The client repeats its last acknowledgement: the end
			// message was lost.
			if f[1] == m.seq {
				m.out(m.last)
			}
			m.st.ignored.Add(1)
			return false
		case ccsBlockUpload<<5 | blkStart:
			m.st.ignored.Add(1)
			return false
		}
	default: // no special case
	}
	if m.active() {
		m.abortCurrent(canopen.AbortCommand)
	} else {
		m.abort(0, 0, canopen.AbortCommand)
	}
	return false
}

// initiate starts a transfer.
func (m *machine) initiate(f frame) bool {
	m.index, m.sub = f.mux()
	b0 := f[0]
	switch b0 >> 5 {
	case ccsInitiateDownload:
		v := m.writable()
		if v == nil {
			return false
		}
		if b0&2 != 0 { // expedited
			n := 4
			if b0&1 != 0 {
				n = 4 - int(b0>>2&3)
			} else if s := v.DataType.Size(); s >= 1 && s <= 4 {
				n = s // size not indicated: the object's size
			}
			return m.store(f[4 : 4+n])
		}
		if !m.initDownloadSize(b0&1 != 0, f.u32()) {
			return false
		}
		m.state = stDownloadSegment
		m.send(muxFrame(scsInitiateDownload<<5, m.index, m.sub, 0))
		return true
	case ccsInitiateUpload:
		data, ok := m.read()
		if !ok {
			return false
		}
		m.startUpload(data)
		return true
	case ccsBlockUpload:
		blk, pst := f[4], f[5]
		if blk < 1 || blk > MaxBlockSize {
			m.abortCurrent(canopen.AbortBlockSize)
			return false
		}
		data, ok := m.read()
		if !ok {
			return false
		}
		if pst > 0 && len(data) <= int(pst) {
			m.st.switches.Add(1)
			m.startUpload(data)
			return true
		}
		m.buf, m.blk, m.blockStart = data, blk, 0
		m.crc = m.crcOK && b0&4 != 0
		m.state = stBlockUploadInit
		m.send(muxFrame(scsBlockUpload<<5|m.crcBit()|2, m.index, m.sub, uint32(len(data))))
		return true
	default: // ccsBlockDownload
		if m.writable() == nil || !m.initDownloadSize(b0&2 != 0, f.u32()) {
			return false
		}
		m.crc = m.crcOK && b0&4 != 0
		m.seq = 0
		m.state = stBlockDownload
		m.send(muxFrame(scsBlockDownload<<5|m.crcBit(), m.index, m.sub, uint32(m.blkSize)))
		return true
	}
}

func (m *machine) crcBit() byte {
	if m.crcOK {
		return 4
	}
	return 0
}

// writable checks that the object exists and may be written, and aborts
// otherwise. The dictionary's Write checks again at the end.
func (m *machine) writable() *od.Variable {
	v, err := m.d.Variable(m.index, m.sub)
	if err != nil {
		m.abortCurrent(abortCodeOf(err))
		return nil
	}
	if !v.Access.Writable() {
		m.abortCurrent(canopen.AbortReadOnly)
		return nil
	}
	return v
}

func (m *machine) initDownloadSize(sized bool, size uint32) bool {
	m.sized, m.size = sized, int(size)
	if sized && uint64(size) > uint64(m.maxSize) {
		m.abortCurrent(canopen.AbortOutOfMemory)
		return false
	}
	if sized {
		m.buf = make([]byte, 0, min(m.size+segData, 64<<10))
	}
	return true
}

func (m *machine) read() ([]byte, bool) {
	data, err := m.d.Read(m.index, m.sub)
	if err != nil {
		m.abortCurrent(abortCodeOf(err))
		return nil, false
	}
	return data, true
}

// store writes downloaded data and confirms (expedited and segmented
// download, whose confirmation is the initiate response).
func (m *machine) store(data []byte) bool {
	if err := m.d.Write(m.index, m.sub, data); err != nil {
		m.abortCurrent(abortCodeOf(err))
		return false
	}
	m.st.downloads.Add(1)
	m.st.bytesDown.Add(uint64(len(data)))
	m.send(muxFrame(scsInitiateDownload<<5, m.index, m.sub, 0))
	m.reset()
	return true
}

// startUpload answers an upload: expedited for 1..4 bytes, else
// segmented with the size indicated.
func (m *machine) startUpload(data []byte) {
	if n := len(data); n >= 1 && n <= 4 {
		f := muxFrame(scsInitiateUpload<<5|byte(4-n)<<2|3, m.index, m.sub, 0)
		copy(f[4:], data)
		m.st.uploads.Add(1)
		m.st.bytesUp.Add(uint64(n))
		m.send(f)
		m.reset()
		return
	}
	m.buf, m.pos, m.toggle = data, 0, 0
	m.state = stUploadSegment
	m.send(muxFrame(scsInitiateUpload<<5|1, m.index, m.sub, uint32(len(data))))
}

func (m *machine) uploadSegment(f frame) bool {
	t := f[0] >> 4 & 1
	if t != m.toggle {
		m.abortCurrent(canopen.AbortToggle)
		return false
	}
	end := min(m.pos+segData, len(m.buf))
	chunk := m.buf[m.pos:end]
	var r frame
	r[0] = scsUploadSegment<<5 | t<<4 | byte(segData-len(chunk))<<1
	copy(r[1:], chunk)
	m.pos = end
	m.toggle ^= 1
	if end >= len(m.buf) {
		r[0] |= 1
		m.st.uploads.Add(1)
		m.st.bytesUp.Add(uint64(len(m.buf)))
		m.send(r)
		m.reset()
		return true
	}
	m.send(r)
	return true
}

// limit returns the most data the download may carry and the abort code
// for more.
func (m *machine) limit() (int, canopen.AbortCode) {
	if m.sized && m.size < m.maxSize {
		return m.size, canopen.AbortLengthHigh
	}
	return m.maxSize, canopen.AbortOutOfMemory
}

// checkSize compares a finished download with the indicated size.
func (m *machine) checkSize(n int) bool {
	switch {
	case !m.sized || n == m.size:
		return true
	case n > m.size:
		m.abortCurrent(canopen.AbortLengthHigh)
	default:
		m.abortCurrent(canopen.AbortLengthLow)
	}
	return false
}

func (m *machine) downloadSegment(f frame) bool {
	t := f[0] >> 4 & 1
	if t != m.toggle {
		m.abortCurrent(canopen.AbortToggle)
		return false
	}
	data := f[1 : 8-int(f[0]>>1&7)]
	if lim, code := m.limit(); len(m.buf)+len(data) > lim {
		m.abortCurrent(code)
		return false
	}
	m.buf = append(m.buf, data...)
	m.toggle ^= 1
	if f[0]&1 == 0 {
		m.send(frame{scsDownloadSegment<<5 | t<<4})
		return true
	}
	if !m.checkSize(len(m.buf)) {
		return false
	}
	if err := m.d.Write(m.index, m.sub, m.buf); err != nil {
		m.abortCurrent(abortCodeOf(err))
		return false
	}
	m.st.downloads.Add(1)
	m.st.bytesDown.Add(uint64(len(m.buf)))
	m.send(frame{scsDownloadSegment<<5 | t<<4})
	m.reset()
	return true
}

// blockSegment receives a segment of a block download. Segments out of
// sequence (lost, repeated or reordered ones) are ignored: the
// acknowledgement tells the client where to resume.
func (m *machine) blockSegment(f frame) bool {
	if f.isAbort() {
		m.st.abortsReceived.Add(1)
		m.reset()
		return false
	}
	if len(m.buf) == 0 && m.seq == 0 && f[0]&blkMask == ccsBlockDownload<<5|blkEnd {
		// Empty data: the client ends the transfer without a segment
		// (python canopen does). No segment can look like this: the
		// first one has sequence number 1.
		return m.blockDownloadEnd(f)
	}
	seq, last := f[0]&0x7F, f[0]&0x80 != 0
	if seq != m.seq+1 {
		m.st.ignored.Add(1)
		return false
	}
	need := len(m.buf) + segData
	if last {
		need = len(m.buf) // the last segment holds 0..7 bytes: checked at the end
	}
	if lim, code := m.limit(); need > lim {
		m.abortCurrent(code)
		return false
	}
	m.buf = append(m.buf, f[1:8]...)
	m.seq = seq
	switch {
	case last:
		m.send(ackFrame(seq, m.blkSize))
		m.state = stBlockDownloadEnd
	case seq == m.blkSize:
		m.send(ackFrame(seq, m.blkSize))
		m.seq = 0
	}
	return true
}

// blockDownloadEnd waits for the end of a block download. Anything other
// than the end or an abort is ignored: late or repeated segments of the
// last block look like arbitrary commands.
func (m *machine) blockDownloadEnd(f frame) bool {
	if f.isAbort() {
		m.st.abortsReceived.Add(1)
		m.reset()
		return false
	}
	if f[0]&blkMask != ccsBlockDownload<<5|blkEnd {
		m.st.ignored.Add(1)
		return false
	}
	// n bytes of the last segment were padding; without any segment
	// (empty data) there is nothing to drop.
	n := min(int(f[0]>>2&7), len(m.buf))
	data := m.buf[:len(m.buf)-n]
	if !m.checkSize(len(data)) {
		return false
	}
	if m.crc && CRC16(data) != binary.LittleEndian.Uint16(f[1:3]) {
		m.abortCurrent(canopen.AbortCRC)
		return false
	}
	if err := m.d.Write(m.index, m.sub, data); err != nil {
		m.abortCurrent(abortCodeOf(err))
		return false
	}
	m.st.downloads.Add(1)
	m.st.blocks.Add(1)
	m.st.bytesDown.Add(uint64(len(data)))
	m.send(frame{scsBlockDownload<<5 | blkEnd})
	m.reset()
	return true
}

// sendBlock sends the next block of a block upload.
func (m *machine) sendBlock() {
	m.seq, m.final = 0, false
	pos := m.blockStart
	for m.seq < m.blk {
		end := min(pos+segData, len(m.buf))
		m.seq++
		var f frame
		f[0] = m.seq
		copy(f[1:], m.buf[pos:end])
		last := end >= len(m.buf)
		if last {
			f[0] |= 0x80
			m.final, m.lastLen = true, end-pos
		}
		m.send(f)
		pos = end
		if last {
			return
		}
	}
}

func (m *machine) blockUploadAck(f frame) bool {
	ack, blk := f[1], f[2]
	if ack > m.seq {
		m.abortCurrent(canopen.AbortSequence)
		return false
	}
	if blk < 1 || blk > MaxBlockSize {
		m.abortCurrent(canopen.AbortBlockSize)
		return false
	}
	if m.final && ack == m.seq {
		var crc uint16
		if m.crc {
			crc = CRC16(m.buf)
		}
		e := frame{scsBlockUpload<<5 | byte(segData-m.lastLen)<<2 | blkEnd, byte(crc), byte(crc >> 8)}
		m.state = stBlockUploadEnd
		m.st.blocks.Add(1)
		m.send(e)
		return true
	}
	m.blockStart += int(ack) * segData
	m.blk = blk
	m.sendBlock()
	return true
}
