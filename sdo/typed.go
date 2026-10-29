// SPDX-License-Identifier: MIT

package sdo

import (
	"context"
	"fmt"

	"github.com/otfabric/go-canopen"
)

// ReadValue uploads a sub-object and decodes it as dt ([canopen.Decode]):
// bool, int64, uint64, float32, float64, string or []byte. A value of the
// wrong length for dt is an error matching [canopen.ErrDataType].
func (c *Client) ReadValue(ctx context.Context, index uint16, sub uint8, dt canopen.DataType, opts ...CallOption) (any, error) {
	b, err := c.Upload(ctx, index, sub, opts...)
	if err != nil {
		return nil, err
	}
	v, err := canopen.Decode(dt, b)
	if err != nil {
		return nil, fmt.Errorf("sdo: 0x%04X:%02X: %w", index, sub, err)
	}
	return v, nil
}

// WriteValue encodes v as dt ([canopen.Encode]) and downloads it.
func (c *Client) WriteValue(ctx context.Context, index uint16, sub uint8, dt canopen.DataType, v any, opts ...CallOption) error {
	b, err := canopen.Encode(dt, v)
	if err != nil {
		return fmt.Errorf("sdo: 0x%04X:%02X: %w", index, sub, err)
	}
	return c.Download(ctx, index, sub, b, opts...)
}

// ReadUint uploads an unsigned integer of 1..8 bytes (little endian), of
// whatever width the server sends.
func (c *Client) ReadUint(ctx context.Context, index uint16, sub uint8) (uint64, error) {
	b, err := c.Upload(ctx, index, sub)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 || len(b) > 8 {
		return 0, fmt.Errorf("sdo: 0x%04X:%02X: %w: %d bytes are no integer", index, sub, canopen.ErrDataType, len(b))
	}
	return canopen.DecodeUint(b), nil
}

// ReadInt uploads a signed integer of 1..8 bytes (little endian, two's
// complement), sign-extended from the width the server sends.
func (c *Client) ReadInt(ctx context.Context, index uint16, sub uint8) (int64, error) {
	b, err := c.Upload(ctx, index, sub)
	if err != nil {
		return 0, err
	}
	if len(b) < 1 || len(b) > 8 {
		return 0, fmt.Errorf("sdo: 0x%04X:%02X: %w: %d bytes are no integer", index, sub, canopen.ErrDataType, len(b))
	}
	return canopen.DecodeInt(b), nil
}

// ReadString uploads a VISIBLE_STRING, without trailing NUL bytes.
func (c *Client) ReadString(ctx context.Context, index uint16, sub uint8) (string, error) {
	v, err := c.ReadValue(ctx, index, sub, canopen.VisibleString)
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// WriteUint downloads x as an unsigned integer of size bytes (1..8),
// range-checked.
func (c *Client) WriteUint(ctx context.Context, index uint16, sub uint8, x uint64, size int) error {
	dt, ok := uintTypes[size]
	if !ok {
		return fmt.Errorf("sdo: %w: no unsigned integer of %d bytes", canopen.ErrDataType, size)
	}
	return c.WriteValue(ctx, index, sub, dt, x)
}

// WriteUint8 downloads an UNSIGNED8.
func (c *Client) WriteUint8(ctx context.Context, index uint16, sub uint8, x uint8) error {
	return c.Download(ctx, index, sub, []byte{x})
}

// WriteUint16 downloads an UNSIGNED16.
func (c *Client) WriteUint16(ctx context.Context, index uint16, sub uint8, x uint16) error {
	return c.Download(ctx, index, sub, []byte{byte(x), byte(x >> 8)})
}

// WriteUint32 downloads an UNSIGNED32.
func (c *Client) WriteUint32(ctx context.Context, index uint16, sub uint8, x uint32) error {
	return c.Download(ctx, index, sub, []byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)})
}

var uintTypes = map[int]canopen.DataType{
	1: canopen.Unsigned8, 2: canopen.Unsigned16, 3: canopen.Unsigned24, 4: canopen.Unsigned32,
	5: canopen.Unsigned40, 6: canopen.Unsigned48, 7: canopen.Unsigned56, 8: canopen.Unsigned64,
}
