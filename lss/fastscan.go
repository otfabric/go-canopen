// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

// FastscanOptions tune a Fastscan.
type FastscanOptions struct {
	// Timeout is the listening window of each query (default: the
	// master's [WithFastscanTimeout]).
	Timeout time.Duration
	// Retries is how many times an unanswered, idempotent query is sent
	// again before its silence counts as "no" (0: the master's
	// [WithFastscanRetries]; negative: none).
	Retries int
	// Known gives parts of the address that are known: they are verified
	// instead of scanned (CANopenNode's CO_LSSmaster_FS_MATCH), which
	// restricts the scan to devices with these values.
	Known map[Part]uint32
	// Max stops [Master.Fastscan] after this many devices (0: all).
	Max int
}

// fastscan is one Fastscan run within a session.
type fastscan struct {
	s       *session
	window  time.Duration
	retries int
	known   map[Part]uint32
}

func (m *Master) newFastscan(s *session, o FastscanOptions) *fastscan {
	fs := &fastscan{s: s, window: o.Timeout, retries: o.Retries, known: o.Known}
	if fs.window <= 0 {
		fs.window = m.cfg.fsTimeout
	}
	switch {
	case fs.retries == 0:
		fs.retries = m.cfg.fsRetries
	case fs.retries < 0:
		fs.retries = 0
	}
	return fs
}

// query sends one Fastscan request and reports whether at least one
// slave acknowledged it within the window. After the first
// acknowledgement it waits a quarter window for those of other slaves
// (identical frames, which a real bus usually merges into one), so that
// they do not leak into the next query; stragglers are discarded before
// the next query. An unanswered query is sent again retries times.
func (fs *fastscan) query(id uint32, bitCheck uint8, sub, next Part, retries int) (bool, error) {
	for attempt := 0; ; attempt++ {
		fs.s.drain()
		fs.s.m.count(func(st *MasterStats) { st.Queries++ })
		if err := fs.s.send(fastscanFrame(id, bitCheck, sub, next)); err != nil {
			return false, err
		}
		got, err := fs.s.listen(fs.window, CmdIdentifySlave, fs.window/4)
		if err != nil {
			return false, err
		}
		if len(got) > 0 {
			return true, nil
		}
		if attempt >= retries {
			return false, nil
		}
		fs.s.m.count(func(st *MasterStats) { st.Retries++ })
	}
}

// reset restarts the Fastscan of every slave (BitCheck 0x80) and reports
// whether any unconfigured slave answered.
func (fs *fastscan) reset() (bool, error) {
	return fs.query(0, fastscanConfirm, PartVendor, PartVendor, fs.retries)
}

// verify checks a whole part and moves the matching slaves to the next
// part (or, after the serial number, to the configuration state). It is
// not idempotent (an answered verification moves the slaves on), so it
// is never repeated.
func (fs *fastscan) verify(v uint32, p, next Part) (bool, error) {
	return fs.query(v, 0, p, next, 0)
}

// position resets the slaves and verifies prefix, so that the slaves
// whose address starts with prefix wait for part len(prefix). ok is
// false when no slave answers.
func (fs *fastscan) position(prefix []uint32) (bool, error) {
	for attempt := 0; attempt <= fs.retries+1; attempt++ {
		if attempt > 0 {
			fs.s.m.count(func(st *MasterStats) { st.Restarts++ })
		}
		ok, err := fs.reset()
		if err != nil || !ok {
			return false, err
		}
		done := true
		for i, v := range prefix {
			ok, err := fs.verify(v, Part(i), Part(i+1))
			if err != nil {
				return false, err
			}
			if !ok {
				done = false
				break
			}
		}
		if done {
			return true, nil
		}
	}
	return false, nil
}

// scanPart finds the values of part p among the slaves waiting for it:
// all of them, or one (usually the lowest) with first.
//
// Listing decides each bit by explicit acknowledgements (both values are
// queried, silence is repeated), and confirms each value with a second
// full-match query, so that a lost frame can hide a slave but never
// invent one. With first it queries 0, then 1, and takes the first value
// acknowledged: whichever it takes, a slave has that prefix.
func (fs *fastscan) scanPart(p Part, first bool) ([]uint32, error) {
	if v, ok := fs.known[p]; ok {
		ack, err := fs.query(v, 0, p, p, fs.retries)
		if err != nil || !ack {
			return nil, err
		}
		return []uint32{v}, nil
	}
	if first {
		return fs.scanOne(p)
	}
	var out []uint32
	var walk func(value uint32, k uint8) error
	walk = func(value uint32, k uint8) error {
		if k == 0 {
			ack, err := fs.query(value, 0, p, p, fs.retries)
			if ack {
				out = append(out, value)
			}
			return err
		}
		b := k - 1
		for _, bit := range [2]uint32{0, 1} {
			v := value | bit<<b
			ack, err := fs.query(v, b, p, p, fs.retries)
			if err != nil {
				return err
			}
			if ack {
				if err := walk(v, b); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(0, 32)
	return out, err
}

// scanOne finds one value of part p bit by bit, from bit 31 down.
func (fs *fastscan) scanOne(p Part) ([]uint32, error) {
	var value uint32
	for b := 31; b >= 0; b-- {
		found := false
		for try := 0; try <= fs.retries && !found; try++ {
			for _, bit := range [2]uint32{0, 1} {
				v := value | bit<<b
				ack, err := fs.query(v, uint8(b), p, p, 0)
				if err != nil {
					return nil, err
				}
				if ack {
					value, found = v, true
					break
				}
			}
		}
		if !found {
			return nil, nil
		}
	}
	return []uint32{value}, nil
}

// Fastscan lists the LSS addresses of every unconfigured slave (active
// and pending node-ID Unconfigured) in the waiting state, without
// selecting or changing any: it walks the address tree with Fastscan
// queries whose LSSNext keeps the slaves on the part being scanned, and
// re-positions the slaves (reset, then verification of the known prefix)
// for each part. The result is sorted by address.
//
// Each bit costs two queries, each at least one listening window, so a
// device costs about 260 windows (6.5 s with the default 25 ms). A lost
// frame can hide a device (the next Fastscan finds it); it cannot invent
// one. To give unconfigured devices node-IDs, use [Master.AssignAll].
func (m *Master) Fastscan(ctx context.Context, o FastscanOptions) ([]Address, error) {
	s, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer s.end()
	fs := m.newFastscan(s, o)
	var out []Address
	full := func() bool { return o.Max > 0 && len(out) >= o.Max }
	var walk func(prefix []uint32) error
	walk = func(prefix []uint32) error {
		p := Part(len(prefix))
		ok, err := fs.position(prefix)
		if err != nil || !ok {
			return err
		}
		vals, err := fs.scanPart(p, false)
		if err != nil {
			return err
		}
		for _, v := range vals {
			if full() {
				return nil
			}
			next := append(append([]uint32(nil), prefix...), v)
			if p == PartSerial {
				out = append(out, Address{Vendor: next[0], Product: next[1], Revision: next[2], Serial: next[3]})
				continue
			}
			if err := walk(next); err != nil {
				return err
			}
		}
		return nil
	}
	err = walk(nil)
	return out, err
}

// FastscanSelect identifies one unconfigured slave (the lowest address,
// unless frames are lost) with Fastscan and leaves it in the configuration state, as CANopenNode's
// CO_LSSmaster_IdentifyFastscan and python canopen's fast_scan do. ok is
// false when no unconfigured slave answers. When frames are lost the
// identification restarts (up to Retries+3 times) and finally fails with
// ErrFastscan.
func (m *Master) FastscanSelect(ctx context.Context, o FastscanOptions) (a Address, ok bool, err error) {
	s, err := m.begin(ctx)
	if err != nil {
		return Address{}, false, err
	}
	defer s.end()
	return m.newFastscan(s, o).selectOne()
}

func (fs *fastscan) selectOne() (Address, bool, error) {
	attempts := fs.retries + fs.s.m.cfg.fsAttempts
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			fs.s.m.count(func(st *MasterStats) { st.Restarts++ })
		}
		ok, err := fs.reset()
		if err != nil || !ok {
			return Address{}, false, err
		}
		var a Address
		done := true
		for p := PartVendor; p <= PartSerial; p++ {
			vals, err := fs.scanPart(p, true)
			if err != nil {
				return Address{}, false, err
			}
			if len(vals) == 0 {
				done = false
				break
			}
			a.SetPart(p, vals[0])
			ok, err := fs.verify(vals[0], p, (p+1)%4)
			if err != nil {
				return Address{}, false, err
			}
			if !ok {
				done = false
				break
			}
		}
		if done {
			return a, true, nil
		}
	}
	return Address{}, false, fmt.Errorf("%w after %d attempts", ErrFastscan, attempts)
}

// Assignment is a node-ID [Master.AssignAll] gave a device.
type Assignment struct {
	Address Address        `json:"address"`
	NodeID  canopen.NodeID `json:"nodeId"`
}

// AssignError reports an assignment that failed after the configure
// node-ID request was sent: the device may have taken NodeID (its
// confirmation, or the store, failed), so the node-ID must be treated as
// used.
type AssignError struct {
	Address Address
	NodeID  canopen.NodeID
	Err     error
}

// Error describes the failure.
func (e *AssignError) Error() string {
	return fmt.Sprintf("lss: assign node-ID %d to %v: %v", e.NodeID, e.Address, e.Err)
}

// Unwrap returns the cause.
func (e *AssignError) Unwrap() error { return e.Err }

// AssignOption configures [Master.AssignAll].
type AssignOption func(*assignConfig)

type assignConfig struct {
	store   bool
	fs      FastscanOptions
	max     int
	avoid   map[canopen.NodeID]bool
	handler func(Assignment)
}

// AssignStore makes every device store its new node-ID (store
// configuration) before it leaves the configuration state.
func AssignStore() AssignOption { return func(c *assignConfig) { c.store = true } }

// AssignFastscan sets the Fastscan options (Max is ignored).
func AssignFastscan(o FastscanOptions) AssignOption { return func(c *assignConfig) { c.fs = o } }

// AssignMax stops after n devices (0: all).
func AssignMax(n int) AssignOption { return func(c *assignConfig) { c.max = n } }

// AssignAvoid skips node-IDs that are already in use.
func AssignAvoid(ids ...canopen.NodeID) AssignOption {
	return func(c *assignConfig) {
		for _, id := range ids {
			c.avoid[id] = true
		}
	}
}

// AssignHandler calls h after each assignment, on the caller's goroutine.
func AssignHandler(h func(Assignment)) AssignOption { return func(c *assignConfig) { c.handler = h } }

// AssignAll gives every unconfigured device a node-ID, from first upwards
// (skipping [AssignAvoid]): one by one, it identifies a device with
// Fastscan (which leaves it in the configuration state), checks that this
// device and no other is in the configuration state, configures its
// node-ID (and stores it with [AssignStore]) and switches it back to
// waiting, upon which the device applies the node-ID (NMT reset
// communication) and leaves the Fastscan population. It ends when no
// unconfigured device answers, also when the last device took the last
// free node-ID; it fails with [ErrNoNodeID] only when a device is left
// without one.
//
// A device that keeps answering Fastscan with a pending node-ID (Lely
// lely-core's slave does until its next NMT reset communication) would be
// identified again: a device is given one node-ID per run. When Fastscan
// identifies a device that was given a node-ID already, AssignAll lists
// the remaining devices with [Master.Fastscan] instead (which selects
// none, so such a device cannot hide the others) and configures each by
// switch state selective.
//
// A node-ID is never offered twice: once a configure node-ID request is
// sent, the node-ID counts as used even when no confirmation arrives (the
// call then fails with an [*AssignError] naming it). Before each identification the master switches every
// slave to waiting and checks with "inquire node-ID" that none stayed in
// the configuration state; after it, it inquires the whole address and
// requires answers from the identified device only. The assignments made
// are returned also when an error ends the run.
func (m *Master) AssignAll(ctx context.Context, first canopen.NodeID, opts ...AssignOption) ([]Assignment, error) {
	if err := first.Check(); err != nil {
		return nil, fmt.Errorf("lss: %w", err)
	}
	c := assignConfig{avoid: map[canopen.NodeID]bool{}}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	c.fs.Max = 0
	var out []Assignment
	assigned := map[Address]bool{}
	next := first
	conflicts := 0
	for c.max == 0 || len(out) < c.max {
		for next <= canopen.MaxNodeID && c.avoid[next] {
			next++
		}
		if err := m.leaveConfiguration(ctx, c.fs); err != nil {
			return out, err
		}
		a, ok, err := m.FastscanSelect(ctx, c.fs)
		if err != nil {
			return out, err
		}
		if !ok {
			break
		}
		if assigned[a] {
			// The device answers Fastscan with its pending node-ID
			// (Lely): list the others instead of selecting it again.
			if err := m.SwitchStateGlobal(ctx, Waiting); err != nil {
				return out, err
			}
			return m.assignListed(ctx, &c, out, assigned, next)
		}
		if next > canopen.MaxNodeID {
			_ = m.SwitchStateGlobal(context.WithoutCancel(ctx), Waiting)
			return out, ErrNoNodeID
		}
		alone, err := m.checkSelected(ctx, c.fs, a)
		if err != nil {
			return out, err
		}
		if !alone {
			m.count(func(st *MasterStats) { st.Conflicts++ })
			if conflicts++; conflicts > m.cfg.retries+m.cfg.fsAttempts {
				_ = m.SwitchStateGlobal(context.WithoutCancel(ctx), Waiting)
				return out, fmt.Errorf("%w: selection of %v not confirmed", ErrConflict, a)
			}
			continue
		}
		conflicts = 0
		id := next
		next++
		if err := m.assignSelected(ctx, &c, a, id); err != nil {
			return out, err
		}
		out = m.assigned(&c, out, assigned, a, id)
	}
	return out, m.leaveConfiguration(ctx, c.fs)
}

// assignSelected gives the slave in the configuration state (address a)
// the node-ID id, stores it when asked, and switches every slave back to
// waiting.
func (m *Master) assignSelected(ctx context.Context, c *assignConfig, a Address, id canopen.NodeID) error {
	err := m.SetNodeID(ctx, id)
	if err == nil && c.store {
		err = m.Store(ctx)
	}
	if werr := m.SwitchStateGlobal(context.WithoutCancel(ctx), Waiting); err == nil {
		err = werr
	}
	if err != nil {
		return &AssignError{Address: a, NodeID: id, Err: err}
	}
	return nil
}

// assigned records an assignment.
func (m *Master) assigned(c *assignConfig, out []Assignment, done map[Address]bool, a Address, id canopen.NodeID) []Assignment {
	as := Assignment{Address: a, NodeID: id}
	done[a] = true
	m.count(func(st *MasterStats) { st.Assigned++ })
	m.cfg.log.Info("lss node-ID assigned", "address", a.String(), "node", id)
	if c.handler != nil {
		c.handler(as)
	}
	return append(out, as)
}

// assignListed is the end of [Master.AssignAll] when a device keeps
// answering Fastscan after it was given a node-ID: the devices that
// answer are listed with [Master.Fastscan], and each one without a
// node-ID from this run is selected by switch state selective and given
// the next node-ID.
func (m *Master) assignListed(ctx context.Context, c *assignConfig, out []Assignment, done map[Address]bool, next canopen.NodeID) ([]Assignment, error) {
	m.cfg.log.Info("lss: a device answers Fastscan with a pending node-ID; listing the remaining devices")
	list, err := m.Fastscan(ctx, c.fs)
	if err != nil {
		return out, err
	}
	for _, a := range list {
		if done[a] {
			continue
		}
		if c.max != 0 && len(out) >= c.max {
			break
		}
		for next <= canopen.MaxNodeID && c.avoid[next] {
			next++
		}
		if next > canopen.MaxNodeID {
			return out, ErrNoNodeID
		}
		if err := m.SwitchStateSelective(ctx, a); err != nil {
			return out, err
		}
		alone, err := m.checkSelected(ctx, c.fs, a)
		if err != nil {
			return out, err
		}
		if !alone {
			m.count(func(st *MasterStats) { st.Conflicts++ })
			_ = m.SwitchStateGlobal(context.WithoutCancel(ctx), Waiting)
			return out, fmt.Errorf("%w: selection of %v not confirmed", ErrConflict, a)
		}
		id := next
		next++
		if err := m.assignSelected(ctx, c, a, id); err != nil {
			return out, err
		}
		out = m.assigned(c, out, done, a, id)
	}
	return out, m.leaveConfiguration(ctx, c.fs)
}

// leaveConfiguration switches every slave to waiting and checks, with
// "inquire node-ID", that none answers: two silent checks in a row are
// required.
func (m *Master) leaveConfiguration(ctx context.Context, o FastscanOptions) error {
	s, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer s.end()
	fs := m.newFastscan(s, o)
	silent := 0
	for round := 0; round < 2*(m.cfg.retries+m.cfg.fsAttempts); round++ {
		if silent == 0 {
			if err := s.send(frame(MasterID, CmdSwitchGlobal, byte(Waiting))); err != nil {
				return err
			}
		}
		s.drain()
		if err := s.send(frame(MasterID, CmdInquireNodeID)); err != nil {
			return err
		}
		got, err := s.listen(fs.window, CmdInquireNodeID, 0)
		if err != nil {
			return err
		}
		if len(got) > 0 {
			silent = 0
			continue
		}
		if silent++; silent == 2 {
			return nil
		}
	}
	m.count(func(st *MasterStats) { st.Conflicts++ })
	return fmt.Errorf("%w: a slave stays in the configuration state", ErrConflict)
}

// checkSelected inquires the whole address of the slave(s) in the
// configuration state and reports whether a answered alone: every answer
// must equal a, and every part must be answered.
func (m *Master) checkSelected(ctx context.Context, o FastscanOptions, a Address) (bool, error) {
	s, err := m.begin(ctx)
	if err != nil {
		return false, err
	}
	defer s.end()
	fs := m.newFastscan(s, o)
	for p := PartVendor; p <= PartSerial; p++ {
		cs := CmdInquireVendor + Command(p)
		var got []canbus.Frame
		for attempt := 0; attempt <= fs.retries && len(got) == 0; attempt++ {
			s.drain()
			if err := s.send(frame(MasterID, cs)); err != nil {
				return false, err
			}
			if got, err = s.listen(fs.window, cs, fs.window); err != nil {
				return false, err
			}
		}
		if len(got) == 0 {
			return false, nil
		}
		for i := range got {
			if u32(&got[i]) != a.Part(p) {
				return false, nil
			}
		}
	}
	return true, nil
}
