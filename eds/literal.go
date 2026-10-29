// SPDX-License-Identifier: MIT

package eds

import (
	"fmt"
	"strings"

	"github.com/otfabric/go-canopen"
)

const nodeIDToken = "$NODEID"

// indexFoldASCII returns the index of the first ASCII case-insensitive
// occurrence of tok in s, or -1. It never changes byte offsets, unlike
// strings.ToUpper on text that is not valid UTF-8.
func indexFoldASCII(s, tok string) int {
	for i := 0; i+len(tok) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(tok)], tok) {
			return i
		}
	}
	return -1
}

// parseLiteral parses a value literal of type t. A literal of an integer
// type may contain one $NODEID term ("$NODEID", "$NODEID+X", "X+$NODEID",
// any case, blanks allowed); then rel is true and the result is X plus
// node (X alone for node 0). Everything else goes to [canopen.ParseValue].
func parseLiteral(t canopen.DataType, lit string, node canopen.NodeID) (b []byte, rel bool, err error) {
	i := indexFoldASCII(lit, nodeIDToken)
	if i < 0 {
		if strings.Contains(strings.ToUpper(lit), nodeIDToken) {
			// A look-alike such as "$NODEıD" (dotless i): not a $NODEID
			// term, and not a number either.
			return nil, false, fmt.Errorf("%q is not valid for %s", lit, t)
		}
		b, err = canopen.ParseValue(t, lit, 0)
		if err != nil {
			return nil, false, fmt.Errorf("%q is not valid for %s", lit, t)
		}
		return b, false, nil
	}
	if !t.IsInteger() {
		return nil, true, fmt.Errorf("$NODEID in a %s value", t)
	}
	before, after := strings.TrimSpace(lit[:i]), strings.TrimSpace(lit[i+len(nodeIDToken):])
	var off string
	switch {
	case before == "" && after == "":
		off = "0"
	case before == "" && strings.HasPrefix(after, "+"):
		off = strings.TrimSpace(after[1:])
	case after == "" && strings.HasSuffix(before, "+"):
		off = strings.TrimSpace(before[:len(before)-1])
	}
	if off == "" || off[0] == '+' || off[0] == '-' || strings.Contains(strings.ToUpper(off), "$") {
		return nil, true, fmt.Errorf("%q is not a $NODEID expression (CiA 306: $NODEID+value)", lit)
	}
	b, err = canopen.ParseValue(t, off, 0)
	if err != nil {
		return nil, true, fmt.Errorf("%q is not valid for %s", lit, t)
	}
	if node == 0 {
		return b, true, nil
	}
	if t.IsSigned() {
		x := canopen.DecodeInt(b) + int64(node)
		b, err = canopen.EncodeInt(t, x, uint64(x), x < 0)
	} else {
		b, err = canopen.EncodeInt(t, 0, canopen.DecodeUint(b)+uint64(node), false)
	}
	if err != nil {
		return nil, true, fmt.Errorf("%q for node-ID %d is out of range of %s", lit, node, t)
	}
	return b, true, nil
}
