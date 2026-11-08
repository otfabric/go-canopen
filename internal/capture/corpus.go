// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
)

// CopleyLogs are the four Copley Controls logs in testdata/captures/copley,
// without the .log extension.
var CopleyLogs = []string{
	"unknown_copley-1100_absolute-move",
	"unknown_copley-1100_relative-move",
	"unknown_copley-11e0_linkage-home-and-move",
	"unknown_copley-1060_pvt-move",
}

// Dir returns testdata/captures of this repository.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "captures")
}

// LoadCopley reads testdata/captures/copley/<name>.log.
func LoadCopley(name string) ([]Record, error) {
	return ReadCopleyFile(filepath.Join(Dir(), "copley", name+".log"))
}

// ReadCopleyFile reads a whole CANview log.
func ReadCopleyFile(path string) ([]Record, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return ReadCopley(bytes.NewReader(b))
}

func readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Base(path))
}
