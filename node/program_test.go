// SPDX-License-Identifier: MIT

package node_test

import (
	"bytes"
	"errors"
	"hash/crc32"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/node"
	"github.com/otfabric/go-canopen/program"
)

// flasher is a program handler keeping the image in memory; the
// identification is the CRC-32 of the running image.
type flasher struct {
	mu        sync.Mutex
	calls     []string
	image     []byte
	running   []byte
	failWith  error
	failCtrl  error
	pending   bool
	identOver uint32
}

func (f *flasher) Control(n uint8, cmd program.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cmd.String())
	if f.failCtrl != nil {
		return f.failCtrl
	}
	if cmd == program.Start {
		f.running = f.image
	}
	return nil
}

func (f *flasher) Download(n uint8, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "download")
	if f.failWith != nil {
		return f.failWith
	}
	f.image = bytes.Clone(data)
	if f.pending {
		return program.ErrPending
	}
	return nil
}

func (f *flasher) Identification(n uint8) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identOver != 0 {
		return f.identOver
	}
	return crc32.ChecksumIEEE(f.image)
}

func (f *flasher) seq() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

var fastProgram = master.ProgramOptions{Timing: master.BootTiming{PollInterval: 5 * time.Millisecond, ProgramTimeout: 5 * time.Second}}

func TestProgramDownload(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	f := &flasher{}
	n := startNode(t, r.buses[1], nettest.Dict(t, 5, nil), node.WithProgram(program.Config{Handler: f}))
	if n.Program() == nil {
		t.Fatal("no program device")
	}
	img := bytes.Repeat([]byte("firmware"), 200)
	opts := fastProgram
	opts.Identification = crc32.ChecksumIEEE(img)
	if err := r.m.DownloadProgram(ctx, 5, img, opts); err != nil {
		t.Fatal(err)
	}
	if got := f.seq(); got != "stop,clear,download,start" {
		t.Fatalf("sequence %s", got)
	}
	if !bytes.Equal(f.running, img) {
		t.Fatal("image not running")
	}
	if n.Program().State(1) != program.Started || n.Program().FlashStatus(1) != 0 {
		t.Fatalf("state %s flash %#x", n.Program().State(1), n.Program().FlashStatus(1))
	}
	// A wrong identification stops before the start.
	f.calls = nil
	opts.Identification = 1
	if err := r.m.DownloadProgram(ctx, 5, img, opts); !errors.Is(err, master.ErrProgram) {
		t.Fatalf("identification: %v", err)
	}
	if got := f.seq(); got != "stop,clear,download" {
		t.Fatalf("sequence %s", got)
	}
	// A stopped program is not stopped again; a flash error is reported.
	f.calls = nil
	f.failWith = program.FlashDataFormatError
	if err := r.m.DownloadProgram(ctx, 5, img, fastProgram); err == nil {
		t.Fatal("flash error not reported")
	}
	if got := f.seq(); got != "clear,download,download" {
		// Block download refused (abort) -> segmented retry.
		t.Fatalf("sequence %s", got)
	}
	if st := n.Program().FlashStatus(1); st.Code() != program.FlashDataFormatError {
		t.Fatalf("flash %#x", st)
	}
	// A pending download is finished by the handler later.
	f.failWith = nil
	f.pending = true
	go func() {
		for n.Program().State(1) != program.Stopped || !n.Program().FlashStatus(1).InProgress() {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		n.Program().SetFlashStatus(1, program.NewFlashStatus(false, program.FlashOK))
	}()
	if err := r.m.DownloadProgram(ctx, 5, img, fastProgram); err != nil {
		t.Fatal(err)
	}
	// Control refused outside Pre-operational.
	f.pending = false
	_ = n.SetState(canopen.StateOperational)
	if err := r.m.DownloadProgram(ctx, 5, img, fastProgram); !errors.Is(err, canopen.AbortDeviceState) {
		t.Fatalf("operational: %v", err)
	}
}

func TestProgramObjects(t *testing.T) {
	nettest.CheckLeaks(t)
	r := newRig(t, 1)
	ctx := nettest.Ctx(t)
	f := &flasher{identOver: 0xABCD}
	d := nettest.Dict(t, 5, nil)
	n := startNode(t, r.buses[1], d, node.WithProgram(program.Config{Handler: f, Programs: 2, Stopped: true}))
	c, _ := r.m.SDO(5)
	if v, err := c.ReadUint(ctx, program.IndexControl, 2); err != nil || v != uint64(program.Stopped) {
		t.Fatalf("initial state %d %v", v, err)
	}
	if v, err := c.ReadUint(ctx, program.IndexIdentification, 1); err != nil || v != 0xABCD {
		t.Fatalf("identification %#x %v", v, err)
	}
	for _, idx := range []uint16{program.IndexControl, program.IndexIdentification, program.IndexFlashStatus} {
		if v, err := c.ReadUint(ctx, idx, 0); err != nil || v != 2 {
			t.Fatalf("%04X:00 = %d %v", idx, v, err)
		}
	}
	tests := []struct {
		name string
		do   func() error
		want error
	}{
		{"data before clear", func() error { return c.Download(ctx, program.IndexData, 1, []byte{1}) }, canopen.AbortDeviceState},
		{"invalid command", func() error { return c.WriteUint8(ctx, program.IndexControl, 1, 9) }, canopen.AbortInvalidValue},
		{"start", func() error { return c.WriteUint8(ctx, program.IndexControl, 1, uint8(program.Start)) }, nil},
		{"clear while started", func() error { return c.WriteUint8(ctx, program.IndexControl, 1, uint8(program.Clear)) }, canopen.AbortDeviceState},
		{"reset", func() error { return c.WriteUint8(ctx, program.IndexControl, 1, uint8(program.Reset)) }, nil},
		{"read data", func() error { _, err := c.Upload(ctx, program.IndexData, 1); return err }, canopen.AbortWriteOnly},
		{"write flash status", func() error { return c.WriteUint32(ctx, program.IndexFlashStatus, 1, 0) }, canopen.AbortReadOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if st := n.Program().FlashStatus(1); st.Code() != program.FlashNotCleared {
		t.Fatalf("flash after data before clear: %#x", st)
	}
	// A handler refusing a command: its abort code, or a hardware error.
	f.failCtrl = canopen.AbortLocalControl
	if err := c.WriteUint8(ctx, program.IndexControl, 1, 0); !errors.Is(err, canopen.AbortLocalControl) {
		t.Fatal(err)
	}
	f.failCtrl = errors.New("x")
	if err := c.WriteUint8(ctx, program.IndexControl, 1, 0); !errors.Is(err, canopen.AbortHardware) {
		t.Fatal(err)
	}
	// A download error that is no flash error is "unspecified".
	f.failCtrl = nil
	_ = c.WriteUint8(ctx, program.IndexControl, 2, uint8(program.Clear))
	f.failWith = errors.New("disk full")
	if err := c.Download(ctx, program.IndexData, 2, []byte{1, 2, 3}); !errors.Is(err, canopen.AbortHardware) {
		t.Fatal(err)
	}
	if st := n.Program().FlashStatus(2); st.Code() != program.FlashUnspecified || st.Err() == nil {
		t.Fatalf("flash %#x", st)
	}
	n.Program().SetState(2, program.Started)
	if n.Program().State(2) != program.Started {
		t.Fatal("SetState")
	}
}

func TestProgramTypes(t *testing.T) {
	tests := []struct {
		s    interface{ String() string }
		want string
	}{
		{program.Stop, "stop"}, {program.Start, "start"}, {program.Reset, "reset"}, {program.Clear, "clear"},
		{program.Command(9), "Command(9)"},
		{program.Stopped, "stopped"}, {program.Started, "started"}, {program.Cleared, "cleared"},
		{program.State(7), "State(7)"},
	}
	for _, tc := range tests {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("%v: %q", tc.want, got)
		}
	}
	for _, e := range []program.FlashError{0, 1, 2, 3, 4, 5, 6, 7, 63, 20, 100} {
		if e.Error() == "" {
			t.Errorf("%d: empty", e)
		}
	}
	st := program.NewFlashStatus(true, program.FlashWriteError)
	if !st.InProgress() || st.Code() != program.FlashWriteError || st.Err() != nil {
		t.Fatalf("%#x", st)
	}
	if err := program.NewFlashStatus(false, program.FlashWriteError).Err(); !errors.Is(err, program.FlashWriteError) {
		t.Fatal(err)
	}
	if _, err := program.Attach(nil, program.Config{}); err == nil {
		t.Fatal("Attach without dictionary")
	}
	if err := program.AddObjects(nettest.Dict(t, 5, nil), 0); err == nil {
		t.Fatal("0 programs")
	}
	d := nettest.Dict(t, 5, nil)
	if err := program.AddObjects(d, 1); err != nil {
		t.Fatal(err)
	}
	dev, err := program.Attach(d, program.Config{Handler: &flasher{}})
	if err != nil {
		t.Fatal(err)
	}
	if d.HookCount() == 0 {
		t.Fatal("no hooks")
	}
	dev.Close()
	if d.HookCount() != 0 {
		t.Fatal("hooks left")
	}
	if _, err := node.New(nil, nil); err == nil {
		t.Fatal("node without bus")
	}
	if _, err := node.New(newRig(t, 1).buses[1], nettest.Dict(t, 5, nil), node.WithProgram(program.Config{Programs: 300, Handler: &flasher{}})); err == nil {
		t.Fatal("300 programs")
	}
}
