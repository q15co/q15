GO ?= go
BIN_DIR ?= bin
CONFIG ?= q15.toml

HELPER_TAGS := containers_image_openpgp exclude_graphdriver_btrfs
RUN_ENV := BUILDAH_ISOLATION=chroot GOMAXPROCS=1 GODEBUG=updatemaxprocs=0

.DEFAULT_GOAL := build

.PHONY: all build build-main build-helper test run run-verbose clean fmt help

all: build

build: build-main build-helper

build-main:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/q15 .

build-helper:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -tags='$(HELPER_TAGS)' -o $(BIN_DIR)/q15-sandbox-helper ./cmd/q15-sandbox-helper

test:
	CGO_ENABLED=0 $(GO) test -tags='$(HELPER_TAGS)' ./...

run: build
	@if [ ! -f "$(CONFIG)" ]; then echo "missing config: $(CONFIG)"; exit 1; fi
	$(RUN_ENV) ./$(BIN_DIR)/q15 start --config $(CONFIG)

run-verbose: build
	@if [ ! -f "$(CONFIG)" ]; then echo "missing config: $(CONFIG)"; exit 1; fi
	Q15_SANDBOX_VERBOSE=1 $(RUN_ENV) ./$(BIN_DIR)/q15 start --config $(CONFIG)

fmt:
	gofmt -w $$(rg --files -g '*.go')

clean:
	rm -rf $(BIN_DIR)
	$(GO) clean -cache -testcache

help:
	@echo "Available targets:"
	@echo "  build         Build main app and sandbox helper into ./bin"
	@echo "  build-main    Build ./bin/q15"
	@echo "  build-helper  Build ./bin/q15-sandbox-helper (helper-safe tags)"
	@echo "  test          Run Go tests with helper-safe tags (CGO_ENABLED=0)"
	@echo "  run           Build and start q15 with dev runtime defaults"
	@echo "  run-verbose   Same as run, with Q15_SANDBOX_VERBOSE=1"
	@echo "  fmt           Run gofmt over all Go files"
	@echo "  clean         Remove ./bin and Go build/test caches"
	@echo ""
	@echo "Notes:"
	@echo "  - Set MOONSHOT_API_KEY before running the app"
	@echo "  - Override CONFIG=... to use a different config file"
