// SPDX-License-Identifier: MIT

package cdcf_test

import (
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// TestFromFileSubObjectLimit: a DCF that describes more sub-objects than
// a dictionary is built with (65536, compact storage counted expanded) is
// refused.
func TestFromFileSubObjectLimit(t *testing.T) {
	f, err := eds.ParseFile("../eds/testdata/node37.dcf")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 258 { // 258 × 255 sub-objects
		f.Objects = append(f.Objects, &eds.Object{Index: 0x4000 + uint16(i), Entry: eds.Entry{
			ParameterName: "A", ObjectType: od.ObjectArray, DataType: canopen.Unsigned8,
			AccessType: od.AccessRW, CompactSubObj: 254,
		}})
	}
	f.SortObjects()
	if _, err := cdcf.FromFile(f, 0); err == nil || !strings.Contains(err.Error(), "sub-objects") {
		t.Fatalf("FromFile = %v, want the sub-object limit", err)
	}
}
