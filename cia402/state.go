// SPDX-License-Identifier: MIT

package cia402

import "fmt"

// State is a state of the CiA 402 power drive state machine (IEC
// 61800-7-201 6.3.2).
type State uint8

// States of the drive. Unknown is the zero value: a statusword that matches
// none of the patterns, and the "Start" pseudo-state of transition 0.
const (
	Unknown State = iota
	NotReadyToSwitchOn
	SwitchOnDisabled
	ReadyToSwitchOn
	SwitchedOn
	OperationEnabled
	QuickStopActive
	FaultReactionActive
	Fault
)

var stateNames = [...]string{
	Unknown:             "Unknown",
	NotReadyToSwitchOn:  "Not ready to switch on",
	SwitchOnDisabled:    "Switch on disabled",
	ReadyToSwitchOn:     "Ready to switch on",
	SwitchedOn:          "Switched on",
	OperationEnabled:    "Operation enabled",
	QuickStopActive:     "Quick stop active",
	FaultReactionActive: "Fault reaction active",
	Fault:               "Fault",
}

// String returns the CiA 402 name of the state.
func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return fmt.Sprintf("State(%d)", uint8(s))
}

// Statusword bits (object 0x6041).
const (
	StatusReadyToSwitchOn  uint16 = 1 << 0
	StatusSwitchedOn       uint16 = 1 << 1
	StatusOperationEnabled uint16 = 1 << 2
	StatusFault            uint16 = 1 << 3
	StatusVoltageEnabled   uint16 = 1 << 4
	// StatusQuickStop is active low: 0 means a quick stop is in progress.
	StatusQuickStop        uint16 = 1 << 5
	StatusSwitchOnDisabled uint16 = 1 << 6
	StatusWarning          uint16 = 1 << 7
	// StatusRemote reports that the controlword is processed.
	StatusRemote              uint16 = 1 << 9
	StatusTargetReached       uint16 = 1 << 10
	StatusInternalLimitActive uint16 = 1 << 11
	// StatusOMS12 and StatusOMS13 are the operation-mode-specific bits.
	StatusOMS12 uint16 = 1 << 12
	StatusOMS13 uint16 = 1 << 13

	// Mode-specific meanings of bits 12 and 13.
	StatusSetPointAcknowledge uint16 = StatusOMS12 // PP: new set-point accepted
	StatusFollowingError      uint16 = StatusOMS13 // PP, CSP, CSV
	StatusSpeedZero           uint16 = StatusOMS12 // PV: velocity is zero
	StatusMaxSlippage         uint16 = StatusOMS13 // PV: maximum slippage reached
	StatusHomingAttained      uint16 = StatusOMS12 // HM
	StatusHomingError         uint16 = StatusOMS13 // HM
	StatusIPModeActive        uint16 = StatusOMS12 // IP
	StatusFollowsCommand      uint16 = StatusOMS12 // CSP, CSV, CST: drive follows the command value
)

// statePatterns are the (mask, value) patterns of the statusword per state.
var statePatterns = [...]struct {
	mask, value uint16
	state       State
}{
	{0x4F, 0x00, NotReadyToSwitchOn},
	{0x4F, 0x40, SwitchOnDisabled},
	{0x6F, 0x21, ReadyToSwitchOn},
	{0x6F, 0x23, SwitchedOn},
	{0x6F, 0x27, OperationEnabled},
	{0x6F, 0x07, QuickStopActive},
	{0x4F, 0x0F, FaultReactionActive},
	{0x4F, 0x08, Fault},
}

// DecodeStatusword returns the state a statusword reports, or Unknown when
// it matches none of the CiA 402 patterns.
func DecodeStatusword(sw uint16) State {
	for _, p := range statePatterns {
		if sw&p.mask == p.value {
			return p.state
		}
	}
	return Unknown
}

// StateBits returns the statusword bits 0..6 that report a state: the value
// of its pattern, with bit 5 (quick stop) set where the pattern leaves it
// open, and bit 4 (voltage enabled) set in the states with the power stage
// on. Unknown returns 0.
func StateBits(s State) uint16 {
	switch s {
	case NotReadyToSwitchOn:
		return 0x00
	case SwitchOnDisabled:
		return 0x40
	case ReadyToSwitchOn:
		return 0x31
	case SwitchedOn:
		return 0x33
	case OperationEnabled:
		return 0x37
	case QuickStopActive:
		return 0x17
	case FaultReactionActive:
		return 0x1F
	case Fault:
		return 0x08
	}
	return 0
}

// Controlword bits (object 0x6040).
const (
	CtrlSwitchOn        uint16 = 1 << 0
	CtrlEnableVoltage   uint16 = 1 << 1
	CtrlQuickStop       uint16 = 1 << 2 // active low: 0 requests a quick stop
	CtrlEnableOperation uint16 = 1 << 3
	// CtrlOMS4, CtrlOMS5 and CtrlOMS6 are the operation-mode-specific bits.
	CtrlOMS4       uint16 = 1 << 4
	CtrlOMS5       uint16 = 1 << 5
	CtrlOMS6       uint16 = 1 << 6
	CtrlFaultReset uint16 = 1 << 7 // a rising edge resets a fault
	CtrlHalt       uint16 = 1 << 8
	CtrlOMS9       uint16 = 1 << 9

	// Mode-specific meanings.
	CtrlNewSetPoint          uint16 = CtrlOMS4 // PP: rising edge takes a new set-point
	CtrlChangeSetImmediately uint16 = CtrlOMS5 // PP: the new set-point replaces the active one
	CtrlRelative             uint16 = CtrlOMS6 // PP: the target position is relative
	CtrlChangeOnSetPoint     uint16 = CtrlOMS9 // PP: (not simulated by Drive)
	CtrlHomingStart          uint16 = CtrlOMS4 // HM: rising edge starts, falling edge interrupts homing
	CtrlEnableIP             uint16 = CtrlOMS4 // IP: enable interpolation

	// CtrlCommandMask covers the bits the device control commands use.
	CtrlCommandMask uint16 = 0x8F
)

// Command is a device control command, encoded in bits 0..3 and 7 of the
// controlword.
//
// Some commands share an encoding: SwitchOn and DisableOperation are both
// 0bxxxx_0111, SwitchOnAndEnable and EnableOperation both 0bxxxx_1111. Which
// one a controlword means depends on the state; [Next] treats each pair
// identically.
type Command uint8

// Device control commands. The zero value is "no command" (an automatic
// transition in [Transition]).
const (
	Shutdown Command = iota + 1
	SwitchOn
	SwitchOnAndEnable
	DisableVoltage
	QuickStop
	DisableOperation
	EnableOperation
	FaultReset
)

var commandNames = [...]string{
	0:                 "None",
	Shutdown:          "Shutdown",
	SwitchOn:          "Switch on",
	SwitchOnAndEnable: "Switch on + enable operation",
	DisableVoltage:    "Disable voltage",
	QuickStop:         "Quick stop",
	DisableOperation:  "Disable operation",
	EnableOperation:   "Enable operation",
	FaultReset:        "Fault reset",
}

// String returns the CiA 402 name of the command.
func (c Command) String() string {
	if int(c) < len(commandNames) {
		return commandNames[c]
	}
	return fmt.Sprintf("Command(%d)", uint8(c))
}

// Bits returns the controlword bits 0..3 and 7 of the command; bits the
// command leaves open are 0.
func (c Command) Bits() uint16 {
	switch c {
	case Shutdown:
		return 0x06
	case SwitchOn, DisableOperation:
		return 0x07
	case SwitchOnAndEnable, EnableOperation:
		return 0x0F
	case DisableVoltage:
		return 0x00
	case QuickStop:
		return 0x02
	case FaultReset:
		return 0x80
	}
	return 0
}

// Controlword returns base with bits 0..3 and 7 replaced by the command;
// the other bits (halt, mode-specific bits) are kept. A fault reset needs a
// rising edge of bit 7: write base with bit 7 clear first.
func (c Command) Controlword(base uint16) uint16 {
	return base&^CtrlCommandMask | c.Bits()
}

// DecodeControlword returns the command a controlword encodes. Bit 7 set
// decodes as FaultReset (a device acts on its rising edge only). A
// controlword always decodes to a command: 0bxxxx_0111 is SwitchOn and
// 0bxxxx_1111 EnableOperation (see [Command] for the shared encodings).
func DecodeControlword(cw uint16) Command {
	switch {
	case cw&CtrlFaultReset != 0:
		return FaultReset
	case cw&CtrlEnableVoltage == 0:
		return DisableVoltage
	case cw&CtrlQuickStop == 0:
		return QuickStop
	case cw&CtrlSwitchOn == 0:
		return Shutdown
	case cw&CtrlEnableOperation == 0:
		return SwitchOn
	}
	return EnableOperation
}

// canonical maps commands with a shared encoding to one of them.
func (c Command) canonical() Command {
	switch c {
	case DisableOperation:
		return SwitchOn
	case SwitchOnAndEnable:
		return EnableOperation
	}
	return c
}

// Transition is one transition of the CiA 402 state diagram.
type Transition struct {
	// Number is the transition number, 0..16.
	Number int
	// From is the state left; Unknown for transition 0 (from Start) and
	// transition 13 (from any state).
	From, To State
	// Command triggers the transition; 0 for transitions the drive takes by
	// itself (0, 1, 13, 14, and 12 after a completed quick stop).
	Command Command
}

// transitions is the state diagram, in the order Path prefers.
var transitions = []Transition{
	{0, Unknown, NotReadyToSwitchOn, 0},
	{1, NotReadyToSwitchOn, SwitchOnDisabled, 0},
	{2, SwitchOnDisabled, ReadyToSwitchOn, Shutdown},
	{3, ReadyToSwitchOn, SwitchedOn, SwitchOn},
	{3, ReadyToSwitchOn, SwitchedOn, SwitchOnAndEnable},
	{4, SwitchedOn, OperationEnabled, EnableOperation},
	{5, OperationEnabled, SwitchedOn, DisableOperation},
	{6, SwitchedOn, ReadyToSwitchOn, Shutdown},
	{7, ReadyToSwitchOn, SwitchOnDisabled, DisableVoltage},
	{7, ReadyToSwitchOn, SwitchOnDisabled, QuickStop},
	{8, OperationEnabled, ReadyToSwitchOn, Shutdown},
	{9, OperationEnabled, SwitchOnDisabled, DisableVoltage},
	{10, SwitchedOn, SwitchOnDisabled, DisableVoltage},
	{10, SwitchedOn, SwitchOnDisabled, QuickStop},
	{11, OperationEnabled, QuickStopActive, QuickStop},
	{12, QuickStopActive, SwitchOnDisabled, DisableVoltage},
	{12, QuickStopActive, SwitchOnDisabled, 0},
	{13, Unknown, FaultReactionActive, 0},
	{14, FaultReactionActive, Fault, 0},
	{15, Fault, SwitchOnDisabled, FaultReset},
	{16, QuickStopActive, OperationEnabled, EnableOperation},
}

// Transitions returns the CiA 402 state diagram: every transition with the
// command that triggers it (a transition triggered by two commands appears
// twice).
func Transitions() []Transition { return append([]Transition(nil), transitions...) }

// Next returns the state a command leads to from a state, and the number of
// the transition. ok is false when the command triggers no transition in
// that state (the drive stays where it is). SwitchOnAndEnable from Ready to
// switch on returns transition 3 to Switched on; a device then continues
// with transition 4. Transition 16 (Quick stop active → Operation enabled)
// is returned as the diagram allows it, though a device takes it only with a
// quick stop option code 5..8.
func Next(s State, c Command) (to State, transition int, ok bool) {
	if c == 0 {
		return s, 0, false
	}
	c = c.canonical()
	for _, t := range transitions {
		if t.From == s && t.Command != 0 && t.Command.canonical() == c {
			return t.To, t.Number, true
		}
	}
	return s, 0, false
}

// Path returns the shortest command sequence from one state to another, each
// command one transition. Automatic transitions are taken for granted: from
// Not ready to switch on the path starts at Switch on disabled, from Fault
// reaction active at Fault. The result is empty (not nil) when from is
// already to, and nil when to cannot be reached by commands (Not ready to
// switch on, Fault reaction active, Fault, Unknown).
func Path(from, to State) []Command {
	switch from {
	case NotReadyToSwitchOn:
		from = SwitchOnDisabled
	case FaultReactionActive:
		from = Fault
	}
	if from == to {
		return []Command{}
	}
	type node struct {
		s    State
		path []Command
	}
	seen := map[State]bool{from: true}
	queue := []node{{from, nil}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, t := range transitions {
			if t.From != n.s || t.Command == 0 || seen[t.To] {
				continue
			}
			p := append(append([]Command(nil), n.path...), t.Command)
			if t.To == to {
				return p
			}
			seen[t.To] = true
			queue = append(queue, node{t.To, p})
		}
	}
	return nil
}

// ValidTransition reports whether the state diagram has transition n from
// one state to another (for transition 13, from any state but Fault reaction
// active and Fault).
func ValidTransition(n int, from, to State) bool {
	for _, t := range transitions {
		if t.Number != n || t.To != to {
			continue
		}
		if t.From == from || (n == 13 && from != FaultReactionActive && from != Fault) {
			return true
		}
	}
	return false
}
