// SPDX-License-Identifier: MIT

package e2e

import (
	"testing"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/virtual"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/internal/nettest"
)

// TestPlantVirtual runs the plant on the virtual bus.
func TestPlantVirtual(t *testing.T) {
	nettest.CheckLeaks(t)
	vb := virtual.New(virtual.Config{Name: "plant"})
	t.Cleanup(func() { _ = vb.Close() })
	runPlant(t, func() canopen.Bus {
		p, err := vb.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := canbus.NewBus(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		return b
	})
}
