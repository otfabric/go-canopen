// SPDX-License-Identifier: MIT

package canopen

import "fmt"

// NMTState is a node's NMT state as it reports it in heartbeat and node
// guarding messages (CiA 301 7.2.8.3.2). Boot-up is reported as
// StateInitialising (0x00).
type NMTState uint8

// NMT states.
const (
	StateInitialising   NMTState = 0x00
	StateStopped        NMTState = 0x04
	StateOperational    NMTState = 0x05
	StatePreOperational NMTState = 0x7F
	// StateUnknown is no state a node reports: a consumer's state before
	// it has heard from the node, or after a heartbeat timeout.
	StateUnknown NMTState = 0xFF
)

// String names the state as CiA 301 does.
func (s NMTState) String() string {
	switch s {
	case StateInitialising:
		return "initialising"
	case StateStopped:
		return "stopped"
	case StateOperational:
		return "operational"
	case StatePreOperational:
		return "pre-operational"
	case StateUnknown:
		return "unknown"
	}
	return fmt.Sprintf("NMTState(0x%02X)", uint8(s))
}

// ParseNMTState parses a state name as String returns it.
func ParseNMTState(s string) (NMTState, error) {
	for _, st := range []NMTState{StateInitialising, StateStopped, StateOperational, StatePreOperational, StateUnknown} {
		if st.String() == s {
			return st, nil
		}
	}
	return 0, fmt.Errorf("canopen: unknown NMT state %q", s)
}

// NMTCommand is the command specifier of an NMT module control message
// (CAN-ID 0x000: command, node-ID).
type NMTCommand uint8

// NMT commands.
const (
	NMTStart               NMTCommand = 0x01 // enter operational
	NMTStop                NMTCommand = 0x02 // enter stopped
	NMTEnterPreOperational NMTCommand = 0x80
	NMTResetNode           NMTCommand = 0x81 // reset application
	NMTResetCommunication  NMTCommand = 0x82
)

var nmtCommandNames = map[NMTCommand]string{
	NMTStart:               "start",
	NMTStop:                "stop",
	NMTEnterPreOperational: "pre-operational",
	NMTResetNode:           "reset-node",
	NMTResetCommunication:  "reset-communication",
}

// String names the command.
func (c NMTCommand) String() string {
	if n, ok := nmtCommandNames[c]; ok {
		return n
	}
	return fmt.Sprintf("NMTCommand(0x%02X)", uint8(c))
}

// Valid reports whether c is one of the five NMT commands.
func (c NMTCommand) Valid() bool { _, ok := nmtCommandNames[c]; return ok }

// ParseNMTCommand parses a command name as String returns it.
func ParseNMTCommand(s string) (NMTCommand, error) {
	for c, n := range nmtCommandNames {
		if n == s {
			return c, nil
		}
	}
	return 0, fmt.Errorf("canopen: unknown NMT command %q", s)
}
