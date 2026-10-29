// SPDX-License-Identifier: MIT

package nmt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/testbus"
)

func TestCommandFrame(t *testing.T) {
	for _, tc := range []struct {
		node canopen.NodeID
		cmd  canopen.NMTCommand
	}{
		{0, canopen.NMTStart}, {1, canopen.NMTStop}, {127, canopen.NMTEnterPreOperational},
		{5, canopen.NMTResetNode}, {9, canopen.NMTResetCommunication},
	} {
		f := CommandFrame(tc.node, tc.cmd)
		cmd, node, err := ParseCommand(&f)
		if err != nil || cmd != tc.cmd || node != tc.node {
			t.Errorf("%v %d: got %v %d %v", tc.cmd, tc.node, cmd, node, err)
		}
	}
}

func TestParseCommandInvalid(t *testing.T) {
	ok := CommandFrame(1, canopen.NMTStart)
	mk := func(mut func(*canbus.Frame)) canbus.Frame { f := ok; mut(&f); return f }
	for name, f := range map[string]canbus.Frame{
		"id":       mk(func(f *canbus.Frame) { f.ID = 1 }),
		"extended": mk(func(f *canbus.Frame) { f.Flags |= canbus.FlagExtended }),
		"remote":   mk(func(f *canbus.Frame) { f.Flags |= canbus.FlagRemote }),
		"short":    mk(func(f *canbus.Frame) { f.Len = 1 }),
		"long":     mk(func(f *canbus.Frame) { f.Len = 3 }),
		"command":  mk(func(f *canbus.Frame) { f.Data[0] = 0x03 }),
		"node":     mk(func(f *canbus.Frame) { f.Data[1] = 128 }),
	} {
		if _, _, err := ParseCommand(&f); !errors.Is(err, canopen.ErrProtocol) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestErrorControlFrame(t *testing.T) {
	for _, tc := range []struct {
		node   canopen.NodeID
		state  canopen.NMTState
		toggle bool
	}{
		{1, canopen.StateInitialising, false}, {2, canopen.StateStopped, true},
		{3, canopen.StateOperational, false}, {127, canopen.StatePreOperational, true},
	} {
		f := ErrorControlFrame(tc.node, tc.state, tc.toggle)
		if f.ID != 0x700+canbus.ID(tc.node) {
			t.Errorf("id 0x%X", f.ID)
		}
		node, st, tg, err := ParseErrorControl(&f)
		if err != nil || node != tc.node || st != tc.state || tg != tc.toggle {
			t.Errorf("%+v: got %d %v %v %v", tc, node, st, tg, err)
		}
	}
	r := GuardRequest(5)
	if r.ID != 0x705 || r.Flags&canbus.FlagRemote == 0 || r.Len != 1 {
		t.Errorf("guard request %+v", r)
	}
}

func TestParseErrorControlInvalid(t *testing.T) {
	ok := ErrorControlFrame(1, canopen.StateOperational, false)
	mk := func(mut func(*canbus.Frame)) canbus.Frame { f := ok; mut(&f); return f }
	for name, f := range map[string]canbus.Frame{
		"id":     mk(func(f *canbus.Frame) { f.ID = 0x181 }),
		"node0":  mk(func(f *canbus.Frame) { f.ID = 0x700 }),
		"remote": mk(func(f *canbus.Frame) { f.Flags |= canbus.FlagRemote }),
		"len":    mk(func(f *canbus.Frame) { f.Len = 2 }),
		"empty":  mk(func(f *canbus.Frame) { f.Len = 0 }),
		"state":  mk(func(f *canbus.Frame) { f.Data[0] = 0x06 }),
	} {
		if _, _, _, err := ParseErrorControl(&f); !errors.Is(err, canopen.ErrProtocol) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestConsumerEntry(t *testing.T) {
	e := ConsumerEntry(5, 250*time.Millisecond)
	if e != 0x000500FA {
		t.Fatalf("entry 0x%08X", e)
	}
	n, d, ok := ParseConsumerEntry(e)
	if n != 5 || d != 250*time.Millisecond || !ok {
		t.Fatal(n, d, ok)
	}
	if ConsumerEntry(1, time.Hour)&0xFFFF != 0xFFFF {
		t.Error("clamp")
	}
	for _, e := range []uint32{0, 0x000000FA, 0x00800064, 0x00050000} {
		if _, _, ok := ParseConsumerEntry(e); ok {
			t.Errorf("0x%08X valid", e)
		}
	}
}

func TestStrings(t *testing.T) {
	for _, s := range []string{
		ResetApplication.String(), ResetCommunication.String(), ResetKind(9).String(),
		EventBootUp.String(), EventStateChange.String(), EventTimeout.String(), EventResumed.String(), HeartbeatEventKind(9).String(),
		GuardTimeout.String(), GuardResumed.String(), GuardStateChange.String(), GuardToggleError.String(), GuardBootUp.String(), GuardEventKind(9).String(),
	} {
		if s == "" {
			t.Error("empty name")
		}
	}
}

func TestMaster(t *testing.T) {
	a, b := testbus.Pair(t)
	sub := subscribe(t, b)
	m := NewMaster(a, WithLogger(nil))
	ctx := context.Background()
	if err := m.Send(ctx, 0, canopen.NMTStart); err != nil {
		t.Fatal(err)
	}
	if err := m.Send(ctx, 42, canopen.NMTResetNode); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][2]byte{{0x01, 0}, {0x81, 42}} {
		f := recv(t, sub, nil)
		if f.ID != 0 || f.Len != 2 || f.Data[0] != want[0] || f.Data[1] != want[1] {
			t.Errorf("frame %v", f)
		}
	}
	if err := m.Send(ctx, 128, canopen.NMTStart); !errors.Is(err, canopen.ErrInvalidNodeID) {
		t.Error(err)
	}
	if err := m.Send(ctx, 1, 0x33); err == nil {
		t.Error("invalid command accepted")
	}
	_ = a.Close()
	if err := m.Send(ctx, 1, canopen.NMTStop); err == nil {
		t.Error("send on closed bus")
	}
	if m.Stats().Sent != 2 {
		t.Errorf("stats %+v", m.Stats())
	}
}
