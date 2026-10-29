// SPDX-License-Identifier: MIT

package node

import (
	"io"
	"log/slog"
	"time"

	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/timestamp"
)

// Option configures a [Node].
type Option func(*config)

type config struct {
	log       *slog.Logger
	onState   func(nmt.StateChange)
	onHB      func(nmt.HeartbeatEvent)
	onTime    func(timestamp.Event)
	sdoOpts   []sdo.ServerOption
	timePer   time.Duration
	drive     *cia402.DriveConfig
	drivePer  time.Duration
	autoStart bool
}

func newConfig(opts []Option) config {
	c := config{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithLogger logs the node's events, and those of its services, to l.
// The default logs nothing.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.log = l
		}
	}
}

// WithStateHandler calls h after every NMT state change of the node, once
// the node's services have been enabled or disabled for the new state.
// It runs on the goroutine that changed the state (usually the NMT
// slave's) and must not block for long; it must not call [Node.Reset].
func WithStateHandler(h func(nmt.StateChange)) Option {
	return func(c *config) { c.onState = h }
}

// WithHeartbeatHandler calls h with every event of the node's heartbeat
// consumer (0x1016): boot-ups, state changes, losses and resumptions of
// the nodes it hears. It runs on the consumer's goroutine.
func WithHeartbeatHandler(h func(nmt.HeartbeatEvent)) Option {
	return func(c *config) { c.onHB = h }
}

// WithTimeHandler calls h with every TIME message the node consumes
// (0x1012 bit 31 set) while it is pre-operational or operational.
func WithTimeHandler(h func(timestamp.Event)) Option {
	return func(c *config) { c.onTime = h }
}

// WithTimePeriod makes the node's TIME producer send the time every
// period while 0x1012 bit 30 (produce) is set and the node is
// pre-operational or operational. The default sends only on
// [Node.SendTime].
func WithTimePeriod(d time.Duration) Option {
	return func(c *config) { c.timePer = d }
}

// WithSDOServerOptions passes options (timeout, block size, maximum
// size...) to the node's SDO server. COB-ID options are ignored: the
// server always follows 0x1200.
func WithSDOServerOptions(opts ...sdo.ServerOption) Option {
	return func(c *config) { c.sdoOpts = append(c.sdoOpts, opts...) }
}

// WithDrive makes the node a CiA 402 drive: a [cia402.Drive] is attached
// to the dictionary (its objects are added where missing) and simulated
// with a step every period while the node runs (0: the application calls
// [cia402.Drive.Step] itself). A fault of the drive is raised as an
// emergency (its error code, with the error register bits of its class
// and the device profile bit) and cleared when the drive's fault is
// reset; cfg.OnError, when set, is called as well. The drive's error
// code is 0x603F, kept by the drive.
func WithDrive(cfg cia402.DriveConfig, period time.Duration) Option {
	return func(c *config) { c.drive, c.drivePer = &cfg, period }
}

// WithAutoStart makes the node enter Operational on its own right after
// its boot-up (CiA 302 "NMT startup" of a self-starting device), and after
// every reset.
func WithAutoStart() Option {
	return func(c *config) { c.autoStart = true }
}
