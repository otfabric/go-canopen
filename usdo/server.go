// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// ServerStats counts what a [Server] did.
type ServerStats struct {
	Requests        uint64 `json:"requests"`        // messages addressed to the server while enabled
	Uploads         uint64 `json:"uploads"`         // uploads completed
	Downloads       uint64 `json:"downloads"`       // downloads completed
	Segmented       uint64 `json:"segmented"`       // segmented transfers completed (also counted above)
	Bulk            uint64 `json:"bulk"`            // bulk transfers completed (also counted in Downloads)
	BytesUploaded   uint64 `json:"bytesUploaded"`   // data bytes of completed uploads
	BytesDownloaded uint64 `json:"bytesDownloaded"` // data bytes of completed downloads
	AbortsSent      uint64 `json:"abortsSent"`      // aborts sent (refusals, protocol errors, timeouts)
	AbortsReceived  uint64 `json:"abortsReceived"`  // transfers the client aborted
	Timeouts        uint64 `json:"timeouts"`        // transfers aborted for inactivity
	Restarts        uint64 `json:"restarts"`        // sessions replaced by a new request with the same session-ID
	Busy            uint64 `json:"busy"`            // transfers refused: too many sessions
	Duplicates      uint64 `json:"duplicates"`      // repeated segments or segment requests answered again or ignored
	Remote          uint64 `json:"remote"`          // remote (routed) requests ignored: not supported
	Ignored         uint64 `json:"ignored"`         // messages ignored: malformed, from no valid node, while disabled
	SendErrors      uint64 `json:"sendErrors"`      // responses the bus failed to send
}

type serverStats struct {
	requests, uploads, downloads, segmented, bulk, bytesUp, bytesDown        atomic.Uint64
	abortsSent, abortsReceived, timeouts, restarts, busy, dups, remote, ignr atomic.Uint64
	sendErrors                                                               atomic.Uint64
}

// sessKey identifies a transfer: the client and its session-ID.
type sessKey struct {
	client canopen.NodeID
	id     uint8
}

// session kinds.
const (
	kindDownload = iota + 1 // segmented download
	kindBulk                // bulk download
	kindUpload              // segmented upload
)

type session struct {
	kind     int
	index    uint16
	sub      uint8
	size     int
	buf      []byte // download data, or upload data
	seq      int    // segments accepted (download) or sent (upload)
	deadline time.Time
}

// Server is a USDO server (CiA 1301): it serves an object dictionary to
// every USDO client of the network, several transfers at once, each
// identified by the client's node-ID and the session-ID. It receives on
// 601h..67Fh (client to server, CAN-ID 600h + client node-ID) the
// messages addressed to its node-ID, to broadcast (00h) and to its groups
// ([WithGroups]), and answers on 580h + its node-ID with the client's
// node-ID as destination.
//
// Remote accesses go through [od.Dictionary.Read] and
// [od.Dictionary.Write], so access rights, limits and hooks apply; a
// refusal reaches the client as the USDO abort code of the dictionary's
// SDO abort code ([FromSDO]). Downloads of more than [MaxExpedited] bytes
// are buffered and written once, when complete.
type Server struct {
	bus canopen.Bus
	d   *od.Dictionary
	cfg config
	lc  lifecycle

	enabled atomic.Bool
	node    atomic.Uint32
	active  atomic.Int64
	st      serverStats

	// sessions are only used by the service goroutine (or a test calling
	// handle).
	sessions map[sessKey]*session
	now      func() time.Time
}

// NewServer returns a USDO server for d. It does not receive until
// [Server.Start]. Options: [WithLogger], [WithTimeout], [WithBRS],
// [WithNodeID], [WithGroups], [WithMaxSessions], [WithMaxSize],
// [WithProcessingTime], [WithTProcUnit].
func NewServer(bus canopen.Bus, d *od.Dictionary, opts ...Option) (*Server, error) {
	if bus == nil || d == nil {
		return nil, errors.New("usdo: NewServer needs a bus and a dictionary")
	}
	cfg := newConfig(opts)
	if err := cfg.check(); err != nil {
		return nil, err
	}
	s := &Server{bus: bus, d: d, cfg: cfg, sessions: map[sessKey]*session{}, now: time.Now}
	s.enabled.Store(true)
	return s, nil
}

// NodeID returns the server's node-ID (0 before Start without
// [WithNodeID]).
func (s *Server) NodeID() canopen.NodeID {
	if id := canopen.NodeID(s.node.Load()); id != 0 {
		return id
	}
	return s.cfg.node
}

// SetNodeID changes the node-ID (after an NMT reset with a new node-ID);
// running transfers continue.
func (s *Server) SetNodeID(id canopen.NodeID) error {
	if err := id.Check(); err != nil {
		return err
	}
	s.node.Store(uint32(id))
	return nil
}

// SetEnabled enables or disables the server (the node disables it while
// NMT stopped). A disabled server ignores requests and drops its
// transfers.
func (s *Server) SetEnabled(on bool) { s.enabled.Store(on) }

// Sessions returns the number of transfers in progress.
func (s *Server) Sessions() int { return int(s.active.Load()) }

// Start resolves the node-ID ([WithNodeID] or the dictionary's),
// subscribes to the client requests and starts serving. Once it returns
// no request is missed.
func (s *Server) Start() error {
	return s.lc.start(func(context.Context) (func(context.Context), error) {
		id := s.NodeID()
		if id == 0 {
			id = s.d.NodeID()
		}
		if err := s.SetNodeID(id); err != nil {
			return nil, fmt.Errorf("usdo: server: %w", err)
		}
		sub, err := s.bus.Subscribe(canbus.WithFilters(filter(canopen.FuncSDORx)))
		if err != nil {
			return nil, fmt.Errorf("usdo: subscribe: %w", err)
		}
		return func(ctx context.Context) { s.run(ctx, sub) }, nil
	})
}

// Close stops the server and waits for its goroutine. It is idempotent.
func (s *Server) Close() error {
	s.lc.close()
	return nil
}

// Stats returns a snapshot of the counters.
func (s *Server) Stats() ServerStats {
	st := &s.st
	return ServerStats{
		Requests: st.requests.Load(), Uploads: st.uploads.Load(), Downloads: st.downloads.Load(),
		Segmented: st.segmented.Load(), Bulk: st.bulk.Load(), BytesUploaded: st.bytesUp.Load(),
		BytesDownloaded: st.bytesDown.Load(), AbortsSent: st.abortsSent.Load(), AbortsReceived: st.abortsReceived.Load(),
		Timeouts: st.timeouts.Load(), Restarts: st.restarts.Load(), Busy: st.busy.Load(), Duplicates: st.dups.Load(),
		Remote: st.remote.Load(), Ignored: st.ignr.Load(), SendErrors: st.sendErrors.Load(),
	}
}

// reply is a response to send to a client.
type reply struct {
	to canopen.NodeID
	m  Message
}

func (s *Server) run(ctx context.Context, sub *canbus.Subscription) {
	defer func() { _ = sub.Close() }()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next := s.expire()
		wait := time.Hour
		if !next.IsZero() {
			wait = max(time.Until(next), 0)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			s.cfg.log.Error("usdo server: subscription ended", "error", sub.Err())
			return
		case <-timer.C:
		case f := <-sub.Frames():
			for _, r := range s.handle(&f) {
				s.send(ctx, r)
			}
		}
	}
}

// expire aborts the transfers whose deadline passed and returns the next
// deadline (zero: none).
func (s *Server) expire() time.Time {
	now := s.now()
	var next time.Time
	for k, ss := range s.sessions {
		if !now.Before(ss.deadline) {
			s.st.timeouts.Add(1)
			s.drop(k)
			s.send(context.Background(), s.abort(k.client, k.id, ss.index, ss.sub, AbortTimeout))
			continue
		}
		if next.IsZero() || ss.deadline.Before(next) {
			next = ss.deadline
		}
	}
	return next
}

// send transmits a response.
func (s *Server) send(ctx context.Context, r reply) {
	f, err := Frame(r.m, s.NodeID(), true, s.cfg.brs)
	if err != nil {
		s.st.sendErrors.Add(1)
		return
	}
	sctx, cancel := sendCtx(ctx)
	defer cancel()
	if err := s.bus.Send(sctx, &f); err != nil {
		s.st.sendErrors.Add(1)
		s.cfg.log.Debug("usdo server send", "error", err)
	}
}

// addressed reports whether the server takes messages to a.
func (s *Server) addressed(a Address) bool {
	return a.IsBroadcast() || a == Address(s.NodeID()) || s.cfg.groups[a]
}

func (s *Server) abort(to canopen.NodeID, sid uint8, index uint16, sub uint8, code AbortCode) reply {
	s.st.abortsSent.Add(1)
	return reply{to, Message{Dest: Address(to), Cmd: CmdAbort, Session: sid, Index: index, SubIndex: sub, Abort: code}}
}

func (s *Server) respond(to canopen.NodeID, m Message) []reply {
	m.Dest = Address(to)
	return []reply{{to, m}}
}

// drop ends a session.
func (s *Server) drop(k sessKey) {
	if _, ok := s.sessions[k]; ok {
		delete(s.sessions, k)
		s.active.Add(-1)
	}
}

// open starts a session, replacing one with the same key.
func (s *Server) open(k sessKey, ss *session) {
	ss.deadline = s.now().Add(s.cfg.timeout)
	if _, ok := s.sessions[k]; !ok {
		s.active.Add(1)
	}
	s.sessions[k] = ss
}

// handle processes one received frame and returns the responses.
func (s *Server) handle(f *canbus.Frame) []reply {
	client, ok := sender(f, canopen.FuncSDORx)
	if !ok {
		s.st.ignr.Add(1)
		return nil
	}
	m, err := Decode(f.Payload())
	if err != nil && len(f.Payload()) < 3 {
		s.st.ignr.Add(1)
		return nil
	}
	if !s.addressed(m.Dest) {
		return nil
	}
	if m.Cmd.Remote() {
		s.st.remote.Add(1)
		return nil
	}
	if !s.enabled.Load() {
		s.st.ignr.Add(1)
		for k := range s.sessions {
			s.drop(k)
		}
		return nil
	}
	s.st.requests.Add(1)
	k := sessKey{client, m.Session}
	if err != nil {
		ss := s.sessions[k]
		s.drop(k)
		code := AbortDLC
		if errors.Is(err, ErrCommand) {
			code = AbortCommand
		}
		if ss != nil {
			return []reply{s.abort(client, m.Session, ss.index, ss.sub, code)}
		}
		return []reply{s.abort(client, m.Session, m.Index, m.SubIndex, code)}
	}
	switch m.Cmd {
	case CmdDownloadExpedited, CmdUpload, CmdDownloadInit, CmdBulkInit:
		if _, ok := s.sessions[k]; ok {
			s.st.restarts.Add(1)
			s.drop(k)
		}
		return s.initiate(k, m)
	case CmdDownloadSegment, CmdBulkSegment, CmdDownloadLast, CmdBulkLast:
		return s.segment(k, m)
	case CmdUploadSegment:
		return s.uploadSegment(k, m)
	case CmdAbort:
		if _, ok := s.sessions[k]; ok {
			s.st.abortsReceived.Add(1)
			s.drop(k)
		}
		return nil
	default: // a response command sent to a server
		s.drop(k)
		return []reply{s.abort(client, m.Session, m.Index, m.SubIndex, AbortCommand)}
	}
}

// initiate starts a transfer: expedited ones complete at once.
func (s *Server) initiate(k sessKey, m Message) []reply {
	refuse := func(code AbortCode) []reply { return []reply{s.abort(k.client, m.Session, m.Index, m.SubIndex, code)} }
	obj := Message{Session: m.Session, Index: m.Index, SubIndex: m.SubIndex}
	switch m.Cmd {
	case CmdUpload:
		data, err := s.d.Read(m.Index, m.SubIndex)
		if err != nil {
			return refuse(codeOf(err))
		}
		obj.DataType = s.dataType(m.Index, m.SubIndex)
		if len(data) <= MaxExpedited {
			obj.Cmd, obj.Data = CmdUploadExpedited, data
			s.st.uploads.Add(1)
			s.st.bytesUp.Add(uint64(len(data)))
			return s.respond(k.client, obj)
		}
		if len(s.sessions) >= s.cfg.sessions {
			s.st.busy.Add(1)
			return refuse(AbortBusy)
		}
		s.open(k, &session{kind: kindUpload, index: m.Index, sub: m.SubIndex, size: len(data), buf: data})
		obj.Cmd, obj.Size = CmdUploadInit, uint32(len(data))
		return s.respond(k.client, obj)
	case CmdDownloadExpedited:
		if code := s.checkDownload(k, m, len(m.Data)); code != 0 {
			return refuse(code)
		}
		if err := s.d.Write(m.Index, m.SubIndex, m.Data); err != nil {
			return refuse(codeOf(err))
		}
		s.st.downloads.Add(1)
		s.st.bytesDown.Add(uint64(len(m.Data)))
		obj.Cmd = CmdDownloadExpeditedOK
		return s.respond(k.client, obj)
	default: // segmented or bulk download
	}
	if s.cfg.maxSize > 0 && m.Size > uint32(s.cfg.maxSize) {
		return refuse(AbortOutOfMemory)
	}
	if code := s.checkDownload(k, m, int(m.Size)); code != 0 {
		return refuse(code)
	}
	if len(s.sessions) >= s.cfg.sessions {
		s.st.busy.Add(1)
		return refuse(AbortBusy)
	}
	ss := &session{kind: kindDownload, index: m.Index, sub: m.SubIndex, size: int(m.Size), buf: make([]byte, 0, min(int(m.Size), 1<<16))}
	obj.Cmd = CmdDownloadInitOK
	if m.Cmd == CmdBulkInit {
		ss.kind, obj.Cmd = kindBulk, CmdBulkInitOK
		obj.TProc = uint16(min(s.cfg.tproc/s.cfg.tunit, 0xFFFF))
	}
	s.open(k, ss)
	return s.respond(k.client, obj)
}

// dataType returns the data type byte of an object (0 when unknown or
// above FFh).
func (s *Server) dataType(index uint16, sub uint8) uint8 {
	v, err := s.d.Variable(index, sub)
	if err != nil || v.DataType > 0xFF {
		return 0
	}
	return uint8(v.DataType)
}

// checkDownload checks a download request before data is accepted: the
// object exists and is writable, the data type matches, and no other
// transfer writes it.
func (s *Server) checkDownload(k sessKey, m Message, size int) AbortCode {
	v, err := s.d.Variable(m.Index, m.SubIndex)
	if err != nil {
		return codeOf(err)
	}
	if !v.Access.Writable() {
		return AbortReadOnly
	}
	if m.DataType != 0 && (v.DataType > 0xFF || uint8(v.DataType) != m.DataType) {
		return AbortLength
	}
	if n := v.DataType.Size(); n > 0 && size != n {
		switch {
		case size > n:
			return AbortLengthHigh
		default:
			return AbortLengthLow
		}
	}
	for ok, o := range s.sessions {
		if ok != k && o.kind != kindUpload && o.index == m.Index && o.sub == m.SubIndex {
			return AbortParallel
		}
	}
	return 0
}

// segment handles a download segment.
func (s *Server) segment(k sessKey, m Message) []reply {
	ss := s.sessions[k]
	bulk := m.Cmd == CmdBulkSegment || m.Cmd == CmdBulkLast
	if ss == nil || ss.kind == kindUpload || (ss.kind == kindBulk) != bulk {
		s.drop(k)
		return []reply{s.abort(k.client, m.Session, 0, 0, AbortSession)}
	}
	fail := func(code AbortCode) []reply {
		s.drop(k)
		return []reply{s.abort(k.client, m.Session, ss.index, ss.sub, code)}
	}
	ss.deadline = s.now().Add(s.cfg.timeout)
	last := m.Cmd == CmdDownloadLast || m.Cmd == CmdBulkLast
	if !last {
		switch {
		case m.Counter == counter(ss.seq+1):
		case ss.seq > 0 && m.Counter == counter(ss.seq):
			// a repeated segment: confirm it again (segmented) or ignore it
			s.st.dups.Add(1)
			if bulk {
				return nil
			}
			return s.respond(k.client, Message{Cmd: CmdDownloadSegmentOK, Session: m.Session, Counter: m.Counter})
		default:
			return fail(AbortCounter)
		}
		ss.seq++
	}
	if len(ss.buf)+len(m.Data) > ss.size {
		return fail(AbortSize)
	}
	ss.buf = append(ss.buf, m.Data...)
	if !last {
		if bulk {
			return nil
		}
		return s.respond(k.client, Message{Cmd: CmdDownloadSegmentOK, Session: m.Session, Counter: m.Counter})
	}
	if len(ss.buf) != ss.size {
		return fail(AbortSize)
	}
	s.drop(k)
	if err := s.d.Write(ss.index, ss.sub, ss.buf); err != nil {
		return []reply{s.abort(k.client, m.Session, ss.index, ss.sub, codeOf(err))}
	}
	s.st.downloads.Add(1)
	s.st.bytesDown.Add(uint64(len(ss.buf)))
	if bulk {
		s.st.bulk.Add(1)
		return s.respond(k.client, Message{Cmd: CmdBulkLastOK, Session: m.Session})
	}
	s.st.segmented.Add(1)
	return s.respond(k.client, Message{Cmd: CmdDownloadLastOK, Session: m.Session})
}

// counter returns the counter byte of segment n (1-based): n modulo 256.
func counter(n int) uint8 { return uint8(n) }

// uploadSegment answers a request for an upload segment: the next one, or
// the last one sent again when the request repeats.
func (s *Server) uploadSegment(k sessKey, m Message) []reply {
	ss := s.sessions[k]
	if ss == nil || ss.kind != kindUpload {
		s.drop(k)
		return []reply{s.abort(k.client, m.Session, 0, 0, AbortSession)}
	}
	n := ss.seq + 1
	switch {
	case m.Counter == counter(n):
		ss.seq = n
	case ss.seq > 0 && m.Counter == counter(ss.seq):
		s.st.dups.Add(1)
		n = ss.seq
	default:
		s.drop(k)
		return []reply{s.abort(k.client, m.Session, ss.index, ss.sub, AbortCounter)}
	}
	ss.deadline = s.now().Add(s.cfg.timeout)
	off := (n - 1) * SegmentSize
	end := min(off+SegmentSize, ss.size)
	out := Message{Session: m.Session, Data: ss.buf[off:end], Cmd: CmdUploadSegmentData, Counter: m.Counter}
	if end == ss.size {
		out.Cmd, out.Counter = CmdUploadLast, 0
		s.drop(k)
		s.st.uploads.Add(1)
		s.st.segmented.Add(1)
		s.st.bytesUp.Add(uint64(ss.size))
	}
	return s.respond(k.client, out)
}

// lifecycle is the Start/Close state of a long-running service.
type lifecycle struct {
	mu     sync.Mutex
	state  int // 0 new, 1 running, 2 closed
	cancel context.CancelFunc
	done   chan struct{}
}

// start runs setup and, when it succeeds, the loop it returns in a new
// goroutine.
func (l *lifecycle) start(setup func(ctx context.Context) (func(ctx context.Context), error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.state {
	case 1:
		return fmt.Errorf("%w: already started", canopen.ErrBusy)
	case 2:
		return canopen.ErrClosed
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop, err := setup(ctx)
	if err != nil {
		cancel()
		return err
	}
	l.cancel, l.done, l.state = cancel, make(chan struct{}), 1
	go func() {
		defer close(l.done)
		loop(ctx)
	}()
	return nil
}

// running reports whether the service was started and not closed.
func (l *lifecycle) running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == 1
}

// close stops the loop and waits for it; later calls do nothing.
func (l *lifecycle) close() {
	l.mu.Lock()
	st, cancel, done := l.state, l.cancel, l.done
	l.state = 2
	l.mu.Unlock()
	if st == 1 {
		cancel()
		<-done
	}
}
