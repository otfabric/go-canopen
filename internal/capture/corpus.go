// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Excerpt is a committed excerpt of a log that is not redistributed
// whole: chosen lines, verbatim, with their line numbers in the original.
type Excerpt struct {
	Source         string      `json:"source"`
	Note           string      `json:"note"`
	OriginalSHA256 string      `json:"originalSHA256"`
	Raw            []ExcerptLn `json:"-"`
}

// ExcerptLn is one line of an excerpt.
type ExcerptLn struct {
	Line int
	Text string
}

// Dir returns testdata/captures of this repository.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "captures")
}

// ExtendedDir returns the directory of the extended corpus (the whole
// logs, fetched by scripts/fetch-captures.sh) and whether it is set:
// $CANOPEN_CAPTURES_DIR, else .cache/captures when it exists.
func ExtendedDir() (string, bool) {
	if d := os.Getenv("CANOPEN_CAPTURES_DIR"); d != "" {
		return d, true
	}
	d := filepath.Join(Dir(), "..", "..", ".cache", "captures")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d, true
	}
	return "", false
}

// LoadExcerpt reads testdata/captures/<rel> and parses every line.
func LoadExcerpt(rel string) (*Excerpt, []Record, error) {
	b, err := readFile(filepath.Join(Dir(), rel))
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		Source         string            `json:"source"`
		Note           string            `json:"note"`
		OriginalSHA256 string            `json:"originalSHA256"`
		Lines          []json.RawMessage `json:"lines"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", rel, err)
	}
	ex := &Excerpt{Source: doc.Source, Note: doc.Note, OriginalSHA256: doc.OriginalSHA256}
	recs := make([]Record, 0, len(doc.Lines))
	prev := 0
	for _, l := range doc.Lines {
		var pair []any
		if err := json.Unmarshal(l, &pair); err != nil || len(pair) != 2 {
			return nil, nil, fmt.Errorf("%s: entry %s is not [line, text]", rel, l)
		}
		n, ok1 := pair[0].(float64)
		text, ok2 := pair[1].(string)
		if !ok1 || !ok2 || int(n) <= prev {
			return nil, nil, fmt.Errorf("%s: entry %s: line numbers must increase", rel, l)
		}
		prev = int(n)
		rec, err := ParseCopleyLine(strings.TrimSpace(text))
		if err != nil {
			return nil, nil, fmt.Errorf("%s line %d: %w", rel, int(n), err)
		}
		rec.Line = int(n)
		recs = append(recs, rec)
		ex.Raw = append(ex.Raw, ExcerptLn{Line: int(n), Text: text})
	}
	return ex, recs, nil
}

// ReadCopleyFile reads a whole CANview log.
func ReadCopleyFile(path string) ([]Record, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return ReadCopley(bytes.NewReader(b))
}

// ErrNoExtended reports that the extended corpus is not present.
var ErrNoExtended = errors.New("capture: extended corpus not fetched (make captures-extended)")

func readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(path))
}
