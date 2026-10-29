// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/cdcf"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// cdcfValues are configuration entries (index, sub-index, integer value)
// of the shared device: a concise DCF of them by the stack's
// configuration tool (cdcf --generator dcfgen) is compared byte for byte
// with go-canopen's (canopen.ParseValue with the EDS data types,
// cdcf.Encode).
var cdcfValues = []struct {
	index uint16
	sub   uint8
	value string
}{
	{0x2001, 0, "17493"}, {0x1017, 0, "300"}, {0x2003, 0, "-2"}, {0x2000, 0, "127"},
	{0x2007, 0, "1234605616436508552"}, {0x200A, 0, "150"}, {0x2006, 0, "2"},
}

// cdcfRanges are the index ranges of the dictionary dumps compared.
var cdcfRanges = [][2]uint16{{0x1000, 0x1FFF}, {0x2000, 0x9FFF}}

// TestConciseDCF: each stack's concise DCF generator (cdcf) against
// go-canopen's cdcf package: the configuration entries of the stack's
// configuration tool (dcfgen) and the dictionary of a DCF dumped as a
// concise DCF (objects), byte for byte.
func TestConciseDCF(t *testing.T) {
	for _, a := range adapters {
		t.Run(a, func(t *testing.T) {
			c := needImage(t, a)
			dir := workDir(t)
			sc := func(name string, fn func(t *testing.T)) { scenario(t, dirEDS, a, name, fn) }
			need := func(t *testing.T, feature string) {
				if !c.op("cdcf") || !c.Features[feature] {
					skipf(t, "%s has no concise DCF generator (cdcf, %s false)", a, feature)
				}
			}
			stack := func(t *testing.T, args ...string) []byte {
				t.Helper()
				r := adapterRun(t, "", a, append([]string{"cdcf"}, args...)...)
				data, _ := r.doc["data"].(string)
				b, err := hex.DecodeString(data)
				if r.code != 0 || err != nil {
					failf(t, "cdcf: %v", r)
				}
				return b
			}
			sc("cdcf-dcfgen", func(t *testing.T) {
				need(t, "cdcfDcfgen")
				f, err := eds.ParseFile(fixture(t, a, "eds/interop-device.eds", dir))
				if err != nil {
					t.Fatal(err)
				}
				d, err := f.Dictionary(deviceNode)
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"--dcf", deviceEDS, "--node-id", fmt.Sprint(deviceNode), "--generator", "dcfgen"}
				var entries []cdcf.Entry
				for _, v := range cdcfValues {
					args = append(args, "--sdo", fmt.Sprintf("0x%04X:%02X=%s", v.index, v.sub, v.value))
					vr, err := d.Variable(v.index, v.sub)
					if err != nil {
						t.Fatal(err)
					}
					b, err := canopen.ParseValue(vr.DataType, v.value, deviceNode)
					if err != nil {
						t.Fatal(err)
					}
					entries = append(entries, cdcf.Entry{Index: v.index, SubIndex: v.sub, Data: b})
				}
				ours, err := cdcf.Encode(entries)
				if err != nil {
					t.Fatal(err)
				}
				theirs := stack(t, args...)
				if !bytes.Equal(ours, theirs) {
					dec, derr := cdcf.Decode(theirs)
					differ(t, "%s: %x (decoded %v, %v); go-canopen: %x (%v)", a, theirs, dec, derr, ours, entries)
				}
				info(t).detail = fmt.Sprintf("%d entries, %d bytes identical", len(entries), len(ours))
			})
			for _, rg := range cdcfRanges {
				rg := rg
				sc(fmt.Sprintf("cdcf-objects-%04X-%04X", rg[0], rg[1]), func(t *testing.T) {
					need(t, "cdcfObjects")
					f, err := eds.ParseFile(fixture(t, a, "eds/interop-device-node5.dcf", dir))
					if err != nil {
						t.Fatal(err)
					}
					d, err := f.Dictionary(deviceNode)
					if err != nil {
						t.Fatal(err)
					}
					ours := dictionaryEntries(t, d, rg[0], rg[1])
					theirs := stack(t, "--dcf", "/fixtures/eds/interop-device-node5.dcf", "--node-id", fmt.Sprint(deviceNode),
						"--generator", "objects", "--range", fmt.Sprintf("0x%04X-0x%04X", rg[0], rg[1]))
					dec, err := cdcf.Decode(theirs)
					if err != nil {
						failf(t, "cdcf.Decode of %s's concise DCF: %v", a, err)
					}
					ob, err := cdcf.Encode(ours)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(ob, theirs) {
						differ(t, "%d entries from %s, %d from go-canopen; first differences: %s", len(dec), a, len(ours), entryDiff(ours, dec, 6))
					}
					info(t).detail = fmt.Sprintf("%d sub-objects, %d bytes identical", len(ours), len(ob))
				})
			}
		})
	}
}

// dictionaryEntries are the values of every sub-object of d with an
// index in [first, last], in index and sub-index order.
func dictionaryEntries(t *testing.T, d *od.Dictionary, first, last uint16) []cdcf.Entry {
	t.Helper()
	var out []cdcf.Entry
	objs := d.Objects()
	sort.Slice(objs, func(i, j int) bool { return objs[i].Index < objs[j].Index })
	for _, o := range objs {
		if o.Index < first || o.Index > last {
			continue
		}
		subs := append([]*od.Variable(nil), o.Subs...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].SubIndex < subs[j].SubIndex })
		for _, v := range subs {
			b, err := d.Get(v.Index, v.SubIndex)
			if err != nil {
				t.Fatalf("0x%04X:%02X: %v", v.Index, v.SubIndex, err)
			}
			out = append(out, cdcf.Entry{Index: v.Index, SubIndex: v.SubIndex, Data: b})
		}
	}
	return out
}

// entryDiff describes the first n differences of two entry lists.
func entryDiff(ours, theirs []cdcf.Entry, n int) string {
	var s []string
	for i := 0; i < max(len(ours), len(theirs)) && len(s) < n; i++ {
		var o, x string
		if i < len(ours) {
			o = ours[i].String()
		}
		if i < len(theirs) {
			x = theirs[i].String()
		}
		if o != x {
			s = append(s, fmt.Sprintf("#%d go-canopen %q, stack %q", i, o, x))
		}
	}
	return fmt.Sprint(s)
}
