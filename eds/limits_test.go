// SPDX-License-Identifier: MIT

package eds

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// compactFile returns a file of one VAR and n ARRAYs in compact storage
// of 254 elements each: 1 + 255n sub-objects.
func compactFile(n int) []byte {
	var b strings.Builder
	b.WriteString("[1000]\nParameterName=Device type\nObjectType=7\nDataType=7\nAccessType=ro\nDefaultValue=0\n")
	for i := range n {
		fmt.Fprintf(&b, "[%04X]\nParameterName=A%d\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=254\n", 0x2000+i, i)
	}
	return []byte(b.String())
}

// allocated returns the octets f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestSubObjectLimit: a dictionary is built with at most maxSubObjects
// sub-objects, compact storage counted expanded. At the limit the build
// succeeds; one compact array more is refused, in lenient mode too,
// before anything is built: the build allocates in proportion to the
// file, not to the sub-objects it describes.
func TestSubObjectLimit(t *testing.T) {
	atLimit := (maxSubObjects - 1) / 255 // one VAR and these arrays: exactly maxSubObjects
	if 1+255*atLimit != maxSubObjects {
		t.Fatalf("the test file does not reach the limit: %d sub-objects", 1+255*atLimit)
	}
	f, err := ParseBytes(compactFile(atLimit))
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.Dictionary(1)
	if err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	n := 0
	for _, o := range d.Objects() {
		n += len(o.Subs)
	}
	if n != maxSubObjects {
		t.Errorf("%d sub-objects built, want %d", n, maxSubObjects)
	}

	in := compactFile(atLimit + 1)
	f, err = ParseBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, lenient := range []bool{false, true} {
		var err error
		got := allocated(func() { _, _, err = f.BuildDictionary(1, BuildOptions{Lenient: lenient}) })
		if err == nil || !strings.Contains(err.Error(), "sub-objects") {
			t.Errorf("lenient %v: one array beyond the limit: %v, want the sub-object limit", lenient, err)
		}
		if budget := uint64(200 * len(in)); got > budget {
			t.Errorf("lenient %v: %d octets allocated for a file of %d octets, budget %d", lenient, got, len(in), budget)
		}
	}
}

// TestNaNLimitRefused: a float limit that is NaN is refused by the build
// (a warning and the sub-object skipped in lenient mode) and reported by
// Validate.
func TestNaNLimitRefused(t *testing.T) {
	for _, key := range []string{"LowLimit", "HighLimit"} {
		in := "[1000]\nParameterName=Device type\nObjectType=7\nDataType=7\nAccessType=ro\nDefaultValue=0\n" +
			"[2000]\nParameterName=F\nObjectType=7\nDataType=8\nAccessType=rw\nDefaultValue=1\n" + key + "=NaN\n"
		f, err := ParseBytes([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Dictionary(1); err == nil || !strings.Contains(err.Error(), "NaN") {
			t.Errorf("%s=NaN: Dictionary = %v, want a refusal", key, err)
		}
		d, ds, err := f.BuildDictionary(1, BuildOptions{Lenient: true})
		if err != nil || d.Object(0x2000) != nil || len(ds) == 0 {
			t.Errorf("%s=NaN, lenient: object %v, diagnostics %v, %v; want it skipped with a warning", key, d.Object(0x2000), ds, err)
		}
		found := false
		for _, dg := range f.Validate() {
			found = found || dg.Key == key && strings.Contains(dg.Message, "NaN")
		}
		if !found {
			t.Errorf("%s=NaN: Validate does not report it", key)
		}
	}
}

// countingReader yields n octets of comment lines and counts what it
// gave.
type countingReader struct {
	left, read int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.left)
	for i := range n {
		p[i] = ';'
		if (r.read+i)%64 == 63 {
			p[i] = '\n'
		}
	}
	r.left -= n
	r.read += n
	return n, nil
}

// TestFileSizeLimit: Parse, ParseFile and ParseBytes read files of up to
// maxFileSize octets; a larger one is refused, and Parse stops reading it
// at the limit.
func TestFileSizeLimit(t *testing.T) {
	for _, tc := range []struct {
		size int
		ok   bool
	}{
		{maxFileSize - 1, true},
		{maxFileSize, true},
		{maxFileSize + 1, false},
		{4 * maxFileSize, false},
	} {
		r := &countingReader{left: tc.size}
		_, err := Parse(r)
		if (err == nil) != tc.ok {
			t.Errorf("Parse of %d octets: %v", tc.size, err)
		}
		if r.read > maxFileSize+1 || (tc.ok && r.read != tc.size) {
			t.Errorf("Parse of %d octets read %d", tc.size, r.read)
		}
	}
	big := make([]byte, maxFileSize+1)
	_, _ = (&countingReader{left: len(big)}).Read(big)
	if _, err := ParseBytes(big); err == nil {
		t.Error("ParseBytes of one octet beyond the limit succeeded")
	}
	if _, err := ParseBytes(big[:maxFileSize]); err != nil {
		t.Errorf("ParseBytes at the limit: %v", err)
	}
	path := filepath.Join(t.TempDir(), "big.eds")
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(path); err == nil {
		t.Error("ParseFile of one octet beyond the limit succeeded")
	}
	if _, err := ParseFile(filepath.Join(t.TempDir(), "missing.eds")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ParseFile of a missing file: %v", err)
	}
}
