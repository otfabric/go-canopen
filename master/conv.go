package master

import (
	"fmt"
	"math"

	"github.com/otfabric/go-canopen"
)

// u8Of, u16Of and u32Of narrow a decoded unsigned integer; ok is false
// when the value does not fit.
func u8Of(v uint64) (x uint8, ok bool) {
	if v > math.MaxUint8 {
		return 0, false
	}
	return uint8(v), true
}

func u16Of(v uint64) (x uint16, ok bool) {
	if v > math.MaxUint16 {
		return 0, false
	}
	return uint16(v), true
}

func u32Of(v uint64) (x uint32, ok bool) {
	if v > math.MaxUint32 {
		return 0, false
	}
	return uint32(v), true
}

// toU32 narrows the result of a read of an UNSIGNED32: a value wider
// than 32 bits (a device answering with more bytes than the type has)
// is [canopen.ErrDataType] instead of being truncated.
func toU32(v uint64, err error) (uint32, error) {
	if err != nil {
		return 0, err
	}
	x, ok := u32Of(v)
	if !ok {
		return 0, fmt.Errorf("%w: 0x%X is no UNSIGNED32", canopen.ErrDataType, v)
	}
	return x, nil
}
