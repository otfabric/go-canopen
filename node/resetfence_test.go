// SPDX-License-Identifier: MIT

package node_test

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
	"github.com/otfabric/go-canopen/internal/testbus"
)

// TestNoSDOAnswerAfterResetCommand: an SDO request that arrives right
// after an NMT reset command is not answered from the state before the
// reset (CiA 301: the node is Initialising until its boot-up message).
// Regression: the SDO server ran on its own subscription and answered
// such a request while the NMT slave started the reset, then lost the
// next request while the server was renewed; Lely's CiA 302 boot master,
// which uploads 0x1000 and 0x1018 right after its reset communication,
// then took the lost upload for a wrong identity (error status M; found
// by the interop suite).
func TestNoSDOAnswerAfterResetCommand(t *testing.T) {
	ports := testbus.Ports(t, 2)
	d := nettest.Dict(t, 5, nil)
	startNode(t, ports[0], d)
	raw := ports[1]
	sub, err := raw.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	send := func(id canbus.ID, data ...byte) {
		t.Helper()
		f, err := canbus.NewStandard(id, data)
		if err != nil {
			t.Fatal(err)
		}
		if err := raw.Send(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		// Reset communication, then an upload of 0x1000 at once.
		send(0x000, byte(canopen.NMTResetCommunication), 5)
		send(0x605, 0x40, 0x00, 0x10, 0x00, 0, 0, 0, 0)
		// Every frame of the node up to its boot-up message.
		for booted := false; !booted; {
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			f, err := sub.Receive(rctx)
			rcancel()
			if err != nil {
				t.Fatalf("round %d: no boot-up: %v", i, err)
			}
			switch {
			case f.ID == 0x585:
				t.Fatalf("round %d: SDO response %x before the boot-up message", i, f.Data[:f.Len])
			case f.ID == 0x705 && f.Len == 1 && f.Data[0] == 0:
				booted = true
			}
		}
		// After the boot-up the node answers.
		send(0x605, 0x40, 0x00, 0x10, 0x00, 0, 0, 0, 0)
		for answered := false; !answered; {
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			f, err := sub.Receive(rctx)
			rcancel()
			if err != nil {
				t.Fatalf("round %d: no SDO response after the boot-up: %v", i, err)
			}
			answered = f.ID == 0x585
		}
	}
}
