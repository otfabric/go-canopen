// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// ErrNotStarted reports a transfer on a client that was not started.
var ErrNotStarted = errors.New("usdo: client not started")

// ErrSessions reports a client with 256 transfers in progress: every
// session-ID is in use.
var ErrSessions = errors.New("usdo: no free session-ID")

// ClientStats counts what a [Client] did.
type ClientStats struct {
	Uploads         uint64 `json:"uploads"`         // uploads completed (per server)
	Downloads       uint64 `json:"downloads"`       // downloads completed (per server)
	Bulk            uint64 `json:"bulk"`            // transfers that used bulk transfer
	BytesUploaded   uint64 `json:"bytesUploaded"`   // data bytes of completed uploads
	BytesDownloaded uint64 `json:"bytesDownloaded"` // data bytes of completed downloads
	Failed          uint64 `json:"failed"`          // transfers (per server) that ended with an error
	AbortsSent      uint64 `json:"abortsSent"`      // aborts sent (protocol errors, timeouts, cancellation)
	AbortsReceived  uint64 `json:"abortsReceived"`  // aborts the servers sent
	Timeouts        uint64 `json:"timeouts"`        // responses that did not come in time
	Ignored         uint64 `json:"ignored"`         // responses ignored: malformed, stale, unexpected
}

type clientStats struct {
	uploads, downloads, bulk, bytesUp, bytesDown, failed atomic.Uint64
	abortsSent, abortsReceived, timeouts, ignored        atomic.Uint64
}

// resp is a response received from a server.
type resp struct {
	from canopen.NodeID
	m    Message
}

// Client is a USDO client (CiA 1301). Unlike a classical SDO client it
// needs no channel per server: requests go on 600h + the client's own
// node-ID with the destination in the message, and responses come on
// 580h + the server's node-ID with the client's node-ID as destination.
// Transfers are identified by a session-ID, so several can run at once,
// from several goroutines, to the same or to different servers. A
// transfer to a broadcast or group address collects the responses of all
// servers.
type Client struct {
	bus  canopen.Bus
	node canopen.NodeID
	cfg  config
	lc   lifecycle
	st   clientStats

	mu    sync.Mutex
	convs map[uint8]chan resp
	next  uint8
}

// NewClient returns the USDO client of node (its node-ID: the CAN-ID of
// its requests is 600h + node). Call Start before transfers. Options:
// [WithLogger], [WithTimeout], [WithBRS], [WithTProcUnit].
func NewClient(bus canopen.Bus, node canopen.NodeID, opts ...Option) (*Client, error) {
	if bus == nil {
		return nil, errors.New("usdo: NewClient needs a bus")
	}
	if err := node.Check(); err != nil {
		return nil, err
	}
	cfg := newConfig(opts)
	if err := cfg.check(); err != nil {
		return nil, err
	}
	return &Client{bus: bus, node: node, cfg: cfg, convs: map[uint8]chan resp{}, next: 1}, nil
}

// NodeID returns the client's node-ID.
func (c *Client) NodeID() canopen.NodeID { return c.node }

// Start subscribes to the responses (580h..5FFh) and starts dispatching
// them to the transfers.
func (c *Client) Start() error {
	return c.lc.start(func(context.Context) (func(context.Context), error) {
		sub, err := c.bus.Subscribe(canbus.WithFilters(filter(canopen.FuncSDOTx)))
		if err != nil {
			return nil, fmt.Errorf("usdo: subscribe: %w", err)
		}
		return func(ctx context.Context) {
			defer func() { _ = sub.Close() }()
			for {
				f, err := sub.Receive(ctx)
				if err != nil {
					return
				}
				c.dispatch(&f)
			}
		}, nil
	})
}

// Close stops the client and waits for its goroutine; running transfers
// time out. It is idempotent.
func (c *Client) Close() error {
	c.lc.close()
	return nil
}

// Stats returns a snapshot of the counters.
func (c *Client) Stats() ClientStats {
	st := &c.st
	return ClientStats{
		Uploads: st.uploads.Load(), Downloads: st.downloads.Load(), Bulk: st.bulk.Load(),
		BytesUploaded: st.bytesUp.Load(), BytesDownloaded: st.bytesDown.Load(), Failed: st.failed.Load(),
		AbortsSent: st.abortsSent.Load(), AbortsReceived: st.abortsReceived.Load(), Timeouts: st.timeouts.Load(),
		Ignored: st.ignored.Load(),
	}
}

// dispatch hands a response to its transfer.
func (c *Client) dispatch(f *canbus.Frame) {
	from, ok := sender(f, canopen.FuncSDOTx)
	if !ok {
		c.st.ignored.Add(1)
		return
	}
	m, err := Decode(f.Payload())
	if len(f.Payload()) < 3 || m.Dest != Address(c.node) {
		return // not for this client
	}
	if err != nil || m.Cmd.Remote() {
		c.st.ignored.Add(1)
		return
	}
	c.mu.Lock()
	ch := c.convs[m.Session]
	c.mu.Unlock()
	if ch == nil {
		c.st.ignored.Add(1)
		return
	}
	select {
	case ch <- resp{from, m}:
	default:
		c.st.ignored.Add(1)
	}
}

// openSession allocates a session-ID.
func (c *Client) openSession() (uint8, chan resp, error) {
	if !c.lc.running() {
		return 0, nil, ErrNotStarted
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for range 256 {
		id := c.next
		c.next++
		if _, used := c.convs[id]; !used {
			ch := make(chan resp, 64)
			c.convs[id] = ch
			return id, ch, nil
		}
	}
	return 0, nil, ErrSessions
}

func (c *Client) closeSession(id uint8) {
	c.mu.Lock()
	delete(c.convs, id)
	c.mu.Unlock()
}

// link is how a transfer talks to the servers.
type link interface {
	send(ctx context.Context, m Message) error
	recv(ctx context.Context, d time.Duration) (resp, error)
}

// busLink is the link of a client session.
type busLink struct {
	c  *Client
	ch chan resp
}

func (l *busLink) send(ctx context.Context, m Message) error {
	f, err := Frame(m, l.c.node, false, l.c.cfg.brs)
	if err != nil {
		return err
	}
	sctx, cancel := sendCtx(ctx)
	defer cancel()
	return l.c.bus.Send(sctx, &f)
}

// errRecvTimeout is a response timeout of a link.
var errRecvTimeout = errors.New("usdo: response timeout")

func (l *busLink) recv(ctx context.Context, d time.Duration) (resp, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return resp{}, ctx.Err()
	case <-t.C:
		return resp{}, errRecvTimeout
	case r := <-l.ch:
		return r, nil
	}
}

// xfer is one transfer with one server.
type xfer struct {
	st      *clientStats
	l       link
	ctx     context.Context
	server  canopen.NodeID
	dest    Address
	sid     uint8
	index   uint16
	sub     uint8
	timeout time.Duration
	tunit   time.Duration
}

func (c *Client) newXfer(ctx context.Context, server canopen.NodeID, index uint16, sub uint8, cl call) (*xfer, func(), error) {
	sid, ch, err := c.openSession()
	if err != nil {
		return nil, nil, err
	}
	to := c.cfg.timeout
	if cl.timeout > 0 {
		to = cl.timeout
	}
	x := &xfer{st: &c.st, l: &busLink{c, ch}, ctx: ctx, server: server, dest: Address(server), sid: sid,
		index: index, sub: sub, timeout: to, tunit: c.cfg.tunit}
	return x, func() { c.closeSession(sid) }, nil
}

func callOf(opts []CallOption) call {
	var cl call
	for _, o := range opts {
		if o != nil {
			o(&cl)
		}
	}
	return cl
}

// msg returns a message of the transfer.
func (x *xfer) msg(cmd Command) Message {
	return Message{Dest: x.dest, Cmd: cmd, Session: x.sid, Index: x.index, SubIndex: x.sub}
}

// send sends a request.
func (x *xfer) send(m Message) error { return x.l.send(x.ctx, m) }

// abort sends an abort to the server and returns the local error.
func (x *xfer) abort(code AbortCode) error {
	m := x.msg(CmdAbort)
	m.Abort = code
	x.st.abortsSent.Add(1)
	_ = x.l.send(context.WithoutCancel(x.ctx), m)
	return &AbortError{Code: code, Node: x.server, Index: x.index, SubIndex: x.sub, timeout: code == AbortTimeout}
}

// fail ends the transfer after an error of recv or of a check: protocol
// errors and timeouts are aborted, the context's error is returned as is
// (after an abort), remote aborts are returned as they are.
func (x *xfer) fail(err error) error {
	var ae *AbortError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, errRecvTimeout):
		x.st.timeouts.Add(1)
		err = x.abort(AbortTimeout)
	case x.ctx.Err() != nil:
		_ = x.abort(AbortUnknown)
		err = x.ctx.Err()
	default:
		err = fmt.Errorf("usdo: node %d: %w", x.server, err)
	}
	x.st.failed.Add(1)
	return err
}

// expect waits for one of the response commands from the server; aborts
// become remote [AbortError]s; other messages are ignored as stale.
func (x *xfer) expect(cmds ...Command) (Message, error) {
	deadline := time.Now().Add(x.timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return Message{}, errRecvTimeout
		}
		r, err := x.l.recv(x.ctx, left)
		if err != nil {
			return Message{}, err
		}
		if r.from != x.server {
			x.st.ignored.Add(1)
			continue
		}
		if r.m.Cmd == CmdAbort {
			x.st.abortsReceived.Add(1)
			return Message{}, &AbortError{Code: r.m.Abort, Node: x.server, Index: x.index, SubIndex: x.sub, Remote: true}
		}
		if slices.Contains(cmds, r.m.Cmd) {
			return r.m, nil
		}
		x.st.ignored.Add(1)
	}
}

// checkObject checks that a response names the transfer's object.
func (x *xfer) checkObject(m Message) error {
	if m.Index != x.index || m.SubIndex != x.sub {
		return x.abort(AbortProtocol)
	}
	return nil
}

// Upload reads an object of a server: expedited when the server answers
// with the data, segmented otherwise.
func (c *Client) Upload(ctx context.Context, server canopen.NodeID, index uint16, sub uint8, opts ...CallOption) ([]byte, error) {
	if err := server.Check(); err != nil {
		return nil, err
	}
	x, done, err := c.newXfer(ctx, server, index, sub, callOf(opts))
	if err != nil {
		return nil, err
	}
	defer done()
	if err := x.send(x.msg(CmdUpload)); err != nil {
		return nil, x.fail(err)
	}
	m, err := x.expect(CmdUploadExpedited, CmdUploadInit)
	if err != nil {
		return nil, x.fail(err)
	}
	return x.uploadFrom(m)
}

// uploadFrom completes an upload from the server's first response.
func (x *xfer) uploadFrom(m Message) ([]byte, error) {
	if err := x.checkObject(m); err != nil {
		return nil, x.fail(err)
	}
	if m.Cmd == CmdUploadExpedited {
		x.st.uploads.Add(1)
		x.st.bytesUp.Add(uint64(len(m.Data)))
		return m.Data, nil
	}
	size := int(m.Size)
	buf := make([]byte, 0, min(size, 1<<16))
	for n := 1; ; n++ {
		req := Message{Dest: x.dest, Cmd: CmdUploadSegment, Session: x.sid, Counter: counter(n)}
		if err := x.send(req); err != nil {
			return nil, x.fail(err)
		}
		var seg Message
		for {
			var err error
			if seg, err = x.expect(CmdUploadSegmentData, CmdUploadLast); err != nil {
				return nil, x.fail(err)
			}
			if seg.Cmd == CmdUploadSegmentData && seg.Counter == counter(n-1) && n > 1 {
				x.st.ignored.Add(1) // a repeated earlier segment
				continue
			}
			break
		}
		if seg.Cmd == CmdUploadSegmentData && seg.Counter != counter(n) {
			return nil, x.fail(x.abort(AbortCounter))
		}
		if len(buf)+len(seg.Data) > size {
			return nil, x.fail(x.abort(AbortSize))
		}
		buf = append(buf, seg.Data...)
		if seg.Cmd == CmdUploadLast {
			if len(buf) != size {
				return nil, x.fail(x.abort(AbortSize))
			}
			x.st.uploads.Add(1)
			x.st.bytesUp.Add(uint64(size))
			return buf, nil
		}
	}
}

// Download writes an object of a server: expedited up to [MaxExpedited]
// bytes, segmented (each segment confirmed) or, with [UseBulk], bulk
// (confirmed at the end) above.
func (c *Client) Download(ctx context.Context, server canopen.NodeID, index uint16, sub uint8, data []byte, opts ...CallOption) error {
	if err := server.Check(); err != nil {
		return err
	}
	cl := callOf(opts)
	x, done, err := c.newXfer(ctx, server, index, sub, cl)
	if err != nil {
		return err
	}
	defer done()
	switch {
	case len(data) <= MaxExpedited:
		return x.downloadExpedited(data, cl)
	case cl.bulk:
		return x.downloadBulk(data, cl)
	}
	return x.downloadSegmented(data, cl)
}

func (x *xfer) downloadExpedited(data []byte, cl call) error {
	m := x.msg(CmdDownloadExpedited)
	m.DataType, m.Data = cl.dataType, data
	if err := x.send(m); err != nil {
		return x.fail(err)
	}
	r, err := x.expect(CmdDownloadExpeditedOK)
	if err != nil {
		return x.fail(err)
	}
	if err := x.checkObject(r); err != nil {
		return x.fail(err)
	}
	x.downloaded(len(data))
	return nil
}

func (x *xfer) downloaded(n int) {
	x.st.downloads.Add(1)
	x.st.bytesDown.Add(uint64(n))
}

// chunks splits download data into segments: all but the last are
// SegmentSize bytes.
func chunks(data []byte) [][]byte {
	var out [][]byte
	for len(data) > SegmentSize {
		out = append(out, data[:SegmentSize])
		data = data[SegmentSize:]
	}
	return append(out, data)
}

func (x *xfer) downloadSegmented(data []byte, cl call) error {
	m := x.msg(CmdDownloadInit)
	m.DataType, m.Size = cl.dataType, uint32(len(data))
	if err := x.send(m); err != nil {
		return x.fail(err)
	}
	r, err := x.expect(CmdDownloadInitOK)
	if err != nil {
		return x.fail(err)
	}
	if err := x.checkObject(r); err != nil {
		return x.fail(err)
	}
	segs := chunks(data)
	for i, seg := range segs {
		n := i + 1
		if n == len(segs) {
			break
		}
		if err := x.send(Message{Dest: x.dest, Cmd: CmdDownloadSegment, Session: x.sid, Counter: counter(n), Data: seg}); err != nil {
			return x.fail(err)
		}
		for {
			r, err := x.expect(CmdDownloadSegmentOK)
			if err != nil {
				return x.fail(err)
			}
			if r.Counter == counter(n) {
				break
			}
			if n > 1 && r.Counter == counter(n-1) {
				x.st.ignored.Add(1) // repeated confirmation
				continue
			}
			return x.fail(x.abort(AbortCounter))
		}
	}
	if err := x.send(Message{Dest: x.dest, Cmd: CmdDownloadLast, Session: x.sid, Data: segs[len(segs)-1]}); err != nil {
		return x.fail(err)
	}
	if _, err := x.expect(CmdDownloadLastOK); err != nil {
		return x.fail(err)
	}
	x.downloaded(len(data))
	return nil
}

func (x *xfer) downloadBulk(data []byte, cl call) error {
	x.st.bulk.Add(1)
	m := x.msg(CmdBulkInit)
	m.DataType, m.Size = cl.dataType, uint32(len(data))
	if err := x.send(m); err != nil {
		return x.fail(err)
	}
	r, err := x.expect(CmdBulkInitOK)
	if err != nil {
		return x.fail(err)
	}
	if err := x.checkObject(r); err != nil {
		return x.fail(err)
	}
	if err := sendBulk(x.ctx, x.l, x.dest, x.sid, data, time.Duration(r.TProc)*x.tunit); err != nil {
		return x.fail(err)
	}
	if _, err := x.expect(CmdBulkLastOK); err != nil {
		return x.fail(err)
	}
	x.downloaded(len(data))
	return nil
}

// sendBulk sends the segments of a bulk transfer, pace apart.
func sendBulk(ctx context.Context, l link, dest Address, sid uint8, data []byte, pace time.Duration) error {
	segs := chunks(data)
	for i, seg := range segs {
		if i > 0 && pace > 0 {
			t := time.NewTimer(pace)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		m := Message{Dest: dest, Cmd: CmdBulkSegment, Session: sid, Counter: counter(i + 1), Data: seg}
		if i == len(segs)-1 {
			m = Message{Dest: dest, Cmd: CmdBulkLast, Session: sid, Data: seg}
		}
		if err := l.send(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// Result is the outcome of a transfer with one server of a transfer to
// several.
type Result struct {
	Data []byte // uploaded data
	Err  error
}

// multi is a transfer to several servers.
type multi struct {
	c       *Client
	l       link
	ctx     context.Context
	dest    Address
	sid     uint8
	index   uint16
	sub     uint8
	timeout time.Duration
	expect  []canopen.NodeID
}

// collect receives responses until the timeout, or until every expected
// server answered (want reports which have). handle processes each
// response; it returns false for a response it ignores.
func (mu *multi) collect(answered func() []canopen.NodeID, handle func(r resp) bool) error {
	deadline := time.Now().Add(mu.timeout)
	for {
		if len(mu.expect) > 0 {
			got := answered()
			all := true
			for _, n := range mu.expect {
				if !slices.Contains(got, n) {
					all = false
					break
				}
			}
			if all {
				return nil
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		r, err := mu.l.recv(mu.ctx, left)
		if errors.Is(err, errRecvTimeout) {
			return nil
		}
		if err != nil {
			return err
		}
		if !handle(r) {
			mu.c.st.ignored.Add(1)
		}
	}
}

func (c *Client) newMulti(ctx context.Context, dest Address, index uint16, sub uint8, cl call) (*multi, func(), error) {
	sid, ch, err := c.openSession()
	if err != nil {
		return nil, nil, err
	}
	to := c.cfg.timeout
	if cl.timeout > 0 {
		to = cl.timeout
	}
	return &multi{c: c, l: &busLink{c, ch}, ctx: ctx, dest: dest, sid: sid, index: index, sub: sub, timeout: to, expect: cl.expect},
		func() { c.closeSession(sid) }, nil
}

// remoteAbort records an abort a server sent.
func (mu *multi) remoteAbort(from canopen.NodeID, m Message) error {
	mu.c.st.abortsReceived.Add(1)
	mu.c.st.failed.Add(1)
	return &AbortError{Code: m.Abort, Node: from, Index: mu.index, SubIndex: mu.sub, Remote: true}
}

// DownloadMulti writes an object of every server dest addresses
// (broadcast, a group, or one node) and returns the outcome per server
// that answered: nil, or the error. Up to [MaxExpedited] bytes are sent
// expedited; more by bulk transfer (CiA's example of a broadcast bulk
// download): the servers that answer the initiate request take part, the
// segments are paced by the largest t_proc they report, and each confirms
// or aborts at the end. Without [Expect] the call collects responses for
// the whole response timeout. The error is for the transfer as a whole (a
// send failure, the context).
func (c *Client) DownloadMulti(ctx context.Context, dest Address, index uint16, sub uint8, data []byte, opts ...CallOption) (map[canopen.NodeID]error, error) {
	cl := callOf(opts)
	mu, done, err := c.newMulti(ctx, dest, index, sub, cl)
	if err != nil {
		return nil, err
	}
	defer done()
	out := map[canopen.NodeID]error{}
	answered := func() []canopen.NodeID {
		ns := make([]canopen.NodeID, 0, len(out))
		for n := range out {
			ns = append(ns, n)
		}
		return ns
	}
	if len(data) <= MaxExpedited {
		m := Message{Dest: dest, Cmd: CmdDownloadExpedited, Session: mu.sid, Index: index, SubIndex: sub, DataType: cl.dataType, Data: data}
		if err := mu.l.send(ctx, m); err != nil {
			return nil, err
		}
		err := mu.collect(answered, func(r resp) bool {
			if _, dup := out[r.from]; dup {
				return false
			}
			switch r.m.Cmd {
			case CmdDownloadExpeditedOK:
				out[r.from] = nil
				c.st.downloads.Add(1)
				c.st.bytesDown.Add(uint64(len(data)))
			case CmdAbort:
				out[r.from] = mu.remoteAbort(r.from, r.m)
			default:
				return false
			}
			return true
		})
		return out, err
	}
	c.st.bulk.Add(1)
	m := Message{Dest: dest, Cmd: CmdBulkInit, Session: mu.sid, Index: index, SubIndex: sub, DataType: cl.dataType, Size: uint32(len(data))}
	if err := mu.l.send(ctx, m); err != nil {
		return nil, err
	}
	var pace time.Duration
	part := map[canopen.NodeID]bool{}
	err = mu.collect(answered, func(r resp) bool {
		if _, dup := out[r.from]; dup || part[r.from] {
			return false
		}
		switch r.m.Cmd {
		case CmdBulkInitOK:
			part[r.from] = true
			pace = max(pace, time.Duration(r.m.TProc)*c.cfg.tunit)
			// answered for the collection; replaced by the final result
			out[r.from] = nil
		case CmdAbort:
			out[r.from] = mu.remoteAbort(r.from, r.m)
		default:
			return false
		}
		return true
	})
	if err != nil {
		return out, err
	}
	if len(part) == 0 {
		return out, nil
	}
	final := map[canopen.NodeID]bool{}
	if err := sendBulk(ctx, mu.l, dest, mu.sid, data, pace); err != nil {
		return out, err
	}
	finals := func() []canopen.NodeID {
		ns := make([]canopen.NodeID, 0, len(final))
		for n := range final {
			ns = append(ns, n)
		}
		return ns
	}
	mu.expect = make([]canopen.NodeID, 0, len(part))
	for n := range part {
		mu.expect = append(mu.expect, n)
	}
	err = mu.collect(finals, func(r resp) bool {
		if !part[r.from] || final[r.from] {
			return false
		}
		switch r.m.Cmd {
		case CmdBulkLastOK:
			out[r.from] = nil
			c.st.downloads.Add(1)
			c.st.bytesDown.Add(uint64(len(data)))
		case CmdAbort:
			out[r.from] = mu.remoteAbort(r.from, r.m)
		default:
			return false
		}
		final[r.from] = true
		return true
	})
	for n := range part {
		if !final[n] {
			c.st.timeouts.Add(1)
			c.st.failed.Add(1)
			out[n] = &AbortError{Code: AbortTimeout, Node: n, Index: index, SubIndex: sub, timeout: true}
		}
	}
	return out, err
}

// UploadMulti reads an object of every server dest addresses and returns
// the outcome per server that answered. Expedited answers complete at
// once; segmented ones are continued with each server in turn, on the
// same session-ID. Without [Expect] the call collects initial responses
// for the whole response timeout.
func (c *Client) UploadMulti(ctx context.Context, dest Address, index uint16, sub uint8, opts ...CallOption) (map[canopen.NodeID]Result, error) {
	cl := callOf(opts)
	mu, done, err := c.newMulti(ctx, dest, index, sub, cl)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := mu.l.send(ctx, Message{Dest: dest, Cmd: CmdUpload, Session: mu.sid, Index: index, SubIndex: sub}); err != nil {
		return nil, err
	}
	out := map[canopen.NodeID]Result{}
	segmented := map[canopen.NodeID]Message{}
	answered := func() []canopen.NodeID {
		ns := make([]canopen.NodeID, 0, len(out))
		for n := range out {
			ns = append(ns, n)
		}
		return ns
	}
	err = mu.collect(answered, func(r resp) bool {
		if _, dup := out[r.from]; dup {
			return false
		}
		switch r.m.Cmd {
		case CmdUploadExpedited, CmdUploadInit:
			if r.m.Index != index || r.m.SubIndex != sub {
				return false
			}
			if r.m.Cmd == CmdUploadExpedited {
				out[r.from] = Result{Data: r.m.Data}
				c.st.uploads.Add(1)
				c.st.bytesUp.Add(uint64(len(r.m.Data)))
			} else {
				out[r.from] = Result{}
				segmented[r.from] = r.m
			}
		case CmdAbort:
			out[r.from] = Result{Err: mu.remoteAbort(r.from, r.m)}
		default:
			return false
		}
		return true
	})
	if err != nil {
		return out, err
	}
	nodes := make([]canopen.NodeID, 0, len(segmented))
	for n := range segmented {
		nodes = append(nodes, n)
	}
	slices.Sort(nodes)
	for _, n := range nodes {
		x := &xfer{st: &c.st, l: mu.l, ctx: ctx, server: n, dest: Address(n), sid: mu.sid, index: index, sub: sub,
			timeout: mu.timeout, tunit: c.cfg.tunit}
		data, err := x.uploadFrom(segmented[n])
		out[n] = Result{Data: data, Err: err}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}
