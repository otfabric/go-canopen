// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// ServerStats counts what a [Server] did.
type ServerStats struct {
	Requests        uint64 `json:"requests"`        // messages received while enabled
	Uploads         uint64 `json:"uploads"`         // uploads completed
	Downloads       uint64 `json:"downloads"`       // downloads completed
	BlockTransfers  uint64 `json:"blockTransfers"`  // block transfers completed (also counted above)
	ProtocolSwitch  uint64 `json:"protocolSwitch"`  // block uploads answered with a normal upload
	BytesUploaded   uint64 `json:"bytesUploaded"`   // data bytes of completed uploads
	BytesDownloaded uint64 `json:"bytesDownloaded"` // data bytes of completed downloads
	AbortsSent      uint64 `json:"abortsSent"`      // aborts sent (refusals, protocol errors, timeouts)
	AbortsReceived  uint64 `json:"abortsReceived"`  // transfers the client aborted
	Timeouts        uint64 `json:"timeouts"`        // transfers aborted for inactivity
	Restarts        uint64 `json:"restarts"`        // transfers abandoned for a new initiate
	Reacks          uint64 `json:"reacks"`          // block acknowledgements repeated
	Ignored         uint64 `json:"ignored"`         // messages ignored (out of sequence, stale, disabled)
	SendErrors      uint64 `json:"sendErrors"`      // messages the bus failed to send
}

type serverStats struct {
	requests, uploads, downloads, blocks, switches, bytesUp, bytesDown atomic.Uint64
	abortsSent, abortsReceived, timeouts, restarts, reacks, ignored    atomic.Uint64
	sendErrors                                                         atomic.Uint64
}

// Server is an SDO server (CiA 301 7.2.4): it serves an object dictionary
// on one SDO channel to one client at a time, with expedited, segmented
// and block transfer.
//
// Remote accesses go through [od.Dictionary.Read] and
// [od.Dictionary.Write], so access rights, limits and hooks apply, and a
// refusal reaches the client as the dictionary's abort code. Segmented
// and block downloads are buffered and written once, when complete.
type Server struct {
	bus canopen.Bus
	d   *od.Dictionary
	cfg config
	log *slog.Logger

	enabled atomic.Bool
	st      serverStats

	mu       sync.Mutex
	started  bool
	since    time.Time // WithResetFence: requests received earlier are ignored
	closed   bool
	c2s, s2c canopen.COBID
	sub      *canbus.Subscription
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewServer returns a server for d on bus. It does not receive until
// [Server.Start]. Options: [WithLogger], [WithTimeout], [WithCOBIDs],
// [WithBlockSize], [WithMaxSize], [WithCRC], [WithResetFence].
func NewServer(bus canopen.Bus, d *od.Dictionary, opts ...ServerOption) (*Server, error) {
	if bus == nil || d == nil {
		return nil, errors.New("sdo: NewServer needs a bus and a dictionary")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o.applyServer(&cfg)
	}
	if err := cfg.check(); err != nil {
		return nil, err
	}
	s := &Server{bus: bus, d: d, cfg: cfg, log: cfg.log}
	s.enabled.Store(true)
	return s, nil
}

// COBIDs returns the COB-IDs the server uses: client to server (received)
// and server to client (sent). Before Start they are the configured or
// default ones.
func (s *Server) COBIDs() (clientToServer, serverToClient canopen.COBID, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return s.c2s, s.s2c, nil
	}
	return s.resolveCOBIDs()
}

// resolveCOBIDs returns the configured COB-IDs, else those of object
// 0x1200, else the defaults of the dictionary's node-ID.
func (s *Server) resolveCOBIDs() (canopen.COBID, canopen.COBID, error) {
	if s.cfg.cobSet {
		return s.cfg.c2s, s.cfg.s2c, nil
	}
	rx, err1 := s.d.GetUint(od.IndexSDOServer, 1)
	tx, err2 := s.d.GetUint(od.IndexSDOServer, 2)
	if err1 == nil && err2 == nil {
		return canopen.COBID(rx), canopen.COBID(tx), nil
	}
	node := s.d.NodeID()
	if err := node.Check(); err != nil {
		return 0, 0, fmt.Errorf("sdo: no SDO COB-IDs: %w", err)
	}
	return canopen.NewCOBID(canopen.FuncSDORx + canbus.ID(node)), canopen.NewCOBID(canopen.FuncSDOTx + canbus.ID(node)), nil
}

// Start resolves the COB-IDs (the option, object 0x1200 sub-indices 1
// and 2, or 0x600/0x580 + the dictionary's node-ID), subscribes to
// requests and starts serving. Once it returns no request is missed. The
// COB-IDs are read once: restart the server after changing them.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return canopen.ErrClosed
	case s.started:
		return fmt.Errorf("sdo: server %w: already started", canopen.ErrBusy)
	}
	c2s, s2c, err := s.resolveCOBIDs()
	if err != nil {
		return err
	}
	if !c2s.Valid() || !s2c.Valid() {
		return fmt.Errorf("sdo: SDO server COB-IDs %s / %s are not valid", c2s, s2c)
	}
	filters := []canbus.Filter{c2s.Filter()}
	if s.cfg.resetNode != 0 {
		filters = append(filters, canbus.ExactStandard(canopen.FuncNMT))
	}
	s.since = time.Now()
	sub, err := s.bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: filters}))
	if err != nil {
		return fmt.Errorf("sdo: subscribe: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.c2s, s.s2c, s.sub, s.cancel = c2s, s2c, sub, cancel
	s.done = make(chan struct{})
	s.started = true
	go s.run(ctx)
	s.log.Debug("sdo: server started", "rx", c2s, "tx", s2c)
	return nil
}

// Close stops the server and waits for it. It is safe to call more than
// once.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	s.mu.Unlock()
	if started {
		s.cancel()
		_ = s.sub.Close()
		<-s.done
	}
	return nil
}

// SetEnabled enables (the default) or disables the server. A disabled
// server ignores requests and drops a transfer in progress without an
// abort, as a node in the NMT state stopped does.
func (s *Server) SetEnabled(on bool) { s.enabled.Store(on) }

// Enabled reports whether the server answers requests.
func (s *Server) Enabled() bool { return s.enabled.Load() }

// Stats returns a snapshot of the counters.
func (s *Server) Stats() ServerStats {
	st := &s.st
	return ServerStats{
		Requests: st.requests.Load(), Uploads: st.uploads.Load(), Downloads: st.downloads.Load(),
		BlockTransfers: st.blocks.Load(), ProtocolSwitch: st.switches.Load(),
		BytesUploaded: st.bytesUp.Load(), BytesDownloaded: st.bytesDown.Load(),
		AbortsSent: st.abortsSent.Load(), AbortsReceived: st.abortsReceived.Load(),
		Timeouts: st.timeouts.Load(), Restarts: st.restarts.Load(), Reacks: st.reacks.Load(),
		Ignored: st.ignored.Load(), SendErrors: st.sendErrors.Load(),
	}
}

func (s *Server) newMachine(send func(frame)) *machine {
	return &machine{
		d: s.d, out: send, maxSize: s.cfg.maxSize, blkSize: uint8(s.cfg.blkSize),
		crcOK: s.cfg.crc, st: &s.st,
	}
}

// run receives requests until the context ends. It owns the machine.
func (s *Server) run(ctx context.Context) {
	defer close(s.done)
	m := s.newMachine(func(f frame) {
		cf := canFrame(s.s2c, f)
		sctx, cancel := context.WithTimeout(ctx, s.cfg.timeout)
		err := s.bus.Send(sctx, &cf)
		cancel()
		if err != nil {
			s.st.sendErrors.Add(1)
			if ctx.Err() == nil {
				s.log.Warn("sdo: server send failed", "err", err)
			}
		} else if f.cs() == csAbort && f[0] == abortByte {
			idx, sub := f.mux()
			s.log.Debug("sdo: server abort", "index", idx, "sub", sub, "code", canopen.AbortCode(f.u32()))
		}
	})
	timeout, reackEvery := s.cfg.timeout, s.cfg.timeout/4
	var progress, signal time.Time // last progress, last traffic or repeated acknowledgement
	for {
		wctx, cancel := ctx, context.CancelFunc(func() {})
		var hard, next time.Time
		if m.active() {
			hard, next = progress.Add(timeout), progress.Add(timeout)
			if m.receiving() {
				if r := signal.Add(reackEvery); r.Before(next) {
					next = r
				}
			}
			wctx, cancel = context.WithDeadline(ctx, next)
		}
		cf, err := s.sub.Receive(wctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				return // subscription closed: the bus is gone
			}
			now := time.Now()
			if !s.enabled.Load() {
				m.reset()
				continue
			}
			if !now.Before(hard) {
				m.timeout()
			} else {
				m.reack()
				signal = now
			}
			continue
		}
		if s.cfg.resetNode != 0 && cf.ID == canopen.FuncNMT && cf.Flags&canbus.FlagExtended == 0 {
			if s.resetCommand(&cf) {
				s.enabled.Store(false)
				m.reset()
			}
			continue
		}
		f, ok := payload(&cf)
		if !ok {
			continue
		}
		if !s.enabled.Load() {
			s.st.ignored.Add(1)
			m.reset()
			continue
		}
		if s.cfg.resetNode != 0 && !cf.Timestamp.IsZero() && cf.Timestamp.Before(s.since) {
			// Received before this server started: during the reset that
			// replaced the previous one (WithResetFence).
			s.st.ignored.Add(1)
			continue
		}
		now := time.Now()
		signal = now
		if m.handle(f) {
			progress = now
		}
	}
}

// resetCommand reports whether an NMT frame is a reset node or reset
// communication for the server's node (WithResetFence).
func (s *Server) resetCommand(f *canbus.Frame) bool {
	if f.Len < 2 {
		return false
	}
	cmd, node := canopen.NMTCommand(f.Data[0]), canopen.NodeID(f.Data[1])
	return (cmd == canopen.NMTResetNode || cmd == canopen.NMTResetCommunication) && (node == 0 || node == s.cfg.resetNode)
}
