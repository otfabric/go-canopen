// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

func TestServerLifecycle(t *testing.T) {
	checkLeaks(t)
	a, _ := testbus.Pair(t)
	d := testDict(t)
	if _, err := NewServer(nil, d); err == nil {
		t.Error("nil bus accepted")
	}
	if _, err := NewServer(a, nil); err == nil {
		t.Error("nil dictionary accepted")
	}
	for _, o := range []ServerOption{WithTimeout(-1), WithBlockSize(0), WithMaxSize(-5)} {
		if _, err := NewServer(a, d, o); err == nil {
			t.Error("option accepted")
		}
	}
	s, err := NewServer(a, d, WithLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if rx, tx, err := s.COBIDs(); err != nil || rx != 0x605 || tx != 0x585 {
		t.Errorf("COBIDs = %v %v %v", rx, tx, err)
	}
	if err := s.Close(); err != nil { // never started
		t.Fatal(err)
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Errorf("start after close: %v", err)
	}
	s, _ = NewServer(a, d)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Errorf("second start: %v", err)
	}
	if rx, _, _ := s.COBIDs(); rx != 0x605 {
		t.Errorf("started COBIDs %v", rx)
	}
	_ = s.Close()
	_ = s.Close()
}

func TestServerCOBIDs(t *testing.T) {
	checkLeaks(t)
	a, b := testbus.Pair(t)
	ctx := ctxT(t)
	// Without object 0x1200: the dictionary's node-ID.
	d := od.New(7)
	_ = d.Add(od.NewVar(0x1000, "device type", canopen.Unsigned32, od.AccessRO, uint32(0x1234)))
	startServer(t, b, d)
	c, _ := NewClient(a, 7)
	if v, err := c.ReadUint(ctx, 0x1000, 0); err != nil || v != 0x1234 {
		t.Fatalf("ReadUint = %x, %v", v, err)
	}
	// No node-ID and no 0x1200.
	s, _ := NewServer(b, od.New(0))
	if err := s.Start(); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Errorf("node 0: %v", err)
	}
	if _, _, err := s.COBIDs(); err == nil {
		t.Error("COBIDs without node-ID")
	}
	// An invalid COB-ID in 0x1200.
	d = testDict(t)
	_ = d.Set(od.IndexSDOServer, 1, []byte{0x05, 0x06, 0, 0x80})
	s, _ = NewServer(b, d)
	if err := s.Start(); err == nil {
		t.Error("invalid COB-ID accepted")
	}
	// 29-bit COB-IDs from the option on both sides.
	rx, tx := canopen.NewCOBID(0x1ABCDE00), canopen.NewCOBID(0x1ABCDE01)
	startServer(t, b, testDict(t), WithCOBIDs(rx, tx))
	c, _ = NewClient(a, 0, WithCOBIDs(rx, tx), WithBlockMode(BlockAuto))
	want := pattern(300)
	if err := c.Download(ctx, idxDomain, 0, want); err != nil {
		t.Fatal(err)
	}
	got, err := c.Upload(ctx, idxDomain, 0)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, got, want)
}

func TestServerTimeout(t *testing.T) {
	checkLeaks(t)
	a, b := testbus.Pair(t)
	s := startServer(t, b, testDict(t), WithTimeout(40*time.Millisecond))
	p := newPeer(t, a, canopen.NewCOBID(0x585), canopen.NewCOBID(0x605))
	// Segmented download, then silence.
	p.send(muxFrame(0x21, idxDomain, 0, 20))
	if f := p.recv(time.Second); f != muxFrame(0x60, idxDomain, 0, 0) {
		t.Fatalf("response % X", f)
	}
	start := time.Now()
	if f := p.recv(time.Second); f != abortOf(idxDomain, canopen.AbortTimeout) {
		t.Fatalf("got % X, want timeout abort", f)
	}
	if el := time.Since(start); el < 30*time.Millisecond {
		t.Errorf("abort after %v", el)
	}
	// Block download: one segment, then silence. The server repeats its
	// acknowledgement, then times out.
	p.send(blkInitDL(idxDomain, 100, true))
	p.recv(time.Second)
	p.send(seg(1))
	if f := p.recv(time.Second); f != (frame{0xA2, 1, 127}) {
		t.Fatalf("got % X, want repeated ack", f)
	}
	for {
		f := p.recv(time.Second)
		if f == abortOf(idxDomain, canopen.AbortTimeout) {
			break
		}
		if f != (frame{0xA2, 1, 127}) {
			t.Fatalf("got % X", f)
		}
	}
	if st := s.Stats(); st.Timeouts != 2 || st.Reacks == 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestServerDisabledMidTransfer(t *testing.T) {
	a, b := testbus.Pair(t)
	s := startServer(t, b, testDict(t), WithTimeout(80*time.Millisecond))
	p := newPeer(t, a, canopen.NewCOBID(0x585), canopen.NewCOBID(0x605))
	p.send(muxFrame(0x21, idxDomain, 0, 20))
	p.recv(time.Second)
	s.SetEnabled(false)
	// No abort for the dropped transfer, no answer to requests.
	if f, ok := p.tryRecv(150 * time.Millisecond); ok {
		t.Fatalf("disabled server sent % X", f)
	}
	p.send(seg(0x00))
	if f, ok := p.tryRecv(50 * time.Millisecond); ok {
		t.Fatalf("disabled server sent % X", f)
	}
	s.SetEnabled(true)
	p.send(seg(0x10)) // the transfer is gone
	if f := p.recv(time.Second); f != abortOf(0, canopen.AbortCommand) {
		t.Fatalf("got % X", f)
	}
	// Disabled while the transfer is idle: the timer drops it silently.
	p.send(muxFrame(0x21, idxDomain, 0, 20))
	p.recv(time.Second)
	time.Sleep(5 * time.Millisecond)
	s.SetEnabled(false)
	if f, ok := p.tryRecv(150 * time.Millisecond); ok {
		t.Fatalf("disabled server sent % X", f)
	}
}

func TestServerRemoteFrames(t *testing.T) {
	a, b := testbus.Pair(t)
	s := startServer(t, b, testDict(t))
	rtr := canFrame(canopen.NewCOBID(0x605), frame{})
	rtr.Flags |= canbus.FlagRemote
	rtr.Len = 0
	if err := a.Send(ctxT(t), &rtr); err == nil {
		time.Sleep(20 * time.Millisecond)
	}
	if s.Stats().Requests != 0 {
		t.Errorf("remote frame handled: %+v", s.Stats())
	}
}

func TestServerBusClosed(t *testing.T) {
	checkLeaks(t)
	_, b := testbus.Pair(t)
	s := startServer(t, b, testDict(t))
	_ = b.Close() // the server's loop ends with its subscription
	time.Sleep(10 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestServerResetFence: with WithResetFence the server disables itself at
// an NMT reset command for its node or for all nodes, in the order the
// frames arrive; other NMT commands and other nodes' resets change
// nothing.
func TestServerResetFence(t *testing.T) {
	a, b := testbus.Pair(t)
	s := startServer(t, b, testDict(t), WithResetFence(testNode))
	p := newPeer(t, a, canopen.NewCOBID(0x585), canopen.NewCOBID(0x605))
	nmtFrame := func(data ...byte) {
		t.Helper()
		f, err := canbus.NewStandard(canopen.FuncNMT, data)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Send(ctxT(t), &f); err != nil {
			t.Fatal(err)
		}
	}
	upload := func() bool {
		t.Helper()
		p.send(muxFrame(0x40, 0x1000, 0, 0))
		_, ok := p.tryRecv(100 * time.Millisecond)
		return ok
	}
	nmtFrame(byte(canopen.NMTStart), 0)                             // not a reset
	nmtFrame(byte(canopen.NMTResetCommunication), byte(testNode+1)) // another node
	nmtFrame(byte(canopen.NMTResetNode))                            // too short
	if !upload() || !s.Enabled() {
		t.Fatal("disabled by a command that is not a reset of its node")
	}
	// The command and a request right after it: the request is not
	// answered.
	nmtFrame(byte(canopen.NMTResetCommunication), byte(testNode))
	if upload() || s.Enabled() {
		t.Fatal("answered after a reset communication of its node")
	}
	s.SetEnabled(true)
	nmtFrame(byte(canopen.NMTResetNode), 0)
	if upload() || s.Enabled() {
		t.Fatal("answered after a reset node of all nodes")
	}
	// Without the option NMT commands do not reach the server.
	c, d := testbus.Pair(t)
	s2 := startServer(t, d, testDict(t))
	f, _ := canbus.NewStandard(canopen.FuncNMT, []byte{byte(canopen.NMTResetNode), 0})
	if err := c.Send(ctxT(t), &f); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if !s2.Enabled() {
		t.Fatal("disabled without WithResetFence")
	}
}
