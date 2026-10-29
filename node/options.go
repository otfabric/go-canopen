// SPDX-License-Identifier: MIT

package node

import (
	"io"
	"log/slog"
	"time"

	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/lss"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
	"github.com/otfabric/go-canopen/timestamp"
	"github.com/otfabric/go-canopen/usdo"
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
	store     store.Store
	program   *program.Config
	noHBCons  bool
	lss       []lss.SlaveOption
	lssOn     bool
	fd, brs   bool
	usdoOpts  []usdo.Option
	emcyOpts  []emcy.Option
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

// WithStore keeps the node's parameters in s (CiA 301 store and restore,
// see package store): the saved values are loaded when the node is
// created (New fails when s cannot be read) and after every NMT reset
// (those of the areas the reset restores), and when the dictionary has
// 0x1010 and 0x1011 (store.AddObjects adds them) writes of "save" and
// "load" to them save the current values of a group of parameters and
// delete them ([Node.SaveParameters], [Node.RestoreDefaults]). Reads of
// their sub-indices 1..4 return 1 (on command).
func WithStore(s store.Store) Option {
	return func(c *config) { c.store = s }
}

// WithProgram makes the node a programmable device (CiA 302-3 program
// download): the program download objects 0x1F50, 0x1F51, 0x1F56 and
// 0x1F57 are added where missing and served by a [program.Device] whose
// Handler (cfg.Handler) does the flashing; see package program. The
// program control 0x1F51 accepts commands in Pre-operational only.
func WithProgram(cfg program.Config) Option {
	return func(c *config) { c.program = &cfg }
}

// WithoutHeartbeatConsumer leaves out the heartbeat consumer even when the
// dictionary has 0x1016: for a device whose heartbeat consumption is done
// elsewhere, such as the CANopen manager of package master, which
// supervises the slaves of 0x1016 itself (and must not raise emergencies
// or change state when a slave is lost).
func WithoutHeartbeatConsumer() Option {
	return func(c *config) { c.noHBCons = true }
}

// WithLSS makes the node an LSS slave (CiA 305, package lss) with the LSS
// address of its identity object 0x1018 (New fails without it). The
// dictionary's node-ID may then be lss.Unconfigured (0xFF): the node
// starts unconfigured, stays in NMT Initialising without boot-up message,
// heartbeat, SDO or any other service until LSS gives it a node-ID, and
// then boots (reset communication with the new node-ID). The node wires
// the LSS services to itself:
//
//   - configure node-ID sets the pending node-ID of the NMT slave
//     ([Node.SetNodeID]): it applies at the next reset communication or
//     reset node; an unconfigured node resets on its own when the master
//     switches it back to the waiting state (CiA 305 as CANopenNode does);
//   - after every applied reset the LSS slave learns the active node-ID;
//   - store configuration saves the pending node-ID (and bit timing) in
//     the store of [WithStore] (under the pseudo index 0x0000, outside
//     every dictionary area, so parameter saves and restores keep it); New
//     applies a stored node-ID instead of the dictionary's, so that it
//     survives restarts ([Node.StoredLSS]). Without WithStore the node
//     answers "store configuration not supported".
//
// opts configure the LSS slave: the logger, the LSS state handler and the
// bit timing handlers (lss.WithBitTimingHandler, lss.WithActivateHandler),
// which belong to the application since only it can switch the bit rate
// of its CAN controller. The node-ID, reset and store handlers are the
// node's (options setting them are overridden).
func WithLSS(opts ...lss.SlaveOption) Option {
	return func(c *config) { c.lssOn, c.lss = true, append(c.lss, opts...) }
}

// WithFD makes the node a CANopen FD device (CiA 1301), with bit rate
// switching when brs is true: every service sends CAN FD frames — the
// PDOs (pdo.WithFD: up to 64 bytes, no remote frames), SYNC
// (syncobj.WithFD), EMCY (emcy.WithFD: the 20-byte CANopen FD emergency,
// logical device 0, CiA specification 301 and the system clock's time
// unless [WithEMCYOptions] says otherwise), the NMT
// boot-up message and heartbeat (nmt.WithFD) — and accepts them. The SDO
// server is replaced by a USDO server (package usdo), as CiA 1301
// replaces the SDO by the USDO: both answer on 580h + node-ID, so they
// cannot coexist. The USDO server follows the NMT state (enabled in
// Pre-operational and Operational) and the node-ID. Node guarding uses
// remote frames, which CAN FD does not have: the guarding responder is
// left out. MPDOs stay 8-byte classical frames (package mpdo).
func WithFD(brs bool) Option {
	return func(c *config) { c.fd, c.brs = true, brs }
}

// WithUSDOServerOptions passes options (timeout, sessions, groups, maximum
// size...) to the USDO server of [WithFD]. The node-ID option is ignored:
// the server follows the node's.
func WithUSDOServerOptions(opts ...usdo.Option) Option {
	return func(c *config) { c.usdoOpts = append(c.usdoOpts, opts...) }
}

// WithEMCYOptions passes options to the node's emergency producer after
// the node's own (for example emcy.WithFDInfo or emcy.WithFDLayout for
// CANopen FD, or emcy.WithHistory).
func WithEMCYOptions(opts ...emcy.Option) Option {
	return func(c *config) { c.emcyOpts = append(c.emcyOpts, opts...) }
}
