// SPDX-License-Identifier: MIT

package sdo

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/otfabric/go-canopen"
)

// Defaults.
const (
	// DefaultTimeout is the default response timeout of the client and
	// the default inactivity timeout of the server.
	DefaultTimeout = time.Second
	// DefaultBlockThreshold is the default size from which [BlockAuto]
	// uses block transfer.
	DefaultBlockThreshold = 32
)

// BlockMode selects when the client uses block transfer.
type BlockMode uint8

// Block transfer modes.
const (
	// BlockNever uses expedited and segmented transfer only (the default:
	// every server supports them).
	BlockNever BlockMode = iota
	// BlockAlways uses block transfer for every upload and download.
	// Uploads offer the protocol switch threshold set by
	// [WithProtocolSwitch] (default 0: no switch).
	BlockAlways
	// BlockAuto uses block download for data of at least the block
	// threshold ([WithBlockThreshold]), and block upload with a protocol
	// switch threshold of the block threshold minus one (capped at 255),
	// so that the server answers smaller objects with a normal upload.
	BlockAuto
)

// String names the mode.
func (m BlockMode) String() string {
	switch m {
	case BlockNever:
		return "never"
	case BlockAlways:
		return "always"
	case BlockAuto:
		return "auto"
	}
	return fmt.Sprintf("BlockMode(%d)", uint8(m))
}

// config holds the settings of a client or a server.
type config struct {
	log       *slog.Logger
	timeout   time.Duration
	blkSize   int
	maxSize   int
	crc       bool
	cobSet    bool
	c2s, s2c  canopen.COBID
	mode      BlockMode
	threshold int
	pst       int // -1: derived from the mode
	retries   int
	resetNode canopen.NodeID // WithResetFence: 0 none
}

func defaultConfig() config {
	return config{
		timeout:   DefaultTimeout,
		blkSize:   MaxBlockSize,
		maxSize:   DefaultMaxSize,
		crc:       true,
		threshold: DefaultBlockThreshold,
		pst:       -1,
	}
}

func (c *config) check() error {
	switch {
	case c.timeout <= 0:
		return fmt.Errorf("sdo: timeout %v must be positive", c.timeout)
	case c.blkSize < 1 || c.blkSize > MaxBlockSize:
		return fmt.Errorf("sdo: block size %d outside 1..%d", c.blkSize, MaxBlockSize)
	case c.maxSize < 0:
		return fmt.Errorf("sdo: negative size limit %d", c.maxSize)
	case c.pst > 255:
		return fmt.Errorf("sdo: protocol switch threshold %d above 255", c.pst)
	case c.retries < 0:
		return fmt.Errorf("sdo: negative retry count %d", c.retries)
	case c.mode > BlockAuto:
		return fmt.Errorf("sdo: unknown block mode %d", c.mode)
	}
	if c.log == nil {
		c.log = slog.New(discardHandler{})
	}
	return nil
}

// ServerOption configures a [Server].
type ServerOption interface{ applyServer(*config) }

// ClientOption configures a [Client].
type ClientOption interface{ applyClient(*config) }

// Option configures a [Server] or a [Client].
type Option interface {
	ServerOption
	ClientOption
}

type option func(*config)

func (o option) applyServer(c *config) { o(c) }
func (o option) applyClient(c *config) { o(c) }

type serverOption func(*config)

func (o serverOption) applyServer(c *config) { o(c) }

type clientOption func(*config)

func (o clientOption) applyClient(c *config) { o(c) }

// WithLogger logs transfers and aborts to l (at debug level) and
// unexpected conditions (at warn level). The default logs nothing.
func WithLogger(l *slog.Logger) Option { return option(func(c *config) { c.log = l }) }

// WithTimeout sets the client's response timeout and the server's
// inactivity timeout (default [DefaultTimeout]). During a block transfer
// the receiving side repeats its last acknowledgement after a quarter of
// it without traffic.
func WithTimeout(d time.Duration) Option { return option(func(c *config) { c.timeout = d }) }

// WithCOBIDs sets the COB-IDs of the channel: client to server (the
// server receives, default 0x600 + node-ID) and server to client (default
// 0x580 + node-ID). A server otherwise takes them from object 0x1200.
func WithCOBIDs(clientToServer, serverToClient canopen.COBID) Option {
	return option(func(c *config) { c.cobSet, c.c2s, c.s2c = true, clientToServer, serverToClient })
}

// WithResetFence makes the server of node node also receive the NMT
// commands (COB-ID 0) and disable itself at a reset node or reset
// communication addressed to node or to all nodes. The command and the
// requests reach the server in the order they arrived, so a request
// received after the command is never answered by this server: the node
// is resetting (Initialising) and answers again with the server it
// starts after the reset (CiA 301). The server also ignores requests the
// transport received before it started (by the frame's receive
// timestamp): a server started at a reset, before the boot-up message,
// does not answer a request that arrived during the reset. Package node
// uses it for its SDO server, which it replaces at every reset.
func WithResetFence(node canopen.NodeID) ServerOption {
	return serverOption(func(c *config) { c.resetNode = node })
}

// WithBlockSize sets the number of segments per block (1..127, default
// 127) the side receiving a block transfer asks for: the server for
// block downloads, the client for block uploads.
func WithBlockSize(n int) Option { return option(func(c *config) { c.blkSize = n }) }

// WithMaxSize bounds the data of one transfer the side receives (default
// [DefaultMaxSize]): the server's downloads, the client's uploads.
// Larger transfers are aborted with [canopen.AbortOutOfMemory].
func WithMaxSize(n int) Option { return option(func(c *config) { c.maxSize = n }) }

// WithCRC enables (the default) or disables the CRC of block transfers.
// The CRC is used when both sides support it.
func WithCRC(on bool) Option { return option(func(c *config) { c.crc = on }) }

// WithBlockMode selects when the client uses block transfer (default
// [BlockNever]).
func WithBlockMode(m BlockMode) ClientOption { return clientOption(func(c *config) { c.mode = m }) }

// WithBlockThreshold sets the size from which [BlockAuto] uses block
// transfer (default [DefaultBlockThreshold]).
func WithBlockThreshold(n int) ClientOption {
	return clientOption(func(c *config) { c.threshold = n })
}

// WithProtocolSwitch sets the protocol switch threshold the client offers
// in block uploads (0..255): the server answers objects of at most that
// many bytes with a normal (expedited or segmented) upload. 0 forbids the
// switch. It overrides the threshold [BlockAuto] derives.
func WithProtocolSwitch(pst int) ClientOption {
	return clientOption(func(c *config) { c.pst = pst })
}

// WithRetries makes the client repeat a transfer that timed out up to n
// times (default 0): a local timeout, or an abort with
// [canopen.AbortTimeout] from the server (which times out first when a
// request was lost). Each attempt starts a new transfer, so retried
// downloads must be idempotent, as writes of a value are.
func WithRetries(n int) ClientOption { return clientOption(func(c *config) { c.retries = n }) }

// CallOption overrides a client setting for one transfer.
type CallOption func(*callConfig)

type callConfig struct {
	mode BlockMode
}

// UseBlockMode overrides the client's block mode for one transfer.
func UseBlockMode(m BlockMode) CallOption { return func(c *callConfig) { c.mode = m } }
