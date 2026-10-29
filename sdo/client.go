// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// ClientStats counts what a [Client] did.
type ClientStats struct {
	Uploads         uint64 `json:"uploads"`         // uploads completed
	Downloads       uint64 `json:"downloads"`       // downloads completed
	BlockTransfers  uint64 `json:"blockTransfers"`  // transfers that used block transfer (also counted above)
	BytesUploaded   uint64 `json:"bytesUploaded"`   // data bytes of completed uploads
	BytesDownloaded uint64 `json:"bytesDownloaded"` // data bytes of completed downloads
	Failed          uint64 `json:"failed"`          // transfers that ended with an error (after retries)
	AbortsSent      uint64 `json:"abortsSent"`      // aborts sent (protocol errors, timeouts, cancellation)
	AbortsReceived  uint64 `json:"abortsReceived"`  // aborts the server sent
	Timeouts        uint64 `json:"timeouts"`        // attempts that timed out
	Retries         uint64 `json:"retries"`         // attempts repeated after a timeout
	Reacks          uint64 `json:"reacks"`          // block acknowledgements repeated
	Ignored         uint64 `json:"ignored"`         // messages ignored (stale, out of sequence)
}

type clientStats struct {
	uploads, downloads, blocks, bytesUp, bytesDown, failed atomic.Uint64
	abortsSent, abortsReceived, timeouts, retries, reacks  atomic.Uint64
	ignored                                                atomic.Uint64
}

// Client is an SDO client for one SDO channel (one server). Transfers on
// a client are serialized; clients of different channels work
// concurrently. A Client is safe for concurrent use.
//
// Errors: an abort from the server is a *[canopen.AbortError] with
// Remote set. When the server does not answer in time the client sends
// the abort [canopen.AbortTimeout] and returns an error matching both
// [canopen.ErrTimeout] (errors.Is) and *[canopen.AbortError] (errors.As,
// Remote false). A protocol violation by the server makes the client
// abort with the matching code and return an error matching
// [canopen.ErrProtocol] and *[canopen.AbortError] (Remote false). When the
// context ends during a transfer, the client aborts it with
// [canopen.AbortGeneral] and returns the context's error.
//
// Received SDO frames shorter than 8 bytes are zero-padded (some devices
// send short frames); the client always sends 8-byte frames.
type Client struct {
	bus      canopen.Bus
	cfg      config
	log      *slog.Logger
	c2s, s2c canopen.COBID
	sem      chan struct{}
	st       clientStats

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once

	// link receives the server's messages; created by the first
	// transfer, dropped after a failed one (which may leave stale
	// messages behind). Guarded by sem.
	link *busLink
}

// NewClient returns a client for the SDO server of node (COB-IDs 0x600 +
// node to the server, 0x580 + node from it, unless [WithCOBIDs] sets
// them; node is then not used). Options: [WithLogger], [WithTimeout],
// [WithCOBIDs], [WithBlockMode], [WithBlockThreshold], [WithBlockSize],
// [WithCRC], [WithProtocolSwitch], [WithMaxSize], [WithRetries].
func NewClient(bus canopen.Bus, node canopen.NodeID, opts ...ClientOption) (*Client, error) {
	if bus == nil {
		return nil, errors.New("sdo: NewClient needs a bus")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o.applyClient(&cfg)
	}
	if err := cfg.check(); err != nil {
		return nil, err
	}
	c := &Client{bus: bus, cfg: cfg, log: cfg.log, sem: make(chan struct{}, 1)}
	if cfg.cobSet {
		c.c2s, c.s2c = cfg.c2s, cfg.s2c
	} else {
		if err := node.Check(); err != nil {
			return nil, fmt.Errorf("sdo: %w", err)
		}
		c.c2s = canopen.NewCOBID(canopen.FuncSDORx + canbus.ID(node))
		c.s2c = canopen.NewCOBID(canopen.FuncSDOTx + canbus.ID(node))
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c, nil
}

// COBIDs returns the client's COB-IDs: client to server (sent) and server
// to client (received).
func (c *Client) COBIDs() (clientToServer, serverToClient canopen.COBID) { return c.c2s, c.s2c }

// Close aborts a transfer in progress, waits for it to end, releases the
// client's subscription and makes later transfers fail with
// [canopen.ErrClosed]. It is safe to call more
// than once.
func (c *Client) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.sem <- struct{}{} // wait for the running transfer; never released
		if c.link != nil {
			_ = c.link.sub.Close()
			c.link = nil
		}
	})
	return nil
}

// Stats returns a snapshot of the counters.
func (c *Client) Stats() ClientStats {
	st := &c.st
	return ClientStats{
		Uploads: st.uploads.Load(), Downloads: st.downloads.Load(), BlockTransfers: st.blocks.Load(),
		BytesUploaded: st.bytesUp.Load(), BytesDownloaded: st.bytesDown.Load(), Failed: st.failed.Load(),
		AbortsSent: st.abortsSent.Load(), AbortsReceived: st.abortsReceived.Load(),
		Timeouts: st.timeouts.Load(), Retries: st.retries.Load(), Reacks: st.reacks.Load(),
		Ignored: st.ignored.Load(),
	}
}

// Upload reads the value of a sub-object from the server.
func (c *Client) Upload(ctx context.Context, index uint16, sub uint8, opts ...CallOption) ([]byte, error) {
	cc := callConfig{mode: c.cfg.mode}
	for _, o := range opts {
		o(&cc)
	}
	var data []byte
	err := c.run(ctx, index, sub, func(x *xfer) error {
		var err error
		switch cc.mode {
		case BlockAlways, BlockAuto:
			data, err = x.blockUpload(c.pst(cc.mode))
		default:
			data, err = x.upload()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	c.st.uploads.Add(1)
	c.st.bytesUp.Add(uint64(len(data)))
	return data, nil
}

// pst returns the protocol switch threshold offered in block uploads.
func (c *Client) pst(m BlockMode) uint8 {
	if c.cfg.pst >= 0 {
		return uint8(c.cfg.pst)
	}
	if m == BlockAuto {
		return uint8(min(max(c.cfg.threshold-1, 0), 255))
	}
	return 0
}

// Download writes data to a sub-object of the server.
func (c *Client) Download(ctx context.Context, index uint16, sub uint8, data []byte, opts ...CallOption) error {
	cc := callConfig{mode: c.cfg.mode}
	for _, o := range opts {
		o(&cc)
	}
	block := cc.mode == BlockAlways || cc.mode == BlockAuto && len(data) >= c.cfg.threshold
	if uint64(len(data)) > 0xFFFFFFFF {
		return fmt.Errorf("sdo: %d bytes do not fit an SDO transfer", len(data))
	}
	err := c.run(ctx, index, sub, func(x *xfer) error {
		if block {
			return x.blockDownload(data)
		}
		return x.download(data)
	})
	if err != nil {
		return err
	}
	c.st.downloads.Add(1)
	c.st.bytesDown.Add(uint64(len(data)))
	return nil
}

// run serializes a transfer and repeats it after timeouts.
func (c *Client) run(ctx context.Context, index uint16, sub uint8, fn func(*xfer) error) error {
	if c.ctx.Err() != nil {
		return canopen.ErrClosed
	}
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return canopen.ErrClosed
	}
	defer func() { <-c.sem }()
	if c.ctx.Err() != nil {
		return canopen.ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	for attempt := 0; ; attempt++ {
		err := c.attempt(ctx, index, sub, fn)
		if err == nil {
			return nil
		}
		if c.ctx.Err() != nil && ctx.Err() != nil {
			err = fmt.Errorf("%w (%w)", canopen.ErrClosed, err)
		}
		if !retryable(err) || attempt >= c.cfg.retries || ctx.Err() != nil {
			c.st.failed.Add(1)
			return err
		}
		c.st.retries.Add(1)
		c.log.Debug("sdo: retrying after timeout", "index", index, "sub", sub, "attempt", attempt+1)
	}
}

// retryable reports whether a transfer failed for a timeout: the client's
// own, or the server's (an abort with [canopen.AbortTimeout]: the server
// times out first when a request was lost).
func retryable(err error) bool {
	if errors.Is(err, canopen.ErrTimeout) {
		return true
	}
	var ae *canopen.AbortError
	return errors.As(err, &ae) && ae.Remote && ae.Code == canopen.AbortTimeout
}

func (c *Client) attempt(ctx context.Context, index uint16, sub uint8, fn func(*xfer) error) error {
	if c.link == nil {
		s, err := c.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{c.s2c.Filter()}}))
		if err != nil {
			return fmt.Errorf("sdo: subscribe: %w", err)
		}
		c.link = &busLink{bus: c.bus, sub: s, cob: c.c2s}
	} else {
		c.link.drain() // stale messages of earlier transfers
	}
	x := &xfer{c: c, l: c.link, ctx: ctx, index: index, sub: sub}
	err := x.finish(fn(x))
	if err != nil {
		_ = c.link.sub.Close()
		c.link = nil
	}
	return err
}

// link carries the messages of one transfer.
type link interface {
	send(ctx context.Context, f frame) error
	// recv waits up to d for a message; errNoResponse when none came.
	recv(ctx context.Context, d time.Duration) (frame, error)
	// drain discards the messages already received.
	drain()
}

var errNoResponse = errors.New("sdo: no response")

type busLink struct {
	bus canopen.Bus
	sub *canbus.Subscription
	cob canopen.COBID
}

func (l *busLink) send(ctx context.Context, f frame) error {
	cf := canFrame(l.cob, f)
	return l.bus.Send(ctx, &cf)
}

func (l *busLink) recv(ctx context.Context, d time.Duration) (frame, error) {
	if d <= 0 {
		return frame{}, errNoResponse
	}
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		cf, err := l.sub.Receive(wctx)
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return frame{}, errNoResponse
			}
			return frame{}, err
		}
		if f, ok := payload(&cf); ok {
			return f, nil
		}
	}
}

func (l *busLink) drain() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for {
		if _, err := l.sub.Receive(ctx); err != nil {
			return
		}
	}
}

// xfer is one transfer attempt.
type xfer struct {
	c        *Client
	l        link
	ctx      context.Context
	index    uint16
	sub      uint8
	deadline time.Time
	started  bool // a request was sent: aborting it is meaningful
}

// localAbort is a protocol violation by the server: the client aborts
// with code.
type localAbort struct{ code canopen.AbortCode }

func (e *localAbort) Error() string { return e.code.Error() }

func protoErr(code canopen.AbortCode) error { return &localAbort{code} }

// finish converts the outcome of a transfer to the error returned, and
// sends the abort the outcome calls for.
func (x *xfer) finish(err error) error {
	if err == nil {
		return nil
	}
	ae := &canopen.AbortError{Index: x.index, SubIndex: x.sub}
	var la *localAbort
	switch {
	case errors.As(err, &ae) && ae.Remote:
		x.c.st.abortsReceived.Add(1)
		x.c.log.Debug("sdo: server aborted", "index", x.index, "sub", x.sub, "code", ae.Code)
		return ae
	case errors.Is(err, errNoResponse):
		x.c.st.timeouts.Add(1)
		x.abort(canopen.AbortTimeout)
		ae.Code = canopen.AbortTimeout
		return fmt.Errorf("sdo: no response within %v: %w: %w", x.c.cfg.timeout, canopen.ErrTimeout, ae)
	case errors.As(err, &la):
		x.abort(la.code)
		ae.Code = la.code
		x.c.log.Debug("sdo: client aborted", "index", x.index, "sub", x.sub, "code", la.code)
		return fmt.Errorf("sdo: %w: %w", canopen.ErrProtocol, ae)
	case x.ctx.Err() != nil:
		x.abort(canopen.AbortGeneral)
		return err
	}
	return err
}

// abort sends an abort for the transfer, with a fresh context: the
// transfer's may have ended.
func (x *xfer) abort(code canopen.AbortCode) {
	if !x.started {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), x.c.cfg.timeout)
	defer cancel()
	if err := x.l.send(ctx, abortFrame(x.index, x.sub, code)); err == nil {
		x.c.st.abortsSent.Add(1)
	}
}

// send sends a request and restarts the response deadline.
func (x *xfer) send(f frame) error {
	x.started = true
	x.deadline = time.Now().Add(x.c.cfg.timeout)
	if err := x.l.send(x.ctx, f); err != nil {
		return fmt.Errorf("sdo: send: %w", err)
	}
	return nil
}

// recv waits for a response until the deadline. An abort ends the
// transfer.
func (x *xfer) recv() (frame, error) {
	f, err := x.l.recv(x.ctx, time.Until(x.deadline))
	if err != nil {
		return f, err
	}
	if f.cs() == csAbort {
		return f, x.remoteAbort(f)
	}
	return f, nil
}

func (x *xfer) remoteAbort(f frame) error {
	return &canopen.AbortError{Code: canopen.AbortCode(f.u32()), Index: x.index, SubIndex: x.sub, Remote: true}
}

// recvInitiate waits for the response to an initiate request: one of the
// specifiers, for this transfer's object. Responses for another object
// are stale and ignored.
func (x *xfer) recvInitiate(scs ...byte) (frame, error) {
	for {
		f, err := x.recv()
		if err != nil {
			return f, err
		}
		if idx, sub := f.mux(); idx != x.index || sub != x.sub {
			x.c.st.ignored.Add(1)
			continue
		}
		for _, s := range scs {
			if f.cs() == s {
				return f, nil
			}
		}
		return f, protoErr(canopen.AbortCommand)
	}
}

// upload runs an expedited or segmented upload.
func (x *xfer) upload() ([]byte, error) {
	if err := x.send(muxFrame(ccsInitiateUpload<<5, x.index, x.sub, 0)); err != nil {
		return nil, err
	}
	f, err := x.recvInitiate(scsInitiateUpload)
	if err != nil {
		return nil, err
	}
	return x.uploadFrom(f)
}

// uploadFrom continues an upload from the initiate response f.
func (x *xfer) uploadFrom(f frame) ([]byte, error) {
	b0 := f[0]
	if b0&2 != 0 { // expedited
		n := 4
		if b0&1 != 0 {
			n = 4 - int(b0>>2&3)
		}
		return append([]byte(nil), f[4:4+n]...), nil
	}
	sized, size := b0&1 != 0, int64(f.u32())
	if sized && size > int64(x.c.cfg.maxSize) {
		return nil, protoErr(canopen.AbortOutOfMemory)
	}
	limit, code := int64(x.c.cfg.maxSize), canopen.AbortOutOfMemory
	if sized {
		limit, code = size, canopen.AbortLengthHigh
	}
	var data []byte
	if sized {
		data = make([]byte, 0, min(size, 64<<10))
	}
	for t := byte(0); ; t ^= 1 {
		if err := x.send(frame{ccsUploadSegment<<5 | t<<4}); err != nil {
			return nil, err
		}
		f, err := x.recv()
		if err != nil {
			return nil, err
		}
		if f.cs() != scsUploadSegment {
			return nil, protoErr(canopen.AbortCommand)
		}
		if f[0]>>4&1 != t {
			return nil, protoErr(canopen.AbortToggle)
		}
		seg := f[1 : 8-int(f[0]>>1&7)]
		if int64(len(data)+len(seg)) > limit {
			return nil, protoErr(code)
		}
		data = append(data, seg...)
		if f[0]&1 != 0 {
			break
		}
	}
	if sized && int64(len(data)) < size {
		return nil, protoErr(canopen.AbortLengthLow)
	}
	return data, nil
}

// download runs an expedited (1..4 bytes) or segmented download.
func (x *xfer) download(data []byte) error {
	if n := len(data); n >= 1 && n <= 4 {
		f := muxFrame(ccsInitiateDownload<<5|byte(4-n)<<2|3, x.index, x.sub, 0)
		copy(f[4:], data)
		if err := x.send(f); err != nil {
			return err
		}
		_, err := x.recvInitiate(scsInitiateDownload)
		return err
	}
	if err := x.send(muxFrame(ccsInitiateDownload<<5|1, x.index, x.sub, uint32(len(data)))); err != nil {
		return err
	}
	if _, err := x.recvInitiate(scsInitiateDownload); err != nil {
		return err
	}
	pos := 0
	for t := byte(0); ; t ^= 1 {
		end := min(pos+segData, len(data))
		var f frame
		f[0] = ccsDownloadSegment<<5 | t<<4 | byte(segData-(end-pos))<<1
		copy(f[1:], data[pos:end])
		last := end >= len(data)
		if last {
			f[0] |= 1
		}
		if err := x.send(f); err != nil {
			return err
		}
		r, err := x.recv()
		if err != nil {
			return err
		}
		if r.cs() != scsDownloadSegment {
			return protoErr(canopen.AbortCommand)
		}
		if r[0]>>4&1 != t {
			return protoErr(canopen.AbortToggle)
		}
		if last {
			return nil
		}
		pos = end
	}
}

// blockDownload runs a block download.
func (x *xfer) blockDownload(data []byte) error {
	b0 := byte(ccsBlockDownload<<5 | 2) // size indicated
	if x.c.cfg.crc {
		b0 |= 4
	}
	if err := x.send(muxFrame(b0, x.index, x.sub, uint32(len(data)))); err != nil {
		return err
	}
	f, err := x.recvInitiate(scsBlockDownload)
	if err != nil {
		return err
	}
	if f[0]&3 != blkInitiate {
		return protoErr(canopen.AbortCommand)
	}
	blk := f[4]
	if blk < 1 || blk > MaxBlockSize {
		return protoErr(canopen.AbortBlockSize)
	}
	useCRC := x.c.cfg.crc && f[0]&4 != 0
	x.c.st.blocks.Add(1)
	pos, lastLen := 0, 0
	for {
		x.l.drain() // stale acknowledgements of earlier blocks
		seq, final, p := byte(0), false, pos
		for seq < blk {
			end := min(p+segData, len(data))
			seq++
			var s frame
			s[0] = seq
			copy(s[1:], data[p:end])
			if end >= len(data) {
				s[0] |= 0x80
				final, lastLen = true, end-p
			}
			if err := x.send(s); err != nil {
				return err
			}
			p = end
			if final {
				break
			}
		}
		ack, err := x.recvBlockAck()
		if err != nil {
			return err
		}
		if ack[1] > seq {
			return protoErr(canopen.AbortSequence)
		}
		if ack[2] < 1 || ack[2] > MaxBlockSize {
			return protoErr(canopen.AbortBlockSize)
		}
		if final && ack[1] == seq {
			break
		}
		pos += int(ack[1]) * segData
		blk = ack[2]
	}
	var crc uint16
	if useCRC {
		crc = CRC16(data)
	}
	if err := x.send(frame{ccsBlockDownload<<5 | byte(segData-lastLen)<<2 | blkEnd, byte(crc), byte(crc >> 8)}); err != nil {
		return err
	}
	for {
		f, err := x.recv()
		if err != nil {
			return err
		}
		switch f[0] & blkMask {
		case scsBlockDownload<<5 | blkEnd:
			return nil
		case scsBlockDownload<<5 | blkAck: // repeated acknowledgement
			x.c.st.ignored.Add(1)
			continue
		}
		return protoErr(canopen.AbortCommand)
	}
}

// recvBlockAck waits for the acknowledgement of a block download.
func (x *xfer) recvBlockAck() (frame, error) {
	for {
		f, err := x.recv()
		if err != nil {
			return f, err
		}
		if f.cs() != scsBlockDownload {
			return f, protoErr(canopen.AbortCommand)
		}
		if f[0]&3 == blkAck {
			return f, nil
		}
		x.c.st.ignored.Add(1) // a repeated initiate response
	}
}

// blockUpload runs a block upload, offering the protocol switch
// threshold pst.
func (x *xfer) blockUpload(pst uint8) ([]byte, error) {
	b0 := byte(ccsBlockUpload<<5 | blkInitiate)
	if x.c.cfg.crc {
		b0 |= 4
	}
	blk := uint8(x.c.cfg.blkSize)
	req := muxFrame(b0, x.index, x.sub, 0)
	req[4], req[5] = blk, pst
	if err := x.send(req); err != nil {
		return nil, err
	}
	f, err := x.recvInitiate(scsBlockUpload, scsInitiateUpload)
	if err != nil {
		return nil, err
	}
	if f.cs() == scsInitiateUpload { // protocol switch
		return x.uploadFrom(f)
	}
	if f[0]&1 != blkInitiate {
		return nil, protoErr(canopen.AbortCommand)
	}
	useCRC := x.c.cfg.crc && f[0]&4 != 0
	sized, size := f[0]&2 != 0, int64(f.u32())
	if sized && size > int64(x.c.cfg.maxSize) {
		return nil, protoErr(canopen.AbortOutOfMemory)
	}
	limit, code := int64(x.c.cfg.maxSize), canopen.AbortOutOfMemory
	if sized {
		limit, code = size, canopen.AbortLengthHigh
	}
	x.c.st.blocks.Add(1)
	var buf []byte
	if sized {
		buf = make([]byte, 0, min(size+segData, 64<<10))
	}
	if err := x.send(frame{ccsBlockUpload<<5 | blkStart}); err != nil {
		return nil, err
	}
	r := receiver{x: x, blk: blk, last: frame{ccsBlockUpload<<5 | blkStart}}
	for {
		f, err := r.next()
		if err != nil {
			return nil, err
		}
		if f.isAbort() {
			return nil, x.remoteAbort(f)
		}
		if len(buf) == 0 && r.seq == 0 && f[0]&blkMask == scsBlockUpload<<5|blkEnd {
			// An empty object: the server ends the transfer without a
			// segment (Lely CANopen does). No segment can look like
			// this: the first one has sequence number 1.
			return x.blockUploadEnd(f, buf, sized, size, useCRC)
		}
		s, last := f[0]&0x7F, f[0]&0x80 != 0
		if s != r.seq+1 {
			x.c.st.ignored.Add(1)
			continue
		}
		need := int64(len(buf) + segData)
		if last {
			need = int64(len(buf))
		}
		if need > limit {
			return nil, protoErr(code)
		}
		buf = append(buf, f[1:]...)
		r.seq = s
		r.progress()
		if last || s == blk {
			if err := r.ack(); err != nil {
				return nil, err
			}
			if last {
				break
			}
		}
	}
	// Wait for the end; repeated or late segments are ignored.
	for {
		f, err := r.next()
		if err != nil {
			return nil, err
		}
		if f.isAbort() {
			return nil, x.remoteAbort(f)
		}
		if f[0]&blkMask == scsBlockUpload<<5|blkEnd {
			return x.blockUploadEnd(f, buf, sized, size, useCRC)
		}
		x.c.st.ignored.Add(1)
	}
}

// blockUploadEnd checks the end message of a block upload against the
// data received and confirms it.
func (x *xfer) blockUploadEnd(f frame, buf []byte, sized bool, size int64, useCRC bool) ([]byte, error) {
	// n bytes of the last segment were padding; without any segment
	// (an empty object) there is nothing to drop.
	n := min(int(f[0]>>2&7), len(buf))
	data := buf[:len(buf)-n]
	switch {
	case sized && int64(len(data)) > size:
		return nil, protoErr(canopen.AbortLengthHigh)
	case sized && int64(len(data)) < size:
		return nil, protoErr(canopen.AbortLengthLow)
	case useCRC && CRC16(data) != binary.LittleEndian.Uint16(f[1:3]):
		return nil, protoErr(canopen.AbortCRC)
	}
	if err := x.send(frame{ccsBlockUpload<<5 | blkEnd}); err != nil {
		return nil, err
	}
	return data, nil
}

// receiver paces the client's side of a block upload: it waits for
// segments, repeats its acknowledgement when the server goes quiet for a
// quarter of the timeout, and gives up after the timeout without progress
// (or four repetitions).
type receiver struct {
	x     *xfer
	blk   uint8
	last  frame // last message sent
	seq   uint8 // segments received in sequence in this block
	quiet int   // repetitions since the last progress
}

func (r *receiver) progress() {
	r.quiet = 0
	r.x.deadline = time.Now().Add(r.x.c.cfg.timeout)
}

// ack acknowledges the segments received in this block; the next block
// starts.
func (r *receiver) ack() error {
	r.last = ackFrame(r.seq, r.blk)
	r.seq = 0
	return r.x.send(r.last)
}

// next returns the next message from the server.
func (r *receiver) next() (frame, error) {
	x := r.x
	for {
		wait := min(x.c.cfg.timeout/4, time.Until(x.deadline))
		f, err := x.l.recv(x.ctx, wait)
		if !errors.Is(err, errNoResponse) {
			return f, err
		}
		r.quiet++
		if r.quiet > 4 || !time.Now().Before(x.deadline) {
			return f, errNoResponse
		}
		x.c.st.reacks.Add(1)
		if r.seq > 0 {
			r.last = ackFrame(r.seq, r.blk)
			r.seq = 0
		}
		if err := x.l.send(x.ctx, r.last); err != nil {
			return f, fmt.Errorf("sdo: send: %w", err)
		}
	}
}
