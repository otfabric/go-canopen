// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/sdo"
	"github.com/otfabric/go-canopen/usdo"
)

// remote is the access to a node's object dictionary the master's
// procedures use: over the node's SDO client, or with [WithFD] over the
// master's USDO client.
type remote interface {
	Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error)
	Download(ctx context.Context, index uint16, sub uint8, data []byte) error
}

// readUint uploads an unsigned integer of 1..8 bytes (as sdo.Client's
// ReadUint).
func readUint(ctx context.Context, r remote, index uint16, sub uint8) (uint64, error) {
	b, err := r.Upload(ctx, index, sub)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 || len(b) > 8 {
		return 0, fmt.Errorf("master: 0x%04X:%02X: %w: %d bytes are no integer", index, sub, canopen.ErrDataType, len(b))
	}
	return canopen.DecodeUint(b), nil
}

// remote returns the access to a node's dictionary: its SDO client, or
// with WithFD the USDO client addressed to it.
func (m *Master) remote(node canopen.NodeID) (remote, error) {
	if m.cfg.fd {
		if err := node.Check(); err != nil {
			return nil, err
		}
		c, err := m.USDO()
		if err != nil {
			return nil, err
		}
		return usdoRemote{c: c, node: node}, nil
	}
	c, err := m.SDO(node)
	if err != nil {
		return nil, err
	}
	return client{c}, nil
}

// sdoFuncs adapts the access to a node to the two-method SDO interfaces
// of the pdo and cia402 packages. The access is looked up on every call,
// so a failure to create it surfaces as the call's error.
type sdoFuncs struct {
	m    *Master
	node canopen.NodeID
}

func (s sdoFuncs) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	r, err := s.m.remote(s.node)
	if err != nil {
		return nil, err
	}
	return r.Upload(ctx, index, sub)
}

func (s sdoFuncs) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	r, err := s.m.remote(s.node)
	if err != nil {
		return err
	}
	return r.Download(ctx, index, sub, data)
}

// client adapts one SDO client.
type client struct{ c *sdo.Client }

func (s client) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	return s.c.Upload(ctx, index, sub)
}

func (s client) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	return s.c.Download(ctx, index, sub, data)
}

// usdoRemote addresses one server with the master's USDO client. Its
// errors are those of an SDO client: a USDO abort is reported as a
// *canopen.AbortError with the corresponding SDO abort code (wrapping
// the *usdo.AbortError), so that the procedures treat both alike.
type usdoRemote struct {
	c       *usdo.Client
	node    canopen.NodeID
	timeout time.Duration // 0: the client's
	bulk    bool          // bulk transfer for large downloads
}

func (u usdoRemote) opts() []usdo.CallOption {
	var o []usdo.CallOption
	if u.timeout > 0 {
		o = append(o, usdo.WithCallTimeout(u.timeout))
	}
	if u.bulk {
		o = append(o, usdo.UseBulk(true))
	}
	return o
}

func (u usdoRemote) Upload(ctx context.Context, index uint16, sub uint8) ([]byte, error) {
	b, err := u.c.Upload(ctx, u.node, index, sub, u.opts()...)
	return b, sdoError(err)
}

func (u usdoRemote) Download(ctx context.Context, index uint16, sub uint8, data []byte) error {
	return sdoError(u.c.Download(ctx, u.node, index, sub, data, u.opts()...))
}

// usdoAbort is a USDO abort seen as an SDO abort.
type usdoAbort struct {
	sdo  *canopen.AbortError
	orig *usdo.AbortError
}

func (e *usdoAbort) Error() string   { return e.orig.Error() }
func (e *usdoAbort) Unwrap() []error { return []error{e.sdo, e.orig} }

// sdoError converts a USDO abort into an error that also unwraps to a
// *canopen.AbortError.
func sdoError(err error) error {
	var ae *usdo.AbortError
	if err == nil || !errors.As(err, &ae) {
		return err
	}
	return &usdoAbort{
		sdo:  &canopen.AbortError{Code: ae.Code.SDO(), Index: ae.Index, SubIndex: ae.SubIndex, Remote: ae.Remote},
		orig: ae,
	}
}
