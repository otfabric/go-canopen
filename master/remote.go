// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/program"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/store"
)

// ErrProgram reports a program download whose result the node refused: a
// wrong application software identification, or a program that does not
// start.
var ErrProgram = errors.New("master: program download failed")

// timedOut reports an SDO request that got no response.
func timedOut(err error) bool { return errors.Is(err, canopen.ErrTimeout) }

// retry runs an SDO request, repeating it up to retries times while it
// times out (a node that is still resetting does not answer).
func retry(ctx context.Context, retries int, f func() error) error {
	var err error
	for i := 0; i <= retries; i++ {
		if err = f(); err == nil || !timedOut(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// uploadRetry uploads with [retry].
func uploadRetry(ctx context.Context, c remote, retries int, index uint16, sub uint8) ([]byte, error) {
	var b []byte
	err := retry(ctx, retries, func() error {
		var err error
		b, err = c.Upload(ctx, index, sub)
		return err
	})
	return b, err
}

func u32le(x uint32) []byte { return binary.LittleEndian.AppendUint32(nil, x) }

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ConfigureConcise writes entries to a node one by one, in their order,
// as a CiA 302 manager writes a concise DCF (0x1F22): unlike
// [Master.Configure] nothing is regrouped, so the entries must hold the
// sequence the node needs (see cdcf.PDOSequence). The first failure stops
// it; the error names the entry.
func (m *Master) ConfigureConcise(ctx context.Context, node canopen.NodeID, entries []Entry) error {
	c, err := m.remote(node)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if err := c.Download(ctx, e.Index, e.SubIndex, e.Data); err != nil {
			return fmt.Errorf("master: node %d: concise DCF entry %d (%04X:%02X): %w", node, i+1, e.Index, e.SubIndex, err)
		}
	}
	return nil
}

// StoreParameters asks a node to save a group of its parameters: it writes
// the signature "save" to 0x1010 sub-index group (store.All,
// store.Communication, store.Application, store.Manufacturer).
func (m *Master) StoreParameters(ctx context.Context, node canopen.NodeID, group uint8) error {
	c, err := m.remote(node)
	if err != nil {
		return err
	}
	if err := c.Download(ctx, store.IndexStore, group, u32le(store.SignatureSave)); err != nil {
		return fmt.Errorf("master: node %d: store parameters: %w", node, err)
	}
	return nil
}

// RestoreDefaults asks a node to restore the default values of a group of
// its parameters from its next reset on: it writes the signature "load" to
// 0x1011 sub-index group.
func (m *Master) RestoreDefaults(ctx context.Context, node canopen.NodeID, group uint8) error {
	c, err := m.remote(node)
	if err != nil {
		return err
	}
	if err := c.Download(ctx, store.IndexRestore, group, u32le(store.SignatureLoad)); err != nil {
		return fmt.Errorf("master: node %d: restore defaults: %w", node, err)
	}
	return nil
}

// ProgramOptions control [Master.DownloadProgram].
type ProgramOptions struct {
	// Program is the program number (sub-index of 0x1F50, 0x1F51,
	// 0x1F56, 0x1F57); default 1.
	Program uint8
	// Identification, when not 0, must equal the node's application
	// software identification 0x1F56 once the image is written, before
	// the program is started.
	Identification uint32
	// Timing gives the polling interval, the SDO retries and the bound
	// of the whole download (BootTiming.ProgramTimeout); zero fields
	// take the master's ([WithBootTiming]).
	Timing BootTiming
}

// DownloadProgram replaces the program of a node (CiA 302-3 program
// download, in the sequence of Lely lely-core's boot slave process):
//
//  1. read the program control 0x1F51; unless it reads 0 (stopped),
//     write 0 (stop program), ignoring a timeout (the node may restart
//     into its bootloader);
//  2. write 3 (clear program), repeated while it times out;
//  3. download data to the program data 0x1F50 with an SDO block
//     transfer, or a segmented one when the node refuses block transfer;
//  4. poll the flash status 0x1F57 until programming ends (bit 0
//     clear), failing with its error code (a program.FlashError);
//  5. compare 0x1F56 with opts.Identification, when set;
//  6. write 1 (start program), ignoring a timeout;
//  7. poll 0x1F51 until it reads 1 (started).
//
// The node must be pre-operational.
func (m *Master) DownloadProgram(ctx context.Context, node canopen.NodeID, data []byte, opts ProgramOptions) error {
	t := opts.Timing
	def := m.cfg.timing
	if t.PollInterval <= 0 {
		t.PollInterval = def.PollInterval
	}
	if t.ProgramTimeout <= 0 {
		t.ProgramTimeout = def.ProgramTimeout
	}
	if t.SDORetries <= 0 {
		t.SDORetries = def.SDORetries
	}
	n := opts.Program
	if n == 0 {
		n = 1
	}
	c, err := m.remote(node)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, t.ProgramTimeout)
	defer cancel()
	fail := func(step string, err error) error {
		return fmt.Errorf("master: node %d: program download: %s: %w", node, step, err)
	}
	ctrl := func(cmd program.Command) error {
		return c.Download(ctx, program.IndexControl, n, []byte{byte(cmd)})
	}
	// 1. Stop.
	if b, err := c.Upload(ctx, program.IndexControl, n); err != nil || len(b) != 1 || b[0] != byte(program.Stopped) {
		if err := ctrl(program.Stop); err != nil && !timedOut(err) {
			return fail("stop", err)
		}
	}
	// 2. Clear.
	if err := retry(ctx, t.SDORetries, func() error { return ctrl(program.Clear) }); err != nil {
		return fail("clear", err)
	}
	// 3. Program data: block transfer, segmented when refused.
	err = retry(ctx, t.SDORetries, func() error { return downloadProgramData(ctx, c, n, data, true) })
	if err != nil && !timedOut(err) && ctx.Err() == nil {
		m.log.Debug("master: block (bulk) download of the program refused, trying segmented", "node", int(node), "error", err)
		err = retry(ctx, t.SDORetries, func() error { return downloadProgramData(ctx, c, n, data, false) })
	}
	if err != nil {
		return fail("program data", err)
	}
	// 4. Flash status.
	for {
		if err := sleep(ctx, t.PollInterval); err != nil {
			return fail("flash status", err)
		}
		v, err := readUint(ctx, c, program.IndexFlashStatus, n)
		if err != nil {
			continue
		}
		st := program.FlashStatus(v)
		if st.InProgress() {
			continue
		}
		if err := st.Err(); err != nil {
			return fail("flash status", err)
		}
		break
	}
	// 5. Identification.
	if want := opts.Identification; want != 0 {
		got, err := readUint(ctx, c, program.IndexIdentification, n)
		if err != nil {
			return fail("identification", err)
		}
		if uint32(got) != want {
			return fail("identification", fmt.Errorf("%w: 0x1F56 is 0x%08X, want 0x%08X", ErrProgram, got, want))
		}
	}
	// 6. Start.
	if err := ctrl(program.Start); err != nil && !timedOut(err) {
		return fail("start", err)
	}
	// 7. Wait for the program to run.
	notStarted := func(err error) error {
		return fail("start", fmt.Errorf("%w: program not started: %w", ErrProgram, err))
	}
	for {
		if err := sleep(ctx, t.PollInterval); err != nil {
			return notStarted(err)
		}
		b, err := uploadRetry(ctx, c, t.SDORetries, program.IndexControl, n)
		if err != nil {
			if ctx.Err() != nil || !timedOut(err) {
				return notStarted(err)
			}
			continue
		}
		if len(b) == 1 && b[0] == byte(program.Started) {
			return nil
		}
	}
}

// downloadProgramData writes the program data 0x1F50: over SDO with a
// block transfer (block) or a segmented one; over USDO (WithFD) with a bulk
// transfer (block) or a segmented one.
func downloadProgramData(ctx context.Context, c remote, n uint8, data []byte, block bool) error {
	if r, ok := c.(usdoRemote); ok {
		r.bulk = block
		return r.Download(ctx, program.IndexData, n, data)
	}
	mode := sdo.BlockNever
	if block {
		mode = sdo.BlockAlways
	}
	return c.(client).c.Download(ctx, program.IndexData, n, data, sdo.UseBlockMode(mode))
}
