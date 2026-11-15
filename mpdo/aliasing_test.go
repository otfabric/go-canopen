// SPDX-License-Identifier: MIT

package mpdo

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-canopen/internal/testbus"
)

// TestEventOwnsData (safety.md, aliasing): the Data of a receive event is
// the handler's own: a handler that changes it leaves the value written to
// the dictionary unchanged, and so does a producer whose caller changes
// the data after SendDAM.
func TestEventOwnsData(t *testing.T) {
	buses := testbus.Ports(t, 2)
	da, db := device(t, 5), device(t, 6)
	setMPDO(t, da, true, 1, 0x281, ModeDAM)
	setMPDO(t, db, false, 1, 0x281, ModeDAM)
	p := newProducer(t, buses[0], da, 1)
	got := make(chan Event, 1)
	newConsumer(t, buses[1], db, 1, WithReceiveHandler(func(e Event) {
		for i := range e.Data {
			e.Data[i] ^= 0xFF
		}
		got <- e
	}))
	data := []byte{0x78, 0x56, 0x34, 0x12}
	if err := p.SendDAM(ctx(t), 6, idxU32, 0, data); err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 0
	}
	var ev Event
	select {
	case ev = <-got:
	case <-ctx(t).Done():
		t.Fatal("no receive event")
	}
	if !bytes.Equal(ev.Data, []byte{0x87, 0xA9, 0xCB, 0xED}) {
		t.Fatalf("event data % X: the handler's change is not in its own copy", ev.Data)
	}
	if ev.Message.Data != [4]byte{0x78, 0x56, 0x34, 0x12} {
		t.Errorf("event message data % X, want the data sent", ev.Message.Data)
	}
	if v := getU(db, idxU32, 0); v != 0x12345678 {
		t.Errorf("dictionary 0x%08X after the handler changed the event data, want 0x12345678", v)
	}
}
