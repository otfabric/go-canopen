// SPDX-License-Identifier: MIT

package cia402_test

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-canopen/cia402"
	"github.com/otfabric/go-canopen/od"
)

// localSDO serves SDO transfers from a dictionary in-process, stepping the
// drive simulation by 1 ms on every upload. A real application uses an
// sdo.Client instead.
type localSDO struct {
	dict  *od.Dictionary
	drive *cia402.Drive
}

func (s localSDO) Upload(_ context.Context, index uint16, sub uint8) ([]byte, error) {
	s.drive.Step(time.Millisecond)
	return s.dict.Read(index, sub)
}

func (s localSDO) Download(_ context.Context, index uint16, sub uint8, data []byte) error {
	return s.dict.Write(index, sub, data)
}

func Example() {
	// Device side: a drive model on an object dictionary.
	dict := od.New(5)
	drive, err := cia402.NewDrive(dict, cia402.DriveConfig{})
	if err != nil {
		panic(err)
	}
	defer drive.Close()

	// Master side: a controller talking to it through SDO.
	ctrl := cia402.NewController(localSDO{dict, drive}, &cia402.ControllerOptions{PollInterval: time.Microsecond})
	ctx := context.Background()
	if err := ctrl.Enable(ctx); err != nil {
		panic(err)
	}
	if err := ctrl.MoveTo(ctx, 2500, &cia402.MoveOptions{Velocity: 20000, Wait: true}); err != nil {
		panic(err)
	}
	pos, _ := ctrl.Position(ctx)
	st, _ := ctrl.State(ctx)
	fmt.Println(st, pos)
	fmt.Println(cia402.Path(cia402.SwitchOnDisabled, cia402.OperationEnabled)) //nolint:staticcheck // prints the slice
	// Output:
	// Operation enabled 2500
	// [Shutdown Switch on Enable operation]
}
