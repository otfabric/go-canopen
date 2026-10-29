// SPDX-License-Identifier: MIT

package canopen

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// DataType is a CANopen data type index (CiA 301 7.4.7.1, the objects at
// 0x0001..0x0025 of the object dictionary).
type DataType uint16

// Static data types. Multi-byte values are little-endian on the bus.
const (
	Boolean        DataType = 0x0001
	Integer8       DataType = 0x0002
	Integer16      DataType = 0x0003
	Integer32      DataType = 0x0004
	Unsigned8      DataType = 0x0005
	Unsigned16     DataType = 0x0006
	Unsigned32     DataType = 0x0007
	Real32         DataType = 0x0008
	VisibleString  DataType = 0x0009
	OctetString    DataType = 0x000A
	UnicodeString  DataType = 0x000B
	TimeOfDay      DataType = 0x000C
	TimeDifference DataType = 0x000D
	Domain         DataType = 0x000F
	Integer24      DataType = 0x0010
	Real64         DataType = 0x0011
	Integer40      DataType = 0x0012
	Integer48      DataType = 0x0013
	Integer56      DataType = 0x0014
	Integer64      DataType = 0x0015
	Unsigned24     DataType = 0x0016
	Unsigned40     DataType = 0x0018
	Unsigned48     DataType = 0x0019
	Unsigned56     DataType = 0x001A
	Unsigned64     DataType = 0x001B

	// Complex (structured) types of the communication profile.
	PDOCommParam DataType = 0x0020
	PDOMapping   DataType = 0x0021
	SDOParam     DataType = 0x0022
	Identity     DataType = 0x0023
)

type typeInfo struct {
	name   string
	size   int // bytes; 0 for variable length
	signed bool
	float  bool
}

var types = map[DataType]typeInfo{
	Boolean:        {"BOOLEAN", 1, false, false},
	Integer8:       {"INTEGER8", 1, true, false},
	Integer16:      {"INTEGER16", 2, true, false},
	Integer24:      {"INTEGER24", 3, true, false},
	Integer32:      {"INTEGER32", 4, true, false},
	Integer40:      {"INTEGER40", 5, true, false},
	Integer48:      {"INTEGER48", 6, true, false},
	Integer56:      {"INTEGER56", 7, true, false},
	Integer64:      {"INTEGER64", 8, true, false},
	Unsigned8:      {"UNSIGNED8", 1, false, false},
	Unsigned16:     {"UNSIGNED16", 2, false, false},
	Unsigned24:     {"UNSIGNED24", 3, false, false},
	Unsigned32:     {"UNSIGNED32", 4, false, false},
	Unsigned40:     {"UNSIGNED40", 5, false, false},
	Unsigned48:     {"UNSIGNED48", 6, false, false},
	Unsigned56:     {"UNSIGNED56", 7, false, false},
	Unsigned64:     {"UNSIGNED64", 8, false, false},
	Real32:         {"REAL32", 4, false, true},
	Real64:         {"REAL64", 8, false, true},
	VisibleString:  {"VISIBLE_STRING", 0, false, false},
	OctetString:    {"OCTET_STRING", 0, false, false},
	UnicodeString:  {"UNICODE_STRING", 0, false, false},
	TimeOfDay:      {"TIME_OF_DAY", 6, false, false},
	TimeDifference: {"TIME_DIFFERENCE", 6, false, false},
	Domain:         {"DOMAIN", 0, false, false},
	PDOCommParam:   {"PDO_COMMUNICATION_PARAMETER", 0, false, false},
	PDOMapping:     {"PDO_MAPPING", 0, false, false},
	SDOParam:       {"SDO_PARAMETER", 0, false, false},
	Identity:       {"IDENTITY", 0, false, false},
}

// Known reports whether t is a data type this package knows.
func (t DataType) Known() bool { _, ok := types[t]; return ok }

// String returns the CiA 301 name, e.g. "UNSIGNED32".
func (t DataType) String() string {
	if i, ok := types[t]; ok {
		return i.name
	}
	return fmt.Sprintf("DataType(0x%04X)", uint16(t))
}

// ParseDataType parses a CiA 301 type name ("UNSIGNED32") or a type index
// ("0x0007").
func ParseDataType(s string) (DataType, error) {
	s = strings.TrimSpace(s)
	for t, i := range types {
		if strings.EqualFold(i.name, s) {
			return t, nil
		}
	}
	v, err := ParseUint(s, 16)
	if err != nil {
		return 0, fmt.Errorf("canopen: unknown data type %q", s)
	}
	return DataType(v), nil
}

// Size returns the encoded size in bytes, 0 for variable-length types
// (strings, DOMAIN) and unknown types.
func (t DataType) Size() int { return types[t].size }

// Bits returns the size in bits of a value of the type in a PDO: 1 for
// BOOLEAN (CiA 301 encodes it as one bit), 8 times Size otherwise, 0 for
// variable-length types.
func (t DataType) Bits() int {
	if t == Boolean {
		return 1
	}
	return types[t].size * 8
}

// IsInteger reports whether t is a signed or unsigned integer type (not
// BOOLEAN).
func (t DataType) IsInteger() bool {
	i, ok := types[t]
	return ok && t != Boolean && i.size > 0 && !i.float && t != TimeOfDay && t != TimeDifference
}

// IsSigned reports whether t is a signed integer type.
func (t DataType) IsSigned() bool { return types[t].signed }

// IsFloat reports whether t is REAL32 or REAL64.
func (t DataType) IsFloat() bool { return types[t].float }

// IsVariable reports whether values of t have no fixed size (VISIBLE_STRING,
// OCTET_STRING, UNICODE_STRING, DOMAIN).
func (t DataType) IsVariable() bool {
	return t == VisibleString || t == OctetString || t == UnicodeString || t == Domain
}

// Encode converts a Go value to the bus encoding of t. Integers accept any
// Go integer type (range-checked), floats float32/float64, BOOLEAN bool or
// an integer 0/1, strings string, OCTET_STRING and DOMAIN []byte or a
// string. A []byte is accepted for every type when its length matches.
func Encode(t DataType, v any) ([]byte, error) {
	if b, ok := v.([]byte); ok {
		if n := t.Size(); n != 0 && len(b) != n {
			return nil, fmt.Errorf("%w: %s needs %d bytes, got %d", ErrDataType, t, n, len(b))
		}
		return append([]byte(nil), b...), nil
	}
	switch t {
	case VisibleString:
		if s, ok := v.(string); ok {
			return []byte(s), nil
		}
	case OctetString, Domain:
		if s, ok := v.(string); ok {
			return []byte(s), nil
		}
	case UnicodeString:
		if s, ok := v.(string); ok {
			u := utf16.Encode([]rune(s))
			b := make([]byte, 2*len(u))
			for i, r := range u {
				binary.LittleEndian.PutUint16(b[2*i:], r)
			}
			return b, nil
		}
	case Boolean:
		switch x := v.(type) {
		case bool:
			if x {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
	case Real32:
		switch x := v.(type) {
		case float32:
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(x)), nil
		case float64:
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(x))), nil
		}
	case Real64:
		switch x := v.(type) {
		case float32:
			return binary.LittleEndian.AppendUint64(nil, math.Float64bits(float64(x))), nil
		case float64:
			return binary.LittleEndian.AppendUint64(nil, math.Float64bits(x)), nil
		}
	}
	if t.IsInteger() || t == Boolean {
		i, u, neg, ok := toInt(v)
		if !ok {
			return nil, fmt.Errorf("%w: cannot encode %T as %s", ErrDataType, v, t)
		}
		return EncodeInt(t, i, u, neg)
	}
	return nil, fmt.Errorf("%w: cannot encode %T as %s", ErrDataType, v, t)
}

// toInt splits a Go integer into its signed and unsigned readings.
func toInt(v any) (i int64, u uint64, neg, ok bool) {
	switch x := v.(type) {
	case int:
		return int64(x), uint64(x), x < 0, true
	case int8:
		return int64(x), uint64(x), x < 0, true
	case int16:
		return int64(x), uint64(x), x < 0, true
	case int32:
		return int64(x), uint64(x), x < 0, true
	case int64:
		return x, uint64(x), x < 0, true
	case uint:
		return int64(x), uint64(x), false, true
	case uint8:
		return int64(x), uint64(x), false, true
	case uint16:
		return int64(x), uint64(x), false, true
	case uint32:
		return int64(x), uint64(x), false, true
	case uint64:
		return int64(x), x, false, true
	}
	return 0, 0, false, false
}

// EncodeInt encodes an integer of type t, with a range check. neg tells
// whether the value is negative (then i holds it); otherwise u holds it.
func EncodeInt(t DataType, i int64, u uint64, neg bool) ([]byte, error) {
	n := t.Size()
	if n == 0 {
		return nil, fmt.Errorf("%w: %s is not an integer type", ErrDataType, t)
	}
	bits := uint(8 * n)
	if t.IsSigned() {
		lo, hi := -(int64(1) << (bits - 1)), int64(1)<<(bits-1)-1
		if bits == 64 {
			lo, hi = math.MinInt64, math.MaxInt64
		}
		if neg {
			if i < lo {
				return nil, fmt.Errorf("%w: %d out of range of %s", ErrDataType, i, t)
			}
			u = uint64(i)
		} else if u > uint64(hi) {
			return nil, fmt.Errorf("%w: %d out of range of %s", ErrDataType, u, t)
		}
	} else {
		if neg {
			return nil, fmt.Errorf("%w: %d out of range of %s", ErrDataType, i, t)
		}
		if bits < 64 && u >= uint64(1)<<bits {
			return nil, fmt.Errorf("%w: %d out of range of %s", ErrDataType, u, t)
		}
		if t == Boolean && u > 1 {
			return nil, fmt.Errorf("%w: %d is not a BOOLEAN", ErrDataType, u)
		}
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], u)
	return append([]byte(nil), b[:n]...), nil
}

// Decode converts the bus encoding of t to a Go value: bool for BOOLEAN,
// int64 for signed and uint64 for unsigned integers of every width,
// float32/float64 for REAL32/REAL64, string for VISIBLE_STRING and
// UNICODE_STRING, []byte for everything else.
func Decode(t DataType, b []byte) (any, error) {
	if n := t.Size(); n != 0 && len(b) != n {
		return nil, fmt.Errorf("%w: %s needs %d bytes, got %d", ErrDataType, t, n, len(b))
	}
	switch {
	case t == Boolean:
		return b[0] != 0, nil
	case t == Real32:
		return math.Float32frombits(binary.LittleEndian.Uint32(b)), nil
	case t == Real64:
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
	case t == VisibleString:
		return strings.TrimRight(string(b), "\x00"), nil
	case t == UnicodeString:
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		}
		return strings.TrimRight(string(utf16.Decode(u)), "\x00"), nil
	case t.IsSigned():
		return DecodeInt(b), nil
	case t.IsInteger():
		return DecodeUint(b), nil
	}
	return append([]byte(nil), b...), nil
}

// DecodeUint decodes a little-endian unsigned integer of 1..8 bytes.
func DecodeUint(b []byte) uint64 {
	var x [8]byte
	copy(x[:], b)
	return binary.LittleEndian.Uint64(x[:])
}

// DecodeInt decodes a little-endian two's complement integer of 1..8 bytes.
func DecodeInt(b []byte) int64 {
	if len(b) == 0 {
		return 0
	}
	u := DecodeUint(b)
	shift := uint(64 - 8*min(len(b), 8))
	return int64(u<<shift) >> shift
}

// ParseUint parses an unsigned integer literal as EDS files write them:
// decimal, hexadecimal with 0x, or octal with a leading 0 (CiA 306 4.6.3).
func ParseUint(s string, bits int) (uint64, error) {
	s = strings.TrimSpace(s)
	base := 10
	switch {
	case len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X"):
		s, base = s[2:], 16
	case len(s) > 1 && s[0] == '0':
		s, base = s[1:], 8
	}
	return strconv.ParseUint(s, base, bits)
}

// ParseInt parses a signed integer literal (decimal, 0x hexadecimal, or
// octal with a leading 0; an optional sign). A hexadecimal literal may
// also give the two's complement pattern of a negative value, as EDS
// tools write them ("0xFFFFFFFF" for an INTEGER32 -1).
func ParseInt(s string, bits int) (int64, error) {
	s = strings.TrimSpace(s)
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg, s = s[0] == '-', s[1:]
	}
	if !neg && len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		u, err := strconv.ParseUint(s[2:], 16, bits)
		if err != nil {
			return 0, err
		}
		shift := uint(64 - bits)
		return int64(u<<shift) >> shift, nil
	}
	u, err := ParseUint(s, 64)
	if err != nil {
		return 0, err
	}
	limit := uint64(1) << uint(bits-1)
	if neg {
		if u > limit {
			return 0, &strconv.NumError{Func: "ParseInt", Num: s, Err: strconv.ErrRange}
		}
		return -int64(u), nil
	}
	if u >= limit {
		return 0, &strconv.NumError{Func: "ParseInt", Num: s, Err: strconv.ErrRange}
	}
	return int64(u), nil
}

// ParseValue parses a value literal of type t as EDS and DCF files write
// them (CiA 306): integers in decimal, 0x hexadecimal or octal, optionally
// with $NODEID added ("$NODEID+0x180", "0x180+$NODEID"), which evaluates
// to node; floats in decimal notation; VISIBLE_STRING as the text itself;
// OCTET_STRING and DOMAIN as hexadecimal digit pairs (spaces allowed);
// UNICODE_STRING as text. An empty literal of an integer or float type is
// zero.
func ParseValue(t DataType, s string, node NodeID) ([]byte, error) {
	switch t {
	case VisibleString, UnicodeString:
		return Encode(t, s)
	case OctetString, Domain:
		h := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, s)
		h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("%w: %s literal %q: %v", ErrDataType, t, s, err)
		}
		return b, nil
	case TimeOfDay, TimeDifference:
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
		if err == nil && len(b) == 6 {
			return b, nil
		}
		if strings.TrimSpace(s) == "" || strings.TrimSpace(s) == "0" {
			return make([]byte, 6), nil
		}
		return nil, fmt.Errorf("%w: %s literal %q", ErrDataType, t, s)
	case Real32, Real64:
		s = strings.TrimSpace(s)
		if s == "" {
			s = "0"
		}
		var f float64
		var err error
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			// The raw IEEE 754 pattern.
			var u uint64
			u, err = strconv.ParseUint(s[2:], 16, 64)
			if err == nil {
				if t == Real32 {
					return binary.LittleEndian.AppendUint32(nil, uint32(u)), nil
				}
				return binary.LittleEndian.AppendUint64(nil, u), nil
			}
		} else {
			f, err = strconv.ParseFloat(s, 64)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s literal %q", ErrDataType, t, s)
		}
		return Encode(t, f)
	}
	if !t.IsInteger() && t != Boolean {
		return nil, fmt.Errorf("%w: cannot parse a %s literal", ErrDataType, t)
	}
	expr, plus := strings.TrimSpace(s), uint64(0)
	if strings.Contains(strings.ToUpper(expr), "$NODEID") {
		up := strings.ToUpper(expr)
		i := strings.Index(up, "$NODEID")
		rest := strings.TrimSpace(expr[:i] + expr[i+len("$NODEID"):])
		rest = strings.TrimSpace(strings.Trim(rest, "+"))
		rest = strings.TrimSpace(rest)
		plus = uint64(node)
		expr = rest
		if expr == "" {
			expr = "0"
		}
	}
	if expr == "" {
		expr = "0"
	}
	if t.IsSigned() {
		v, err := ParseInt(expr, 8*t.Size())
		if err != nil {
			return nil, fmt.Errorf("%w: %s literal %q: %v", ErrDataType, t, s, err)
		}
		v += int64(plus)
		return EncodeInt(t, v, uint64(v), v < 0)
	}
	v, err := ParseUint(expr, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %s literal %q: %v", ErrDataType, t, s, err)
	}
	return EncodeInt(t, 0, v+plus, false)
}

// FormatValue formats an encoded value of type t for display: decimal for
// signed integers and floats, 0x hexadecimal for unsigned integers, the
// text for strings, hexadecimal bytes otherwise.
func FormatValue(t DataType, b []byte) string {
	v, err := Decode(t, b)
	if err != nil {
		return hex.EncodeToString(b)
	}
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return fmt.Sprintf("0x%0*X", 2*t.Size(), x)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return strconv.Quote(x)
	case []byte:
		return hex.EncodeToString(x)
	}
	return fmt.Sprint(v)
}
