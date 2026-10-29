// SPDX-License-Identifier: MIT

package master

import (
	"context"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/sdo"
)

// sdoFuncs adapts the SDO client of a node to the two-method SDO
// interfaces of the pdo and cia402 packages. The client is looked up on
// every call, so a failure to create it surfaces as the call's error.
type sdoFuncs struct {
	m    *Master
	node canopen.NodeID
}

func (s sdoFuncs) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	c, err := s.m.SDO(s.node)
	if err != nil {
		return nil, err
	}
	return c.Upload(ctx, index, sub)
}

func (s sdoFuncs) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	c, err := s.m.SDO(s.node)
	if err != nil {
		return err
	}
	return c.Download(ctx, index, sub, data)
}

// client adapts one client.
type client struct{ c *sdo.Client }

func (s client) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	return s.c.Upload(ctx, index, sub)
}

func (s client) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	return s.c.Download(ctx, index, sub, data)
}
