// SPDX-License-Identifier: MIT

package sdo

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestCRC16(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want uint16
	}{
		{"", 0},
		{"123456789", 0x31C3}, // CRC-16/XMODEM check value
		{"A", 0x58E5},
	} {
		if got := CRC16([]byte(tc.in)); got != tc.want {
			t.Errorf("CRC16(%q) = 0x%04X, want 0x%04X", tc.in, got, tc.want)
		}
	}
}

var transferSizes = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 13, 14, 15, 20, 21, 27, 28, 29, 34, 35, 36,
	7*127 - 1, 7 * 127, 7*127 + 1, 2 * 7 * 127, 2000, 5000}

// TestTransfers downloads and uploads every size in every mode.
func TestTransfers(t *testing.T) {
	checkLeaks(t)
	modes := []struct {
		name  string
		sopts []ServerOption
		copts []ClientOption
	}{
		{"segmented", nil, nil},
		{"block-crc", nil, []ClientOption{WithBlockMode(BlockAlways)}},
		{"block-nocrc-client", nil, []ClientOption{WithBlockMode(BlockAlways), WithCRC(false)}},
		{"block-nocrc-server", []ServerOption{WithCRC(false)}, []ClientOption{WithBlockMode(BlockAlways)}},
		{"block-size-5", []ServerOption{WithBlockSize(5)}, []ClientOption{WithBlockMode(BlockAlways), WithBlockSize(4)}},
		{"block-size-1", []ServerOption{WithBlockSize(1)}, []ClientOption{WithBlockMode(BlockAlways), WithBlockSize(1)}},
		{"auto", nil, []ClientOption{WithBlockMode(BlockAuto), WithBlockThreshold(20)}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			c, s, d := setup(t, m.sopts, m.copts...)
			ctx := ctxT(t)
			for _, n := range transferSizes {
				want := pattern(n)
				if err := c.Download(ctx, idxDomain, 0, want); err != nil {
					t.Fatalf("download %d bytes: %v", n, err)
				}
				stored, _ := d.Get(idxDomain, 0)
				equal(t, stored, want)
				got, err := c.Upload(ctx, idxDomain, 0)
				if err != nil {
					t.Fatalf("upload %d bytes: %v", n, err)
				}
				equal(t, got, want)
			}
			// The server counts a block upload when the client's last
			// confirmation arrives, after the client returned.
			eventually(t, func() bool { return s.Stats().Uploads == uint64(len(transferSizes)) })
			ss, cs := s.Stats(), c.Stats()
			if ss.Uploads != uint64(len(transferSizes)) || ss.Downloads != uint64(len(transferSizes)) {
				t.Errorf("server stats %+v", ss)
			}
			if cs.Uploads != ss.Uploads || cs.Downloads != ss.Downloads || cs.BytesUploaded != ss.BytesUploaded ||
				cs.BytesDownloaded != ss.BytesDownloaded || cs.Failed != 0 {
				t.Errorf("client stats %+v, server %+v", cs, ss)
			}
			if ss.AbortsSent != 0 || cs.AbortsSent != 0 {
				t.Errorf("aborts: server %d, client %d", ss.AbortsSent, cs.AbortsSent)
			}
		})
	}
}

func TestBlockModeSelection(t *testing.T) {
	c, s, _ := setup(t, nil, WithBlockMode(BlockAuto), WithBlockThreshold(32))
	ctx := ctxT(t)
	// Downloads: block from 32 bytes.
	for _, n := range []int{31, 32} {
		before := s.Stats().BlockTransfers
		if err := c.Download(ctx, idxDomain, 0, pattern(n)); err != nil {
			t.Fatal(err)
		}
		if got, want := s.Stats().BlockTransfers-before, uint64(n/32); got != want {
			t.Errorf("%d bytes: %d block transfers, want %d", n, got, want)
		}
	}
	// Uploads: pst 31, so 31 bytes switch, 32 do not.
	for _, n := range []int{31, 32} {
		if err := c.Download(ctx, idxDomain, 0, pattern(n), UseBlockMode(BlockNever)); err != nil {
			t.Fatal(err)
		}
		before := s.Stats()
		got, err := c.Upload(ctx, idxDomain, 0)
		if err != nil {
			t.Fatal(err)
		}
		equal(t, got, pattern(n))
		after := s.Stats()
		if n == 31 && after.ProtocolSwitch != before.ProtocolSwitch+1 {
			t.Errorf("31 bytes: no protocol switch")
		}
		if n == 32 && after.BlockTransfers != before.BlockTransfers+1 {
			t.Errorf("32 bytes: no block upload")
		}
	}
	// Per-call override and expedited protocol switch.
	before := s.Stats()
	if v, err := c.ReadUint(ctx, 0x1000, 0); err != nil || v != 0x00020192 {
		t.Fatalf("ReadUint = %x, %v", v, err)
	}
	if s.Stats().ProtocolSwitch != before.ProtocolSwitch+1 {
		t.Error("expedited upload did not switch")
	}
	if _, err := c.Upload(ctx, idxString, 0, UseBlockMode(BlockNever)); err != nil {
		t.Fatal(err)
	}
	if s.Stats().ProtocolSwitch != before.ProtocolSwitch+1 || s.Stats().BlockTransfers != before.BlockTransfers {
		t.Error("BlockNever override used block transfer")
	}
}

func TestProtocolSwitchOption(t *testing.T) {
	c, s, _ := setup(t, nil, WithBlockMode(BlockAlways), WithProtocolSwitch(40))
	got, err := c.Upload(ctxT(t), idxString, 0)
	if err != nil || string(got) != testString {
		t.Fatalf("upload = %q, %v", got, err)
	}
	if st := s.Stats(); st.ProtocolSwitch != 1 || st.BlockTransfers != 0 {
		t.Errorf("stats %+v", st)
	}
	if c.pst(BlockAlways) != 40 {
		t.Error("pst")
	}
	c2 := &Client{cfg: defaultConfig()}
	if c2.pst(BlockAlways) != 0 || c2.pst(BlockAuto) != DefaultBlockThreshold-1 {
		t.Error("derived pst")
	}
	c2.cfg.threshold = 1000
	if c2.pst(BlockAuto) != 255 {
		t.Error("pst cap")
	}
}

// TestAbortPropagation checks that the dictionary's refusals reach the
// client as remote aborts, in every transfer type.
func TestAbortPropagation(t *testing.T) {
	checkLeaks(t)
	c, s, d := setup(t, []ServerOption{WithMaxSize(100)}, WithMaxSize(1000))
	ctx := ctxT(t)
	big := pattern(200)
	if err := d.Set(idxDomain, 0, big); err != nil {
		t.Fatal(err)
	}
	type op struct {
		name string
		fn   func(mode BlockMode) error
		code canopen.AbortCode
	}
	up := func(idx uint16, sub uint8) func(BlockMode) error {
		return func(m BlockMode) error { _, err := c.Upload(ctx, idx, sub, UseBlockMode(m)); return err }
	}
	down := func(idx uint16, sub uint8, data []byte) func(BlockMode) error {
		return func(m BlockMode) error { return c.Download(ctx, idx, sub, data, UseBlockMode(m)) }
	}
	ops := []op{
		{"upload no object", up(0x3000, 0), canopen.AbortNoObject},
		{"upload no sub-index", up(od.IndexIdentity, 9), canopen.AbortNoSubIndex},
		{"upload write-only", up(idxWO, 0), canopen.AbortWriteOnly},
		{"upload hook", up(idxHook, 0), canopen.AbortGeneral},
		{"download no object", down(0x3000, 0, []byte{1}), canopen.AbortNoObject},
		{"download no sub-index", down(idxLimited, 1, []byte{1}), canopen.AbortNoSubIndex},
		{"download read-only", down(idxString, 0, []byte("x")), canopen.AbortReadOnly},
		{"download read-only long", down(idxString, 0, pattern(50)), canopen.AbortReadOnly},
		{"download too short", down(idxLimited, 0, []byte{1, 2, 3}), canopen.AbortLengthLow},
		{"download too long", down(idxLimited, 0, []byte{1, 2, 3, 4, 5}), canopen.AbortLengthHigh},
		{"download value low", down(idxLimited, 0, []byte{5, 0, 0, 0}), canopen.AbortValueLow},
		{"download value high", down(idxLimited, 0, []byte{0xE9, 3, 0, 0}), canopen.AbortValueHigh},
		{"download over MaxSize", down(idxSmallDom, 0, pattern(17)), canopen.AbortLengthHigh},
		{"download hook", down(idxHook, 0, pattern(10)), canopen.AbortGeneral},
		{"download beyond server limit", down(idxDomain, 0, pattern(101)), canopen.AbortOutOfMemory},
	}
	for _, mode := range []BlockMode{BlockNever, BlockAlways} {
		for _, o := range ops {
			t.Run(fmt.Sprintf("%s/%s", mode, o.name), func(t *testing.T) {
				wantAbort(t, o.fn(mode), o.code, true)
			})
		}
	}
	// The values are unchanged and the channel still works.
	if v, err := c.ReadUint(ctx, idxLimited, 0); err != nil || v != 100 {
		t.Fatalf("limited = %d, %v", v, err)
	}
	got, err := c.Upload(ctx, idxDomain, 0)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got, big)
	if st := c.Stats(); st.AbortsReceived != uint64(2*len(ops)) || st.Failed != uint64(2*len(ops)) {
		t.Errorf("client stats %+v", st)
	}
	if st := s.Stats(); st.AbortsSent != uint64(2*len(ops)) {
		t.Errorf("server stats %+v", st)
	}
}

func TestClientMaxSize(t *testing.T) {
	c, _, d := setup(t, nil, WithMaxSize(10))
	ctx := ctxT(t)
	_ = d.Set(idxDomain, 0, pattern(11))
	for _, m := range []BlockMode{BlockNever, BlockAlways} {
		_, err := c.Upload(ctx, idxDomain, 0, UseBlockMode(m))
		wantAbort(t, err, canopen.AbortOutOfMemory, false)
		if !errors.Is(err, canopen.ErrProtocol) {
			t.Errorf("%v: not ErrProtocol", err)
		}
	}
	_ = d.Set(idxDomain, 0, pattern(10))
	if _, err := c.Upload(ctx, idxDomain, 0, UseBlockMode(BlockAlways)); err != nil {
		t.Fatal(err)
	}
}

func TestTyped(t *testing.T) {
	c, _, d := setup(t, nil)
	ctx := ctxT(t)
	if s, err := c.ReadString(ctx, idxString, 0); err != nil || s != testString {
		t.Fatalf("ReadString = %q, %v", s, err)
	}
	if v, err := c.ReadInt(ctx, idxI16, 0); err != nil || v != -2 {
		t.Fatalf("ReadInt = %d, %v", v, err)
	}
	if err := c.WriteUint8(ctx, idxU8, 0, 200); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(idxU8, 0); v != 200 {
		t.Errorf("u8 = %d", v)
	}
	if err := c.WriteUint32(ctx, idxLimited, 0, 999); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteUint(ctx, idxLimited, 0, 500, 4); err != nil {
		t.Fatal(err)
	}
	if v, err := c.ReadUint(ctx, idxLimited, 0); err != nil || v != 500 {
		t.Fatalf("ReadUint = %d, %v", v, err)
	}
	if err := c.WriteUint16(ctx, idxWO, 0, 0xBEEF); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(idxWO, 0); v != 0xBEEF {
		t.Errorf("wo = %x", v)
	}
	if err := c.WriteValue(ctx, idxReal, 0, canopen.Real32, float32(-3.25)); err != nil {
		t.Fatal(err)
	}
	if v, err := c.ReadValue(ctx, idxReal, 0, canopen.Real32); err != nil || v != float32(-3.25) {
		t.Fatalf("ReadValue = %v, %v", v, err)
	}
	if err := c.WriteValue(ctx, idxI16, 0, canopen.Integer16, -300); err != nil {
		t.Fatal(err)
	}
	if v, err := c.ReadInt(ctx, idxI16, 0); err != nil || v != -300 {
		t.Fatalf("ReadInt = %d, %v", v, err)
	}
	// Errors.
	_ = d.Set(idxDomain, 0, nil)
	if _, err := c.ReadUint(ctx, idxDomain, 0); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("ReadUint of empty: %v", err)
	}
	if _, err := c.ReadInt(ctx, idxDomain, 0); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("ReadInt of empty: %v", err)
	}
	if _, err := c.ReadValue(ctx, idxString, 0, canopen.Unsigned8); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("ReadValue mismatch: %v", err)
	}
	if err := c.WriteUint(ctx, idxU8, 0, 1, 9); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("WriteUint size 9: %v", err)
	}
	if err := c.WriteUint(ctx, idxU8, 0, 256, 1); !errors.Is(err, canopen.ErrDataType) {
		t.Errorf("WriteUint out of range: %v", err)
	}
	for _, f := range []func() error{
		func() error { _, err := c.ReadUint(ctx, 0x3000, 0); return err },
		func() error { _, err := c.ReadInt(ctx, 0x3000, 0); return err },
		func() error { _, err := c.ReadString(ctx, 0x3000, 0); return err },
	} {
		wantAbort(t, f(), canopen.AbortNoObject, true)
	}
}

func TestConcurrentClients(t *testing.T) {
	checkLeaks(t)
	ports := testbus.Ports(t, 4)
	ctx := ctxT(t)
	var clients []*Client
	for i := 1; i <= 3; i++ {
		d := testDict(t)
		d.SetNodeID(canopen.NodeID(i))
		d.Reset(0x1000, 0x1FFF)
		startServer(t, ports[i], d)
		c, err := NewClient(ports[0], canopen.NodeID(i), WithBlockMode(BlockAuto))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		clients = append(clients, c)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i, c := range clients {
		for g := 0; g < 3; g++ { // several goroutines share each client
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < 5; k++ {
					want := pattern(100*i + 37*g + k)
					if err := c.Download(ctx, idxDomain, 0, want); err != nil {
						errs <- err
						return
					}
					got, err := c.Upload(ctx, idxDomain, 0)
					if err != nil {
						errs <- err
						return
					}
					// Another goroutine of the same client may have
					// written in between; the length tells the writers
					// apart, the content must match its pattern.
					equalPatternOK(errs, got)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i, c := range clients {
		if v, err := c.ReadUint(ctx, od.IndexSDOServer, 1); err != nil || v != 0x600+uint64(i+1) {
			t.Errorf("node %d: COB-ID %x, %v", i+1, v, err)
		}
	}
}

func equalPatternOK(errs chan<- error, got []byte) {
	want := pattern(len(got))
	for i := range got {
		if got[i] != want[i] {
			errs <- fmt.Errorf("corrupt data of %d bytes at %d", len(got), i)
			return
		}
	}
}

func TestClientTimeout(t *testing.T) {
	checkLeaks(t)
	a, b := testbus.Pair(t)
	obs := newPeer(t, b, canopen.NewCOBID(0x605), canopen.NewCOBID(0x585))
	c := newClient(t, a, WithTimeout(30*time.Millisecond), WithRetries(2))
	start := time.Now()
	_, err := c.Upload(ctxT(t), 0x1000, 0)
	if !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	wantAbort(t, err, canopen.AbortTimeout, false)
	if el := time.Since(start); el < 90*time.Millisecond {
		t.Errorf("3 attempts took only %v", el)
	}
	// Three initiates, each followed by an abort.
	for i := 0; i < 3; i++ {
		if f := obs.recv(time.Second); f[0] != ccsInitiateUpload<<5 {
			t.Fatalf("frame %d: % X, want initiate", 2*i, f)
		}
		f := obs.recv(time.Second)
		if f[0] != abortByte || canopen.AbortCode(f.u32()) != canopen.AbortTimeout {
			t.Fatalf("frame %d: % X, want timeout abort", 2*i+1, f)
		}
		if idx, sub := f.mux(); idx != 0x1000 || sub != 0 {
			t.Errorf("abort for %04X:%02X", idx, sub)
		}
	}
	if st := c.Stats(); st.Retries != 2 || st.Timeouts != 3 || st.AbortsSent != 3 || st.Failed != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestServerDisabled(t *testing.T) {
	c, s, _ := setup(t, nil, WithTimeout(30*time.Millisecond))
	ctx := ctxT(t)
	s.SetEnabled(false)
	if s.Enabled() {
		t.Fatal("still enabled")
	}
	if _, err := c.Upload(ctx, 0x1000, 0); !errors.Is(err, canopen.ErrTimeout) {
		t.Fatalf("disabled: %v", err)
	}
	s.SetEnabled(true)
	if _, err := c.Upload(ctx, 0x1000, 0); err != nil {
		t.Fatalf("enabled again: %v", err)
	}
	if s.Stats().Ignored == 0 {
		t.Error("no ignored requests counted")
	}
}
