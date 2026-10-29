// SPDX-License-Identifier: MIT

// Command gengolden regenerates the code generator's golden output,
// internal/golden/*, from the EDS and DCF fixtures (go generate ./...).
// With -check it fails when the files on disk differ instead.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/otfabric/go-canopen/codegen"
	"github.com/otfabric/go-canopen/eds"
)

// Goldens lists the fixtures and the package each is generated into.
var goldens = []struct{ src, pkg string }{
	{"eds/testdata/io401.eds", "io401"},
	{"eds/testdata/drive402.eds", "drive402"},
	{"eds/testdata/compact.eds", "compact"},
	{"eds/testdata/node37.dcf", "node37"},
	{"eds/testdata/third_party/canopennode_ds301_profile.eds", "ds301"},
}

func main() {
	check := flag.Bool("check", false, "fail when the golden files are out of date")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	stale := false
	for _, g := range goldens {
		src, err := os.ReadFile(filepath.Join(*root, g.src))
		if err != nil {
			fail(err)
		}
		f, err := eds.ParseBytes(src)
		if err != nil {
			fail(err)
		}
		sum := sha256.Sum256(src)
		code, err := codegen.Generate(f, codegen.Options{Package: g.pkg, Source: filepath.Base(g.src), SourceSHA256: hex.EncodeToString(sum[:])})
		if err != nil {
			fail(err)
		}
		path := filepath.Join(*root, "internal/golden", g.pkg, g.pkg+".go")
		if *check {
			have, _ := os.ReadFile(path)
			if !bytes.Equal(have, code) {
				fmt.Fprintf(os.Stderr, "%s is out of date: run go generate ./codegen\n", path)
				stale = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, code, 0o644); err != nil {
			fail(err)
		}
	}
	if stale {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gengolden:", err)
	os.Exit(1)
}
