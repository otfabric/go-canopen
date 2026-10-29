// SPDX-License-Identifier: MIT

package lss

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// stuckSlave answers like a slave that stays in the configuration state
// because it missed every "switch state global" (a lost frame): it
// answers the inquiries with its address and node-ID 0xFF and accepts
// configure node-ID, counting the requests it took.
func stuckSlave(t *testing.T, bus *canbus.Bus, a Address, configured *atomic.Int32) {
	t.Helper()
	sub, err := bus.Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(MasterID)}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done; _ = sub.Close() })
	go func() {
		defer close(done)
		for {
			f, err := sub.Receive(ctx)
			if err != nil {
				return
			}
			var r canbus.Frame
			switch cs := Command(f.Data[0]); {
			case cs == CmdInquireNodeID:
				r = frame(SlaveID, cs, byte(Unconfigured))
			case cs >= CmdInquireVendor && cs <= CmdInquireSerial:
				r = frame32(SlaveID, cs, a.Part(Part(cs-CmdInquireVendor)))
			case cs == CmdConfigureNodeID:
				configured.Add(1)
				r = frame(SlaveID, cs, CodeOK, 0)
			default:
				continue
			}
			_ = bus.Send(ctx, &r)
		}
	}()
}

// TestConfigureStuckSlave is the regression test of a wrong LSS
// assignment seen under chaos: a slave left in the configuration state by
// a lost "switch state global" took the configure node-ID meant for the
// next slave. Configure must refuse to configure while another slave is
// selected, and configure nothing.
func TestConfigureStuckSlave(t *testing.T) {
	checkLeaks(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0])
	target := Address{Vendor: 1, Product: 2, Revision: 3, Serial: 0x2002}
	s := startSlave(t, ports[1], target, Unconfigured)
	var stuck atomic.Int32
	stuckSlave(t, ports[2], Address{Vendor: 1, Product: 2, Revision: 3, Serial: 0x2001}, &stuck)
	ctx := ctxT(t)
	err := m.ConfigureNodeID(ctx, target, 6)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("configure with a stuck slave: %v", err)
	}
	if stuck.Load() != 0 || s.Pending().NodeID != Unconfigured {
		t.Fatalf("configured despite the conflict: stuck %d, pending %d", stuck.Load(), s.Pending().NodeID)
	}
	if m.Stats().Conflicts == 0 {
		t.Fatal("conflict not counted")
	}
}

// TestConfigureNotAlone covers the check after the selection: a second
// slave answers the identity inquiries (with another serial number) while
// the target is selected, so the selection is not confirmed.
func TestConfigureNotAlone(t *testing.T) {
	checkLeaks(t)
	ports := testbus.Ports(t, 3)
	m := startMaster(t, ports[0])
	target := Address{Vendor: 1, Product: 2, Revision: 3, Serial: 0x2002}
	s := startSlave(t, ports[1], target, Unconfigured)
	// A device that answers identity inquiries but not inquire node-ID:
	// it passes the check before the selection and fails the one after.
	sub, err := ports[2].Subscribe(canbus.WithFilters(canbus.FilterSet{Filters: []canbus.Filter{canbus.ExactStandard(MasterID)}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctxT(t))
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done; _ = sub.Close() })
	go func() {
		defer close(done)
		other := Address{Vendor: 1, Product: 2, Revision: 3, Serial: 0x2001}
		for {
			f, err := sub.Receive(ctx)
			if err != nil {
				return
			}
			if cs := Command(f.Data[0]); cs >= CmdInquireVendor && cs <= CmdInquireSerial {
				r := frame32(SlaveID, cs, other.Part(Part(cs-CmdInquireVendor)))
				_ = ports[2].Send(ctx, &r)
			}
		}
	}()
	if err := m.ConfigureNodeID(ctx, target, 6); !errors.Is(err, ErrConflict) {
		t.Fatalf("selection not alone: %v", err)
	}
	if s.Pending().NodeID != Unconfigured {
		t.Fatalf("configured despite the conflict: %d", s.Pending().NodeID)
	}
	// Without the intruder the same call succeeds.
	cancel()
	<-done
	if err := m.ConfigureNodeID(ctxT(t), target, 6); err != nil {
		t.Fatal(err)
	}
	if s.NodeID() != 6 {
		t.Fatal(s.NodeID())
	}
}
