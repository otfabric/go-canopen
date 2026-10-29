// SPDX-License-Identifier: MIT

package cdcf_test

import (
	"fmt"

	"github.com/otfabric/go-canopen/cdcf"
)

func ExampleEncode() {
	b, _ := cdcf.Encode([]cdcf.Entry{{Index: 0x1017, SubIndex: 0, Data: []byte{0xE8, 0x03}}})
	fmt.Printf("% X\n", b)
	entries, _ := cdcf.Decode(b)
	fmt.Println(entries)
	// Output:
	// 01 00 00 00 17 10 00 02 00 00 00 E8 03
	// [1017:00=E8 03]
}

func ExamplePDOSequence() {
	for _, e := range cdcf.PDOSequence([]cdcf.Entry{
		{Index: 0x1800, SubIndex: 1, Data: []byte{0x85, 0x01, 0, 0}},
		{Index: 0x1A00, SubIndex: 0, Data: []byte{1}},
		{Index: 0x1A00, SubIndex: 1, Data: []byte{0x08, 0, 0, 0x20}},
	}) {
		fmt.Println(e)
	}
	// Output:
	// 1800:01=85 01 00 80
	// 1A00:00=00
	// 1A00:01=08 00 00 20
	// 1A00:00=01
	// 1800:01=85 01 00 00
}
