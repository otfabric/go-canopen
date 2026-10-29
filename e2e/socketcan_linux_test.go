// SPDX-License-Identifier: MIT

//go:build linux

package e2e

import (
	"net"
	"os"
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
)

// iface returns the SocketCAN interface of the tests (CANOPEN_TEST_IFACE,
// default vcan0). Without it the test skips, or fails when
// CANOPEN_REQUIRE_VCAN=1.
func iface(t *testing.T) string {
	t.Helper()
	name := os.Getenv("CANOPEN_TEST_IFACE")
	if name == "" {
		name = "vcan0"
	}
	if ifi, err := net.InterfaceByName(name); err != nil || ifi.Flags&net.FlagUp == 0 {
		msg := "no usable SocketCAN interface " + name + " (create it: sudo ip link add " + name + " type vcan && sudo ip link set " + name + " up)"
		if os.Getenv("CANOPEN_REQUIRE_VCAN") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return name
}

// TestPlantSocketCAN runs the plant over SocketCAN, every participant on
// its own CAN_RAW socket.
func TestPlantSocketCAN(t *testing.T) {
	name := iface(t)
	nettest.CheckLeaks(t)
	runPlant(t, func() canopen.Bus {
		c, err := socketcan.Open(name, socketcan.WithReceiveBuffer(1<<20))
		if err != nil {
			t.Fatal(err)
		}
		b, err := canbus.NewBus(c)
		if err != nil {
			_ = c.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		return b
	})
}
