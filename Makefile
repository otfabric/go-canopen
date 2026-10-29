# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: help all build examples fmt fmt-check lint lint-ci vet vuln test test-vcan coverage coverage-check cover bench fuzz chaos e2e check clean generate generate-check coverpkg-check cross interop interop-images vcan-setup

help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# The parent otfabric/go.work does not list this module.
export GOWORK := off

# Format with the gofmt of the Go version in go.mod, as CI does.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

BIN_DIR := bin
TEST_PKGS := $(shell go list ./... | grep -v '/examples')
# Packages whose coverage counts. Excluded, by policy (TESTING.md): the
# examples, the generator of the golden files (internal/cmd), the
# generated golden code (internal/golden, verified by the differential
# test), the test helpers (internal/testbus) and the end-to-end suite.
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/internal/' | grep -v '/e2e' | paste -sd, -)
COVER_MIN := 90

# vcan interface the SocketCAN tests use (Linux).
CANOPEN_TEST_IFACE ?= vcan0
export CANOPEN_TEST_IFACE

all: build ## Build the examples

build: ## Build the examples into ./bin
	@mkdir -p $(BIN_DIR)
	@for dir in examples/*/; do \
		name="$$(basename "$$dir")"; \
		go build -o "$(BIN_DIR)/example-$$name" "./$$dir"; \
	done

examples: ## Run the self-contained examples (virtual bus; each must exit successfully)
	@for dir in examples/*/; do \
		e="$$(basename "$$dir")"; \
		grep -q 'example:hardware' "$$dir"main.go && continue; \
		echo "Running example $$e"; \
		go run ./$$dir >/dev/null || { echo "example $$e failed"; exit 1; }; \
	done

fmt: ## Format with the gofmt of the go.mod Go version
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if gofmt (go.mod version or local) would change a file
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then echo "Not formatted:"; echo "$$ci $$loc"; exit 1; fi

lint: ## Run staticcheck
	@staticcheck ./...
	@GOOS=linux staticcheck ./...

lint-ci: ## Run golangci-lint
	@golangci-lint run ./...

vet: ## Run go vet for this platform, Linux (amd64, arm) and Windows
	@go vet ./...
	@GOOS=linux go vet ./...
	@GOOS=linux GOARCH=arm go vet ./...
	@GOOS=windows go vet ./...

vuln: ## Run govulncheck
	@govulncheck ./...

test: ## Run the tests with the race detector (SocketCAN tests skip without vcan0)
	@go test -count=1 -race $(TEST_PKGS)

# Packages share vcan0: on Linux they run one at a time (-p 1).
test-vcan: ## Run the tests on Linux, failing instead of skipping when vcan0 is missing
	@CANOPEN_REQUIRE_VCAN=1 go test -p 1 -count=1 -race $(TEST_PKGS)

vcan-setup: ## Create vcan0 and vcan1 (Linux, sudo)
	@sudo modprobe vcan
	@for i in vcan0 vcan1; do ip link show $$i >/dev/null 2>&1 || sudo ip link add $$i type vcan; sudo ip link set $$i mtu 72 up; done

CHAOS_ITERATIONS ?= 200
chaos: ## Run the chaos-monkey suites with CHAOS_ITERATIONS seeds each
	@CANOPEN_CHAOS_ITERATIONS=$(CHAOS_ITERATIONS) go test -count=1 -timeout=60m -run Monkey -v ./... | grep -E '^(---|ok|FAIL|PASS)'

e2e: ## Run the end-to-end suite (on Linux with vcan0 also over SocketCAN)
	@go test -count=1 -race ./e2e

coverage: ## Run the tests with coverage across the library packages (writes coverage.out)
	@go test -p 1 -count=1 -race -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=coverage.out $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -1

coverage-check: coverage ## Fail when total coverage is below COVER_MIN
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d %); \
	echo "total $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t=$$total -v m=$(COVER_MIN) 'BEGIN { exit (t+0 < m+0) }'

cover: coverage ## Open the coverage report in a browser
	@go tool cover -html=coverage.out

bench: ## Run the benchmarks
	@go test -count=1 -run='^$$' -bench=. -benchmem ./...

FUZZTIME ?= 30s
fuzz: ## Run every fuzz target for FUZZTIME (default 30s)
	@for pkg in $$(go list ./...); do \
		for f in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$f"; \
			go test -run='^$$' -fuzz="^$$f$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

generate: ## Regenerate the code generator's golden output
	@go generate ./codegen

generate-check: ## Fail when the golden output differs from what the generator produces
	@go run ./internal/cmd/gengolden -check

coverpkg-check: ## Fail when CI's coverpkg (.github/workflows/ci.yml) is not COVER_PKGS
	@grep -qF -- "$(COVER_PKGS)" .github/workflows/ci.yml || { echo "coverpkg in .github/workflows/ci.yml must be: $(COVER_PKGS)"; exit 1; }

cross: ## Build every package for the release platforms
	@for t in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		echo "build $$t"; GOOS=$${t%/*} GOARCH=$${t#*/} go build ./... || exit 1; done

# Reference implementations: otfabric/canopen-interop images, pinned by
# digest in interop/harness_test.go (see INTEROPERABILITY.md).
# interop-images builds canopen-interop's source at INTEROP_REF as :dev
# images, to test unreleased adapters.
INTEROP_REF ?= main
interop-images: ## Build the canopen-interop images from source as :dev (needs Docker)
	@rm -rf $(BIN_DIR)/canopen-interop && mkdir -p $(BIN_DIR)
	@git clone -q --depth 1 --branch $(INTEROP_REF) https://github.com/otfabric/canopen-interop.git $(BIN_DIR)/canopen-interop
	@$(MAKE) -C $(BIN_DIR)/canopen-interop images REGISTRY=ghcr.io/otfabric VERSION=dev

interop: ## Run the interop suite against the canopen-interop images (Linux, Docker, vcan0; writes bin/interop-report.json)
	@mkdir -p $(BIN_DIR)
	@CANOPEN_REQUIRE_VCAN=1 CANOPEN_REQUIRE_INTEROP=1 CANOPEN_INTEROP_REPORT=$(CURDIR)/$(BIN_DIR)/interop-report.json \
		go test -tags=interop -count=1 -timeout=60m ./interop/...

check: generate-check fmt-check lint lint-ci vet vuln test coverage examples cross ## Everything CI checks except the vcan and interop jobs

clean: ## Remove binaries and coverage files
	@rm -rf $(BIN_DIR) coverage.out coverage.html
