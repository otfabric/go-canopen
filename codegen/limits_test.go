// SPDX-License-Identifier: MIT

package codegen

import (
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// TestGenerateSubObjectLimit: Generate refuses a file that describes more
// sub-objects than a dictionary is built with (65536, compact storage
// counted expanded).
func TestGenerateSubObjectLimit(t *testing.T) {
	f, err := eds.ParseFile("../eds/testdata/compact.eds")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 258 { // 258 × 255 sub-objects
		f.Objects = append(f.Objects, &eds.Object{Index: 0x4000 + uint16(i), Entry: eds.Entry{
			ParameterName: "Array", ObjectType: od.ObjectArray, DataType: canopen.Unsigned8,
			AccessType: od.AccessRW, CompactSubObj: 254,
		}})
	}
	f.SortObjects()
	f.UpdateObjectLists()
	if _, err := Generate(f, Options{Package: "big"}); err == nil || !strings.Contains(err.Error(), "sub-objects") {
		t.Fatalf("Generate = %v, want the sub-object limit", err)
	}
}
