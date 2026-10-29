// SPDX-License-Identifier: MIT

// Package program is the program download of CiA 302-3 on the device
// side: the objects through which a CANopen manager replaces the
// application software of a device over SDO.
//
//   - 0x1F50 program data (ARRAY of DOMAIN, write-only): the program
//     image, written with an SDO (block) download;
//   - 0x1F51 program control (ARRAY of UNSIGNED8): writing 0 stops the
//     program, 1 starts it, 2 resets it, 3 clears it (erases the flash);
//     reading returns the program's state (0 stopped, 1 started, 3
//     cleared);
//   - 0x1F56 application software identification (ARRAY of UNSIGNED32,
//     read-only): a value identifying the program (a CRC, a version),
//     which the manager compares with its expected value 0x1F55;
//   - 0x1F57 flash status identification (ARRAY of UNSIGNED32,
//     read-only): bit 0 set while programming is in progress, bits 1..7
//     an error code ([FlashError]).
//
// The sub-index is the program number (1 for a device with one program).
// A [Device] serves the objects of a dictionary and leaves the flashing to
// a [Handler]. The sequence a manager uses (Lely lely-core's boot slave
// process; master.Master.DownloadProgram) is: stop (0x1F51 = 0), clear
// (0x1F51 = 3), download 0x1F50, poll 0x1F57 until bit 0 is clear and
// check the error code, start (0x1F51 = 1), poll 0x1F51 until it reads 1.
//
// Sources: CiA 302-3 is a member document; the object meanings and
// values here follow Lely lely-core (src/co/nmt_boot.c, which cites CiA
// 302-2 v4.1.0) and Zephyr's CANopenNode program download module
// (modules/canopennode/canopen_program.c, which cites the CiA 302-3 draft
// standard proposal v4.1.0): the program control values, the flash
// status codes and the state rules (clear only when stopped, data only
// when cleared, control in Pre-operational only) are taken from them.
package program

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// Object indices of CiA 302-3 program download.
const (
	IndexData           uint16 = 0x1F50 // program data (device)
	IndexControl        uint16 = 0x1F51 // program control (device)
	IndexIdentification uint16 = 0x1F56 // application software identification (device)
	IndexFlashStatus    uint16 = 0x1F57 // flash status identification (device)
	// IndexExpectedIdentification is the manager's expected application
	// software identification of each node (ARRAY of UNSIGNED32,
	// sub-index = node-ID).
	IndexExpectedIdentification uint16 = 0x1F55
	// IndexManagerData is the manager's program data of each node (ARRAY
	// of DOMAIN, sub-index = node-ID), downloaded to the node's 0x1F50
	// sub-index 1 when its software must be updated.
	IndexManagerData uint16 = 0x1F58
)

// Command is a value written to the program control 0x1F51.
type Command uint8

// Program control commands.
const (
	Stop  Command = 0
	Start Command = 1
	Reset Command = 2
	Clear Command = 3
)

// String names the command.
func (c Command) String() string {
	switch c {
	case Stop:
		return "stop"
	case Start:
		return "start"
	case Reset:
		return "reset"
	case Clear:
		return "clear"
	}
	return fmt.Sprintf("Command(%d)", uint8(c))
}

// State is the value read from the program control 0x1F51.
type State uint8

// Program states.
const (
	Stopped State = 0
	Started State = 1
	Cleared State = 3
)

// String names the state.
func (s State) String() string {
	switch s {
	case Stopped:
		return "stopped"
	case Started:
		return "started"
	case Cleared:
		return "cleared"
	}
	return fmt.Sprintf("State(%d)", uint8(s))
}

// FlashError is the error code of a flash status (bits 1..7 of 0x1F57).
// It implements error; FlashOK is "no error".
type FlashError uint8

// Flash status error codes.
const (
	FlashOK                FlashError = 0
	FlashNoValidProgram    FlashError = 1
	FlashDataFormatUnknown FlashError = 2
	FlashDataFormatError   FlashError = 3 // data format or CRC error
	FlashNotCleared        FlashError = 4 // flash not cleared before write
	FlashWriteError        FlashError = 5
	FlashAddressError      FlashError = 6 // general address error
	FlashSecured           FlashError = 7 // write access currently forbidden
	FlashUnspecified       FlashError = 63
)

var flashText = map[FlashError]string{
	FlashOK:                "no error",
	FlashNoValidProgram:    "no valid program available",
	FlashDataFormatUnknown: "data format unknown",
	FlashDataFormatError:   "data format error or data CRC error",
	FlashNotCleared:        "flash not cleared before write",
	FlashWriteError:        "flash write error",
	FlashAddressError:      "general address error",
	FlashSecured:           "flash secured (write access currently forbidden)",
	FlashUnspecified:       "unspecified error",
}

// Error implements error.
func (e FlashError) Error() string {
	if t, ok := flashText[e]; ok {
		return "program: flash status: " + t
	}
	if e > FlashUnspecified {
		return fmt.Sprintf("program: flash status: manufacturer-specific error %d", uint8(e))
	}
	return fmt.Sprintf("program: flash status: reserved error %d", uint8(e))
}

// FlashStatus is the value of the flash status identification 0x1F57:
// bit 0 programming in progress, bits 1..7 the error code; the other bits
// are manufacturer-specific.
type FlashStatus uint32

// NewFlashStatus returns the status with the in-progress bit and an error
// code (masked to 7 bits).
func NewFlashStatus(inProgress bool, code FlashError) FlashStatus {
	s := FlashStatus(code&0x7F) << 1
	if inProgress {
		s |= 1
	}
	return s
}

// InProgress reports bit 0.
func (s FlashStatus) InProgress() bool { return s&1 != 0 }

// Code returns the error code (bits 1..7).
func (s FlashStatus) Code() FlashError { return FlashError(s>>1) & 0x7F }

// Err returns nil when no programming is in progress and the code is
// FlashOK, the code otherwise (nil while in progress).
func (s FlashStatus) Err() error {
	if s.InProgress() || s.Code() == FlashOK {
		return nil
	}
	return s.Code()
}

// ErrPending is returned by [Handler.Download] that goes on programming
// after it returned: the flash status stays "in progress" until the
// handler sets it with [Device.SetFlashStatus].
var ErrPending = errors.New("program: programming in progress")

// Handler does the flashing of a [Device]. Its methods run on the SDO
// server's goroutine; a long operation delays the SDO response (and may
// make the client time out), so a slow flash should return [ErrPending]
// from Download and finish in the background.
type Handler interface {
	// Control executes a command on program n (1..). An error refuses
	// it: an [canopen.AbortCode] is sent as it is, another error as
	// canopen.AbortHardware.
	Control(n uint8, cmd Command) error
	// Download receives the image written to 0x1F50 sub-index n; it is
	// only called while program n is cleared. Returning nil ends
	// programming without error; a [FlashError] (or an error wrapping
	// one) sets that code in the flash status, another error
	// FlashUnspecified, and refuses the SDO download.
	Download(n uint8, data []byte) error
	// Identification returns the application software identification
	// of program n (0x1F56).
	Identification(n uint8) uint32
}

// Config configures a [Device].
type Config struct {
	// Handler does the flashing; required.
	Handler Handler
	// Programs is the number of programs (sub-indices); default 1.
	Programs int
	// State, when set, returns the device's NMT state: program control
	// commands are refused (canopen.AbortDeviceState) outside
	// Pre-operational. The node package sets it.
	State func() canopen.NMTState
	// Stopped makes every program start in state Stopped (a bootloader
	// without a running application); by default they start Started.
	Stopped bool
}

// Device serves the program download objects of a dictionary. It is safe
// for concurrent use.
type Device struct {
	d      *od.Dictionary
	cfg    Config
	mu     sync.Mutex
	state  map[uint8]State
	flash  map[uint8]FlashStatus
	unhook []func()
}

// AddObjects adds to d, where missing, the program download objects
// 0x1F50 (DOMAIN, wo), 0x1F51 (UNSIGNED8, rw), 0x1F56 and 0x1F57
// (UNSIGNED32, ro) with sub-indices 1..programs.
func AddObjects(d *od.Dictionary, programs int) error {
	if programs < 1 || programs > 254 {
		return fmt.Errorf("program: %d programs", programs)
	}
	u32 := canopen.Unsigned32
	objs := []*od.Object{
		od.NewArray(IndexData, "Program data", canopen.Domain, od.AccessWO, programs, nil, od.AccessConst),
		od.NewArray(IndexControl, "Program control", canopen.Unsigned8, od.AccessRW, programs, nil, od.AccessConst),
		od.NewArray(IndexIdentification, "Application software identification", u32, od.AccessRO, programs, nil, od.AccessConst),
		od.NewArray(IndexFlashStatus, "Flash status identification", u32, od.AccessRO, programs, nil, od.AccessConst),
	}
	for _, o := range objs {
		if d.Object(o.Index) != nil {
			continue
		}
		if err := d.Add(o); err != nil {
			return err
		}
	}
	return nil
}

// Attach adds the program download objects to d where missing
// ([AddObjects]) and serves them with cfg.Handler. Close removes the
// hooks.
func Attach(d *od.Dictionary, cfg Config) (*Device, error) {
	if d == nil || cfg.Handler == nil {
		return nil, errors.New("program: Attach needs a dictionary and a handler")
	}
	if cfg.Programs == 0 {
		cfg.Programs = 1
	}
	if err := AddObjects(d, cfg.Programs); err != nil {
		return nil, err
	}
	dev := &Device{d: d, cfg: cfg, state: map[uint8]State{}, flash: map[uint8]FlashStatus{}}
	for n := 1; n <= cfg.Programs; n++ {
		dev.state[uint8(n)] = Started
		if cfg.Stopped {
			dev.state[uint8(n)] = Stopped
		}
	}
	dev.unhook = append(dev.unhook,
		d.OnWrite(IndexData, -1, dev.writeData),
		d.OnWrite(IndexControl, -1, dev.writeControl),
		d.OnRead(IndexControl, -1, dev.readControl),
		d.OnRead(IndexIdentification, -1, dev.readIdentification),
		d.OnRead(IndexFlashStatus, -1, dev.readFlash),
	)
	return dev, nil
}

// Close removes the device's hooks from the dictionary.
func (dev *Device) Close() {
	dev.mu.Lock()
	hooks := dev.unhook
	dev.unhook = nil
	dev.mu.Unlock()
	for _, f := range hooks {
		f()
	}
}

// State returns the state of program n.
func (dev *Device) State(n uint8) State {
	dev.mu.Lock()
	defer dev.mu.Unlock()
	return dev.state[n]
}

// SetState sets the state of program n (a handler that changes it on its
// own, e.g. a program that stopped).
func (dev *Device) SetState(n uint8, s State) {
	dev.mu.Lock()
	dev.state[n] = s
	dev.mu.Unlock()
}

// FlashStatus returns the flash status of program n.
func (dev *Device) FlashStatus(n uint8) FlashStatus {
	dev.mu.Lock()
	defer dev.mu.Unlock()
	return dev.flash[n]
}

// SetFlashStatus sets the flash status of program n: what a handler that
// returned [ErrPending] reports when it is done.
func (dev *Device) SetFlashStatus(n uint8, s FlashStatus) {
	dev.mu.Lock()
	dev.flash[n] = s
	dev.mu.Unlock()
}

func u32(x uint32) []byte { return binary.LittleEndian.AppendUint32(nil, x) }

// toAbort converts a handler's error to the abort the SDO server sends.
func toAbort(err error) error {
	var ac canopen.AbortCode
	if errors.As(err, &ac) {
		return ac
	}
	return canopen.AbortHardware
}

func (dev *Device) writeData(v *od.Variable, data []byte) error {
	n := v.SubIndex
	dev.mu.Lock()
	if dev.state[n] != Cleared {
		dev.flash[n] = NewFlashStatus(false, FlashNotCleared)
		dev.mu.Unlock()
		return canopen.AbortDeviceState
	}
	dev.flash[n] = NewFlashStatus(true, FlashOK)
	dev.mu.Unlock()
	err := dev.cfg.Handler.Download(n, data)
	dev.mu.Lock()
	defer dev.mu.Unlock()
	switch {
	case errors.Is(err, ErrPending):
		dev.state[n] = Stopped
		return nil
	case err != nil:
		code := FlashUnspecified
		var fe FlashError
		if errors.As(err, &fe) && fe != FlashOK {
			code = fe
		}
		dev.flash[n] = NewFlashStatus(false, code)
		return toAbort(err)
	}
	dev.flash[n] = NewFlashStatus(false, FlashOK)
	dev.state[n] = Stopped
	return nil
}

func (dev *Device) writeControl(v *od.Variable, data []byte) error {
	n := v.SubIndex
	if st := dev.cfg.State; st != nil && st() != canopen.StatePreOperational {
		return canopen.AbortDeviceState
	}
	cmd := Command(data[0])
	next := Stopped
	switch cmd {
	case Stop:
	case Start, Reset:
		next = Started
	case Clear:
		if dev.State(n) != Stopped {
			return canopen.AbortDeviceState
		}
		next = Cleared
	default:
		return canopen.AbortInvalidValue
	}
	if err := dev.cfg.Handler.Control(n, cmd); err != nil {
		return toAbort(err)
	}
	dev.mu.Lock()
	dev.state[n] = next
	if cmd == Clear {
		dev.flash[n] = NewFlashStatus(false, FlashOK)
	}
	dev.mu.Unlock()
	return nil
}

func (dev *Device) readControl(v *od.Variable, stored []byte) ([]byte, error) {
	if v.SubIndex == 0 {
		return stored, nil
	}
	return []byte{byte(dev.State(v.SubIndex))}, nil
}

func (dev *Device) readIdentification(v *od.Variable, stored []byte) ([]byte, error) {
	if v.SubIndex == 0 {
		return stored, nil
	}
	return u32(dev.cfg.Handler.Identification(v.SubIndex)), nil
}

func (dev *Device) readFlash(v *od.Variable, stored []byte) ([]byte, error) {
	if v.SubIndex == 0 {
		return stored, nil
	}
	return u32(uint32(dev.FlashStatus(v.SubIndex))), nil
}
