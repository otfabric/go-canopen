// SPDX-License-Identifier: MIT

package master

import (
	"context"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/eds"
)

// DCFEntries returns the configuration a DCF holds for a node, as entries
// for [Master.Configure]: every writable sub-object with a ParameterValue
// (without the "refuse write on download" object flag), in index order,
// with node-ID-relative values evaluated for node (0: the DCF's
// DeviceComissioning node-ID). For a PDO with any configured sub-object
// the whole communication and mapping parameter is included, so that
// Configure applies the PDO as the DCF describes it. It is
// [cdcf.Entries].
func DCFEntries(f *eds.File, node canopen.NodeID) ([]Entry, error) {
	return cdcf.Entries(f, node)
}

// ConfigureFromDCF writes the configuration of a DCF to a node
// ([DCFEntries], [Master.Configure]). node 0 takes the DCF's node-ID.
func (m *Master) ConfigureFromDCF(ctx context.Context, node canopen.NodeID, f *eds.File) error {
	entries, err := DCFEntries(f, node)
	if err != nil {
		return err
	}
	if node == 0 {
		node = canopen.NodeID(f.DeviceComissioning.NodeID)
	}
	return m.Configure(ctx, node, entries)
}
