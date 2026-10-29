// SPDX-License-Identifier: MIT

package master

import (
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cia402"
)

// Drive returns a CiA 402 controller for the drive at a node, using the
// node's SDO client ([Master.SDO]); opts may be nil. An invalid node-ID
// surfaces as the error of the controller's first operation.
func (m *Master) Drive(node canopen.NodeID, opts *cia402.ControllerOptions) *cia402.Controller {
	return cia402.NewController(sdoFuncs{m, node}, opts)
}
