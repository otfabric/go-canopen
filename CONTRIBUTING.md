# Contributing to go-canopen

Thank you for your interest in contributing.

## Getting started

```sh
git clone git@github.com:<your-username>/go-canopen.git
cd go-canopen
git checkout -b feature/my-change
```

## Prerequisites

- Go 1.23+ (see [go.mod](go.mod)); the code must build and pass on Go 1.23
- [staticcheck](https://staticcheck.dev/), [golangci-lint](https://golangci-lint.run/) (configuration in `.golangci.yml`, the same linters CI's shared workflow runs), [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
- Optional: Wireshark's `tshark` (the `dissect` oracle) and a Python with `pip install canopen` (`EDS_PYCANOPEN`, the `eds` cross-check)
- For the SocketCAN tests: Linux with the `vcan` module: `make vcan-setup`. On macOS, a Linux VM (for example [Lima](https://lima-vm.io/) with Ubuntu) works.
- Docker, for the interop suite only (planned)

The parent otfabric `go.work` does not list this module: the Makefile sets
`GOWORK=off`; set it yourself when running `go` directly.

## Build and test

```sh
make check          # generated code, format, lint, vet, vuln, race tests, coverage, examples, cross builds
make test-vcan      # Linux: SocketCAN tests must run, not skip
make fuzz           # every fuzz target, FUZZTIME=30s each
make chaos          # the chaos monkey, CHAOS_ITERATIONS=200
```

## Rules

- No cgo and no runtime dependency besides go-canbus.
- SPDX header `// SPDX-License-Identifier: MIT` on every Go file.
- Every behaviour change comes with a test; parsers of untrusted input get
  a fuzz target. Services must stay `-race` clean, leak no goroutines,
  and remove the dictionary hooks they install when closed.
- Coverage stays at or above 90 % in total (`make coverage-check`); keep
  each library package above 90 % as well.
- A compatibility claim needs an automated test against an independent
  implementation (see INTEROPERABILITY.md). Do not write skipped or
  simulated results as passes.
- Where CiA 301 leaves room, document the interpretation in the package
  documentation and in LIMITATIONS.md.
- Keep ARCHITECTURE.md, API.md and the other docs in step with the code.
- Changes to the code generator need `make generate` and a reviewed diff
  of `internal/golden`.

## Commit and pull request

Small, focused commits with a message that says what and why. Open a pull
request against `main`; CI must be green, including the vcan jobs.
