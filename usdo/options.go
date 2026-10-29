// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/otfabric/go-canopen"
)

// Option configures a [Client] or a [Server]. Each option names the side
// it applies to; the other ignores it.
type Option func(*config)

type config struct {
	log      *slog.Logger
	timeout  time.Duration
	brs      bool
	node     canopen.NodeID
	groups   map[Address]bool
	sessions int
	maxSize  int
	tproc    time.Duration
	tunit    time.Duration
}

func newConfig(opts []Option) config {
	c := config{log: discard, timeout: time.Second, sessions: 16, maxSize: 1 << 20, tunit: 100 * time.Microsecond}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.log == nil {
		c.log = discard
	}
	return c
}

func (c *config) check() error {
	switch {
	case c.timeout <= 0:
		return errors.New("usdo: timeout must be positive")
	case c.sessions < 1:
		return errors.New("usdo: at least one session needed")
	case c.maxSize < 0:
		return errors.New("usdo: negative maximum size")
	case c.tunit <= 0 || c.tproc < 0:
		return errors.New("usdo: invalid processing time")
	}
	return nil
}

// WithLogger logs to l (both sides; default: nothing).
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithTimeout sets the response timeout of a [Client] (the time it waits
// for each response, and the collection time of transfers to several
// servers) and the inactivity timeout after which a [Server] aborts a
// transfer (both default 1 s).
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithBRS sends the frames with bit rate switching (both sides; default
// off, which every CAN FD network carries).
func WithBRS(on bool) Option { return func(c *config) { c.brs = on } }

// WithNodeID sets the node-ID of a [Server] (default: the dictionary's,
// read at Start).
func WithNodeID(id canopen.NodeID) Option { return func(c *config) { c.node = id } }

// WithGroups makes a [Server] a member of multicast groups (addresses
// 80h..FFh; others are ignored). How group membership is configured in
// CiA 1301 is not public, so it is an option.
func WithGroups(groups ...Address) Option {
	return func(c *config) {
		if c.groups == nil {
			c.groups = map[Address]bool{}
		}
		for _, g := range groups {
			if g.IsMulticast() {
				c.groups[g] = true
			}
		}
	}
}

// WithMaxSessions limits the transfers a [Server] runs at once (default
// 16); one more is refused with [AbortBusy].
func WithMaxSessions(n int) Option { return func(c *config) { c.sessions = n } }

// WithMaxSize limits the size of a download a [Server] accepts (default
// 1 MiB; 0: no limit); a larger one is refused with [AbortOutOfMemory].
func WithMaxSize(n int) Option { return func(c *config) { c.maxSize = n } }

// WithProcessingTime sets the time a [Server] needs between two segments
// of a bulk transfer, which it reports in the t_proc field of its
// initiate response (default 0).
func WithProcessingTime(d time.Duration) Option { return func(c *config) { c.tproc = d } }

// WithTProcUnit sets the unit of the t_proc field (both sides; default
// 100 µs). CiA's poster defines the field ("server's time for processing
// consecutive segments; which may be considered by the client") but its
// unit is not public, so client and server must agree on it.
func WithTProcUnit(d time.Duration) Option { return func(c *config) { c.tunit = d } }

// CallOption configures one client transfer.
type CallOption func(*call)

type call struct {
	dataType uint8
	bulk     bool
	expect   []canopen.NodeID
	timeout  time.Duration
}

// WithDataType sets the data type byte of a download (default 0:
// unspecified). A server compares a non-zero data type with the object's
// and refuses a mismatch with [AbortLength]. Data types above FFh cannot
// be sent: the field is one byte.
func WithDataType(t canopen.DataType) CallOption {
	return func(c *call) {
		if t <= 0xFF {
			c.dataType = uint8(t)
		}
	}
}

// UseBulk makes a download of more than [MaxExpedited] bytes a bulk
// transfer (segments confirmed as a whole) instead of a segmented one
// (each segment confirmed). Downloads to several servers always use bulk
// transfer.
func UseBulk(on bool) CallOption { return func(c *call) { c.bulk = on } }

// Expect lists the servers a transfer to several servers waits for: it
// ends as soon as all of them answered instead of after the timeout.
func Expect(nodes ...canopen.NodeID) CallOption {
	return func(c *call) { c.expect = append(c.expect, nodes...) }
}

// WithCallTimeout overrides the client's response timeout for one
// transfer.
func WithCallTimeout(d time.Duration) CallOption { return func(c *call) { c.timeout = d } }

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// sendTimeout bounds every frame a service sends on its own.
const sendTimeout = time.Second

// sendCtx returns a context bounded by sendTimeout.
func sendCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, sendTimeout)
}

// asAbort is errors.As for canopen.AbortCode.
func asAbort(err error, a *canopen.AbortCode) bool { return errors.As(err, a) }
