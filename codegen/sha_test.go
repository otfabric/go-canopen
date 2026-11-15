// SPDX-License-Identifier: MIT

package codegen

import (
	"bytes"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// generatedHeader is the line that marks a generated Go file
// (https://go.dev/s/generatedcode).
var generatedHeader = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// TestSourceSHA256Comment: SourceSHA256 goes into the provenance line
// comment as Source does, control characters and line separators
// replaced by spaces and Unicode format characters removed: the header
// stays one line, and the text adds no declaration to the generated file.
func TestSourceSHA256Comment(t *testing.T) {
	f := parse(t, "../eds/testdata/io401.eds")
	for _, tc := range []struct {
		name, sum, want string
	}{
		{"hex", "9f86d081884c7d65", "(sha256 9f86d081884c7d65)"},
		{"empty", "", "from an EDS file. DO NOT EDIT."},
		{"line feed", "ab\nvar Injected = 1\n//", "(sha256 ab var Injected = 1 //)"},
		{"carriage return", "ab\rcd", "(sha256 ab cd)"},
		{"line separator", "ab cd ef", "(sha256 ab cd ef)"},
		{"control characters", "ab\x00\x1f\x7fcd", "(sha256 ab   cd)"},
		{"invalid UTF-8", "ab\xffcd", "(sha256 ab?cd)"},
		{"format characters", "ab\u202Ecd\uFEFF", "(sha256 abcd)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, err := Generate(f, Options{Package: "io401", SourceSHA256: tc.sum})
			if err != nil {
				t.Fatalf("Generate with SourceSHA256 %q: %v", tc.sum, err)
			}
			first, _, _ := bytes.Cut(code, []byte("\n"))
			if !generatedHeader.Match(first) || !strings.Contains(string(first), tc.want) {
				t.Fatalf("header %q, want one generated-code line with %q", first, tc.want)
			}
			file, err := parser.ParseFile(token.NewFileSet(), "io401.go", code, 0)
			if err != nil {
				t.Fatal(err)
			}
			if file.Scope.Lookup("Injected") != nil {
				t.Fatal("the header text declared Injected")
			}
		})
	}
}
