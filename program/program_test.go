// SPDX-License-Identifier: MIT

package program_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/program"
)

// image is a handler keeping the program in memory.
type image struct{ data []byte }

func (i *image) Control(uint8, program.Command) error { return nil }
func (i *image) Download(_ uint8, b []byte) error     { i.data = b; return nil }
func (i *image) Identification(uint8) uint32          { return uint32(len(i.data)) }

// TestDevice drives the objects with remote dictionary accesses, as an
// SDO server does.
func TestDevice(t *testing.T) {
	d := od.New(5)
	state := canopen.StatePreOperational
	img := &image{}
	dev, err := program.Attach(d, program.Config{Handler: img, State: func() canopen.NMTState { return state }})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	steps := []struct {
		name  string
		index uint16
		data  []byte
		want  error
		state program.State
	}{
		{"data while started", program.IndexData, []byte{1}, canopen.AbortDeviceState, program.Started},
		{"stop", program.IndexControl, []byte{0}, nil, program.Stopped},
		{"clear", program.IndexControl, []byte{3}, nil, program.Cleared},
		{"data", program.IndexData, []byte{1, 2, 3}, nil, program.Stopped},
		{"start", program.IndexControl, []byte{1}, nil, program.Started},
	}
	for _, s := range steps {
		if err := d.Write(s.index, 1, s.data); !errors.Is(err, s.want) && (s.want != nil || err != nil) {
			t.Fatalf("%s: %v", s.name, err)
		}
		if dev.State(1) != s.state {
			t.Fatalf("%s: state %s", s.name, dev.State(1))
		}
	}
	if b, _ := d.Read(program.IndexIdentification, 1); b[0] != 3 {
		t.Fatalf("identification % X", b)
	}
	if b, _ := d.Read(program.IndexFlashStatus, 1); b[0] != 0 {
		t.Fatalf("flash % X", b)
	}
	state = canopen.StateOperational
	if err := d.Write(program.IndexControl, 1, []byte{0}); !errors.Is(err, canopen.AbortDeviceState) {
		t.Fatalf("operational: %v", err)
	}
	if _, err := program.Attach(d, program.Config{}); err == nil {
		t.Fatal("Attach without handler")
	}
}

func ExampleFlashStatus() {
	st := program.NewFlashStatus(false, program.FlashDataFormatError)
	fmt.Println(st.InProgress(), st.Code() == program.FlashDataFormatError, st.Err())
	// Output: false true program: flash status: data format error or data CRC error
}
