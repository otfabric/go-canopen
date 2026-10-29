// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/od"
)

// interopFixtures are canopen-interop's EDS fixtures (in the images under
// /fixtures/eds).
var interopFixtures = []string{
	"interop-device.eds", "interop-device-node5.dcf", "compact-subobj.eds", "object-links.eds", "invalid-missing-datatype.eds",
	// Fixture schema 1.1.
	"mpdo-device.eds", "interop-device-other-product.eds",
}

// goFixtures are go-canopen's EDS/DCF test files (mounted on /work).
var goFixtures = []string{
	"compact.eds", "io401.eds", "drive402.eds", "node37.dcf", "broken.dcf",
	"third_party/python_canopen_sample.eds", "third_party/canopennode_ds301_profile.eds",
}

// stackObject is one sub-object of an eds-check result.
type stackObject struct {
	Index      string  `json:"index"`
	SubIndex   int     `json:"subindex"`
	Name       string  `json:"name"`
	DataType   *string `json:"dataType"`
	Access     *string `json:"access"`
	PDOMapping bool    `json:"pdoMapping"`
	Default    *string `json:"default"`
}

// TestEDSCheck: each stack's EDS checker against go-canopen's eds package
// on the same files: the verdict (valid or not) and the normalized object
// list (index, sub-index, data type, access, PDO mapping, the default
// literal evaluated by canopen.ParseValue against the dictionary's
// default).
func TestEDSCheck(t *testing.T) {
	work := workDir(t)
	for _, f := range goFixtures {
		b, err := os.ReadFile(filepath.Join("..", "eds", "testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range adapters {
		t.Run(a, func(t *testing.T) {
			c := needImage(t, a)
			local := workDir(t)
			type file struct{ name, local, inImage string }
			var files []file
			for _, f := range interopFixtures {
				files = append(files, file{f, fixture(t, a, "eds/"+f, local), "/fixtures/eds/" + f})
			}
			for _, f := range goFixtures {
				b := filepath.Base(f)
				files = append(files, file{b, filepath.Join(work, b), "/work/" + b})
			}
			for _, f := range files {
				f := f
				var res struct {
					Valid   bool          `json:"valid"`
					Errors  []string      `json:"errors"`
					Objects []stackObject `json:"objects"`
				}
				ran := false
				check := func(t *testing.T) bool {
					if !c.op("eds-check") {
						skipf(t, "%s has no eds-check", a)
					}
					if !ran {
						r := adapterRun(t, work, a, "eds-check", "--eds", f.inImage)
						if r.code != 0 || r.doc == nil {
							failf(t, "eds-check: %v", r)
						}
						b, _ := json.Marshal(r.doc)
						if err := json.Unmarshal(b, &res); err != nil {
							failf(t, "eds-check result: %v", err)
						}
						ran = true
					}
					return true
				}
				ourFile, ours, diags, perr := goView(f.local)
				oursValid := perr == nil && !eds.HasErrors(diags)
				scenario(t, dirEDS, a, "valid-"+f.name, func(t *testing.T) {
					check(t)
					info(t).detail = fmt.Sprintf("valid=%v", oursValid)
					if res.Valid != oursValid {
						var why []string
						if perr != nil {
							why = append(why, perr.Error())
						}
						for _, d := range diags {
							if d.Severity == eds.Error {
								why = append(why, d.String())
							}
						}
						differ(t, "%s: valid=%v %q; go-canopen valid=%v %q", a, res.Valid, res.Errors, oursValid, why)
					}
				})
				scenario(t, dirEDS, a, "objects-"+f.name, func(t *testing.T) {
					check(t)
					if !res.Valid || !oursValid {
						skipf(t, "not compared: the file is invalid for %s (valid=%v) or go-canopen (valid=%v)", a, res.Valid, oursValid)
					}
					d, dummies := compareObjects(ourFile, ours, res.Objects)
					if len(d) > 0 {
						differ(t, "%d differences: %s", len(d), strings.Join(d, "; "))
					}
					if len(dummies) > 0 {
						differKind(t, "dummy", "%s lists the data types %s as objects", a, strings.Join(dummies, ","))
					}
					info(t).detail = fmt.Sprintf("%d sub-objects", len(res.Objects))
				})
			}
		})
	}
}

// goView parses a file with go-canopen and builds its dictionary leniently
// (for the DCF's node-ID, else 1, as lely's checker).
func goView(path string) (*eds.File, *od.Dictionary, []eds.Diagnostic, error) {
	f, err := eds.ParseFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	diags := f.Validate()
	node := canopen.NodeID(1)
	if f.DeviceComissioning != nil && f.DeviceComissioning.NodeID >= 1 && f.DeviceComissioning.NodeID <= 127 {
		node = canopen.NodeID(f.DeviceComissioning.NodeID)
	}
	d, _, err := f.BuildDictionary(node, eds.BuildOptions{Lenient: true})
	if err != nil {
		return f, nil, diags, err
	}
	return f, d, diags, nil
}

// fileDefault returns the DefaultValue literal of a sub-object in the
// file, if it has a section of its own.
func fileDefault(f *eds.File, index uint16, sub uint8) (string, bool) {
	o := f.Object(index)
	if o == nil {
		return "", false
	}
	if len(o.Subs) == 0 && sub == 0 {
		return o.DefaultValue, true
	}
	if s := o.Sub(sub); s != nil {
		return s.DefaultValue, true
	}
	return "", false
}

// compareObjects compares a stack's object list with the dictionary and
// the file. The data type entries 0x0001..0x0007 a stack lists as objects
// (DummyUsage) are returned apart: go-canopen's dictionary does not hold
// them.
func compareObjects(f *eds.File, d *od.Dictionary, theirs []stackObject) (out, dummies []string) {
	seen := map[string]bool{}
	for _, o := range theirs {
		idx, err := strconv.ParseUint(strings.TrimPrefix(o.Index, "0x"), 16, 16)
		if err != nil {
			out = append(out, "bad index "+o.Index)
			continue
		}
		key := hexKey(uint16(idx), uint8(o.SubIndex))
		seen[key] = true
		if idx >= 1 && idx <= 7 {
			dummies = append(dummies, fmt.Sprintf("0x%04X", idx))
			continue
		}
		v, err := d.Variable(uint16(idx), uint8(o.SubIndex))
		if err != nil {
			out = append(out, key+": not in go-canopen's dictionary")
			continue
		}
		var diffs []string
		if o.DataType == nil || *o.DataType != fmt.Sprintf("0x%04X", uint16(v.DataType)) {
			diffs = append(diffs, fmt.Sprintf("dataType %v vs 0x%04X", deref(o.DataType), uint16(v.DataType)))
		}
		if o.Access == nil || !strings.EqualFold(*o.Access, v.Access.String()) {
			diffs = append(diffs, fmt.Sprintf("access %v vs %s", deref(o.Access), v.Access))
		}
		if o.PDOMapping != v.PDOMappable {
			diffs = append(diffs, fmt.Sprintf("pdoMapping %v vs %v", o.PDOMapping, v.PDOMappable))
		}
		if o.Default != nil && strings.TrimSpace(*o.Default) != "" {
			// The stacks report the file's DefaultValue literal: evaluate
			// it and go-canopen's reading of the same sub-object (a DCF's
			// dictionary holds the ParameterValue instead).
			want := d.DefaultValue(v)
			if lit, ok := fileDefault(f, v.Index, v.SubIndex); ok {
				want, _ = canopen.ParseValue(v.DataType, strings.TrimSpace(lit), d.NodeID())
			}
			got, err := canopen.ParseValue(v.DataType, strings.TrimSpace(*o.Default), d.NodeID())
			if err != nil || !bytes.Equal(got, want) {
				diffs = append(diffs, fmt.Sprintf("default %q = %x (%v) vs %x", *o.Default, got, err, want))
			}
		}
		if len(diffs) > 0 {
			out = append(out, key+": "+strings.Join(diffs, ", "))
		}
	}
	var missing []string
	for _, o := range d.Objects() {
		for _, v := range o.Subs {
			if k := hexKey(v.Index, v.SubIndex); !seen[k] {
				missing = append(missing, k)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		out = append(out, "not in the stack's view: "+strings.Join(missing, ","))
	}
	return out, dummies
}

func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}
