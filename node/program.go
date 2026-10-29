// SPDX-License-Identifier: MIT

package node

import (
	"fmt"

	"github.com/otfabric/go-canopen/program"
)

// installProgram attaches the program download objects of [WithProgram].
func (n *Node) installProgram() error {
	if n.cfg.program == nil {
		return nil
	}
	cfg := *n.cfg.program
	cfg.State = n.slave.State
	dev, err := program.Attach(n.d, cfg)
	if err != nil {
		return fmt.Errorf("node: %w", err)
	}
	n.program = dev
	n.unhook = append(n.unhook, dev.Close)
	return nil
}

// Program returns the program download device of [WithProgram], or nil.
func (n *Node) Program() *program.Device { return n.program }
