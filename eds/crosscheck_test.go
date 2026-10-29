// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
)

// pyVar is one sub-object as testdata/pycanopen_dump.py prints it.
type pyVar struct {
	Index    uint16   `json:"index"`
	Sub      uint8    `json:"sub"`
	Name     string   `json:"name"`
	Type     uint16   `json:"type"`
	Access   string   `json:"access"`
	PDO      bool     `json:"pdo"`
	Relative bool     `json:"relative"`
	Default  *pyValue `json:"default"`
	Value    *pyValue `json:"value"`
}

type pyValue struct {
	Int   *json.Number `json:"int"`
	Float *float64     `json:"float"`
	Str   *string      `json:"str"`
	Hex   *string      `json:"hex"`
}

// pythonCanopen returns a Python interpreter that imports canopen: the one
// in $EDS_PYCANOPEN, else python3 from PATH.
func pythonCanopen(t *testing.T) string {
	py := os.Getenv("EDS_PYCANOPEN")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-I", "-c", "import canopen").Run(); err != nil {
		t.Skipf("SKIPPED: cross-check against python canopen: %q cannot import canopen (%v). "+
			"Set EDS_PYCANOPEN to a Python with `pip install canopen` to run it.", py, err)
	}
	return py
}

// TestCrossCheckPythonCanopen compares the dictionaries built from the
// fixtures with what python canopen builds from them: data type, access
// type, PDO mappability, names and default values (node-ID 0x10). It is
// skipped, loudly, when python canopen is not installed; the
// canopen-interop repository runs the full cross-stack checks.
func TestCrossCheckPythonCanopen(t *testing.T) {
	py := pythonCanopen(t)
	const node = 0x10
	compared := 0
	for _, p := range append(fixtures, "testdata/third_party/python_canopen_sample.eds") {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// python canopen's configparser rejects a BOM.
		tmp := filepath.Join(t.TempDir(), filepath.Base(p))
		if err := os.WriteFile(tmp, bytes.TrimPrefix(src, []byte("\xEF\xBB\xBF")), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd := exec.Command(py, "-I", "testdata/pycanopen_dump.py", tmp, "0x10")
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			t.Logf("%s: python canopen cannot read it (%s); not compared", p, lines[len(lines)-1])
			continue
		}
		var vars []pyVar
		if err := json.Unmarshal(out, &vars); err != nil {
			t.Fatal(err)
		}
		ef := mustParse(t, p)
		d, _, err := ef.BuildDictionary(node, BuildOptions{Lenient: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, pv := range vars {
			if pv.Index < 0x1000 { // python adds the dummy types as objects
				continue
			}
			v, err := d.Variable(pv.Index, pv.Sub)
			if err != nil {
				t.Logf("%s: %04X:%02X only in python canopen (%s)", p, pv.Index, pv.Sub, pv.Name)
				continue
			}
			compact := ef.Object(pv.Index).CompactSubObj > 0
			if compact && pv.Sub == 0 {
				// python canopen gives the implicit sub-index 0 of
				// compact storage its generic defaults (rw, "Number of
				// entries"); see CompactSub0Name.
				continue
			}
			compared++
			key := p + " " + v.Key()
			if uint16(v.DataType) != pv.Type || v.Access.String() != pv.Access || v.PDOMappable != pv.PDO {
				t.Errorf("%s: type/access/pdo %s %s %v vs python %#x %s %v", key, v.DataType, v.Access, v.PDOMappable, pv.Type, pv.Access, pv.PDO)
			}
			if !compact && v.Name != pv.Name {
				t.Errorf("%s: name %q vs python %q", key, v.Name, pv.Name)
			}
			want := pv.Default
			if pv.Value != nil { // DCF: the dictionary holds ParameterValue
				want = pv.Value
			} else if v.NodeIDRelative != pv.Relative {
				t.Errorf("%s: relative %v vs python %v", key, v.NodeIDRelative, pv.Relative)
			}
			if want == nil {
				continue
			}
			def := d.DefaultValue(v)
			switch dv := want; {
			case dv.Int != nil:
				n, err := dv.Int.Int64()
				if err != nil {
					t.Fatal(err)
				}
				mask := uint64(math.MaxUint64)
				if bits := v.DataType.Size() * 8; bits < 64 {
					mask = 1<<bits - 1
				}
				if canopen.DecodeUint(def)&mask != uint64(n)&mask {
					t.Errorf("%s: default %x vs python %d", key, def, n)
				}
			case dv.Float != nil:
				got, _ := canopen.Decode(v.DataType, def)
				if f32, ok := got.(float32); ok && f32 != float32(*dv.Float) || (!ok && got != *dv.Float) {
					t.Errorf("%s: default %v vs python %v", key, got, *dv.Float)
				}
			case dv.Str != nil:
				if string(def) != *dv.Str {
					t.Errorf("%s: default %q vs python %q", key, def, *dv.Str)
				}
			case dv.Hex != nil:
				if hex.EncodeToString(def) != *dv.Hex {
					t.Errorf("%s: default %x vs python %s", key, def, *dv.Hex)
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("nothing compared")
	}
	t.Logf("compared %d sub-objects with python canopen", compared)
}
