SHELL := bash

GO ?= go
DOCKER_COMPOSE ?= docker compose
DOCKER ?= docker
BIN_DIR ?= bin
COMPOSE_FILE ?= docker-compose.yml
COMPOSE_PROJECT_NAME ?= q15-local
AGENT_MOD_DIR ?= systems/agent
EXEC_MOD_DIR ?= systems/exec
PROXY_MOD_DIR ?= systems/proxy
WEB_MOD_DIR ?= systems/web
UI_DIR := $(WEB_MOD_DIR)/ui
PNPM = $(TOOLS_BIN_DIR)/pnpm
EXEC_CONTRACT_MOD_DIR ?= libs/exec-contract
PROXY_CONTRACT_MOD_DIR ?= libs/proxy-contract
CHAT_CONTRACT_MOD_DIR ?= libs/chat-contract

TOOLS_BIN_DIR := $(CURDIR)/.tools/bin

export PATH := $(TOOLS_BIN_DIR):$(PATH)

COMPOSE_ENV := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME)

.DEFAULT_GOAL := build

.PHONY: all build build-agent build-auth build-exec build-proxy build-web build-web-image test-web project-setup fmt lint lint-changed test verify verify-ci hooks-install hooks-uninstall compose-secrets-init compose-up compose-down compose-logs compose-ps clean help protos protos-check ui-install ui-dev ui-build ui-lint ui-fix ui-test ui-test-coverage ui-e2e ui-clean ui-fixtures-check compose-check

all: build

build: build-agent build-auth build-exec build-proxy build-web

build-agent:
	@mkdir -p $(BIN_DIR)
	cd $(AGENT_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-agent .

build-auth:
	@mkdir -p $(BIN_DIR)
	cd $(AGENT_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-auth ./cmd/q15-auth

build-exec:
	@mkdir -p $(BIN_DIR)
	cd $(EXEC_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-exec .

build-proxy:
	@mkdir -p $(BIN_DIR)
	cd $(PROXY_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-proxy .

build-web: ui-build
	@mkdir -p $(BIN_DIR)
	cd $(WEB_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-web .

build-web-image:
	$(DOCKER) build -f docker/web.Dockerfile -t q15-web:local .

test-web: project-setup
	cd $(WEB_MOD_DIR) && CGO_ENABLED=0 $(GO) test $(TEST_FLAGS) ./...

.PHONY: ui-auth-test-server
ui-auth-test-server:
	cd $(WEB_MOD_DIR) && $(GO) run ./internal/auth/testserver

project-setup:
	./scripts/project-setup.sh

fmt: project-setup
	FILES="$(FILES)" ./scripts/fmt.sh

test:
	./scripts/image-build-impact-test.sh
	./scripts/check-ui-fixtures.sh
	./scripts/compose-contract-test.sh
	cd $(EXEC_CONTRACT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(PROXY_CONTRACT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(CHAT_CONTRACT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(AGENT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(EXEC_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(PROXY_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	$(MAKE) test-web

protos: project-setup
	buf generate

protos-check: project-setup
	buf generate
	git diff --exit-code -- libs/

lint: project-setup
	SKIP_TYPESCRIPT=1 ./scripts/lint-changed.sh --tracked
	./scripts/go-static-checks.sh
	./scripts/check-ui-fixtures.sh

ui-install: project-setup
	./scripts/check-ui-tool-versions.sh
	cd $(UI_DIR) && $(PNPM) install --frozen-lockfile

ui-dev: ui-install
	cd $(UI_DIR) && $(PNPM) dev

ui-build: ui-install
	cd $(UI_DIR) && $(PNPM) build

ui-lint: ui-install
	cd $(UI_DIR) && $(PNPM) check

ui-fix: ui-install
	cd $(UI_DIR) && $(PNPM) exec vp check --fix
	cd $(UI_DIR) && $(PNPM) typecheck

ui-test: ui-install
	cd $(UI_DIR) && $(PNPM) test

ui-test-coverage: ui-install
	cd $(UI_DIR) && $(PNPM) test:coverage $(UI_TEST_ARGS)

ui-e2e: ui-build
	cd $(UI_DIR) && $(PNPM) test:e2e

ui-fixtures-check:
	./scripts/check-ui-fixtures.sh

ui-clean:
	rm -rf $(WEB_MOD_DIR)/internal/assets/dist
	mkdir -p $(WEB_MOD_DIR)/internal/assets/dist
	touch $(WEB_MOD_DIR)/internal/assets/dist/.gitkeep

compose-check: project-setup
	./scripts/compose-contract-test.sh

lint-changed: project-setup
	FILES="$(FILES)" ./scripts/lint-changed.sh

verify: project-setup
	$(MAKE) protos-check
	$(MAKE) lint
	$(MAKE) test

verify-ci: project-setup
	FILES="$(FILES)" ./scripts/lint-changed.sh
	./scripts/go-static-checks.sh

hooks-install:
	./scripts/install-hooks.sh

hooks-uninstall:
	./scripts/uninstall-hooks.sh

compose-secrets-init:
	@set -eu; \
	for example in deploy/compose/secrets/*.example deploy/compose/auth/*.example; do \
		target=$${example%.example}; \
		mkdir -p "$$(dirname "$$target")"; \
		if [ -f "$$target" ]; then \
			echo "kept $$target"; \
			continue; \
		fi; \
		cp "$$example" "$$target"; \
		echo "wrote $$target"; \
	done

compose-up:
	$(COMPOSE_ENV) $(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up --build -d --wait

compose-down:
	$(COMPOSE_ENV) $(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down --remove-orphans

compose-logs:
	$(COMPOSE_ENV) $(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs -f $(SERVICE)

compose-ps:
	$(COMPOSE_ENV) $(DOCKER_COMPOSE) -f $(COMPOSE_FILE) ps

clean:
	rm -rf $(BIN_DIR)
	$(GO) clean -cache -testcache

help:
	@echo "Available targets:"
	@echo "  build         Build q15-agent, q15-auth, q15-exec, q15-proxy, and q15-web into ./bin"
	@echo "  build-agent   Build ./bin/q15-agent from $(AGENT_MOD_DIR)"
	@echo "  build-auth    Build ./bin/q15-auth from $(AGENT_MOD_DIR)/cmd/q15-auth"
	@echo "  build-exec    Build ./bin/q15-exec from $(EXEC_MOD_DIR)"
	@echo "  build-proxy   Build ./bin/q15-proxy from $(PROXY_MOD_DIR)"
	@echo "  build-web     Build ./bin/q15-web from $(WEB_MOD_DIR)"
	@echo "  build-web-image  Build the local q15-web image (Corepack/pnpm UI, Go embedding)"
	@echo "  test-web      Test the browser tier (optional TEST_FLAGS)"
	@echo "  ui-install    Install the pinned browser dependencies with the frozen lockfile"
	@echo "  ui-dev        Start the Vite+ React development server"
	@echo "  ui-build      Build the PWA into the Go embedded bundle directory"
	@echo "  ui-lint       Check browser formatting, lint, and TypeScript with Oxc"
	@echo "  ui-fix        Apply safe browser format and lint fixes, then type-check"
	@echo "  ui-test       Run browser contract and UI tests"
	@echo "  ui-test-coverage  Measure browser test coverage and enforce thresholds"
	@echo "  ui-e2e        Run real browser tests against the compiled PWA"
	@echo "  ui-clean      Remove the bundle and restore dist/.gitkeep"
	@echo "  ui-fixtures-check  Check vendored browser fixtures and generated contract types"
	@echo "  compose-check Validate Compose rendering and the bridge volume boundary"
	@echo "  project-setup Install or refresh the pinned repo-local tooling under ./.tools"
	@echo "  fmt           Format tracked files (or FILES='a b' for an explicit subset)"
	@echo "  lint          Run full-repo file checks plus repo-wide Go static analysis"
	@echo "  lint-changed  Run fast changed-file checks (or FILES='a b' for an explicit subset)"
	@echo "  test          Run Go tests for exec/proxy/chat contracts + agent + exec + proxy + web"
	@echo "  protos        Regenerate protobuf stubs under libs/ from proto sources"
	@echo "  protos-check  Regenerate protobuf stubs and fail if they differ from tracked files"
	@echo "  verify        Run project-setup, protos-check, lint, and test"
	@echo "  verify-ci     Run changed-file checks plus repo-wide Go static analysis"
	@echo "  hooks-install Install the optional q15-managed pre-commit hook"
	@echo "  hooks-uninstall  Remove q15-managed or legacy generated git hooks"
	@echo "  compose-secrets-init  Seed ignored local Compose secret files from tracked examples"
	@echo "  compose-up    Build and start the local-development Docker Compose stack"
	@echo "  compose-down  Stop and remove the local-development Docker Compose stack"
	@echo "  compose-logs  Follow local-development Docker Compose logs (set SERVICE=q15-agent|q15-exec|q15-proxy|q15-tei|q15-qdrant)"
	@echo "  compose-ps    Show local-development Docker Compose service status"
	@echo "  clean         Remove ./bin and Go build/test caches"
	@echo ""
	@echo "Notes:"
	@echo "  - FILES accepts a space-separated file list and is shared by hooks, agents, and CI"
	@echo "  - repo-local tools live under ./.tools and are the source of truth for linting"
	@echo "  - compose-* targets use the root ./docker-compose.yml local-development stack"
	@echo "  - the image-first deployment example lives at ./deploy/compose/docker-compose.image-first.yml"
	@echo "  - compose uses tracked YAML config examples plus ignored local secret files under ./deploy/compose"
	@echo "  - q15-auth is the interactive bootstrap tool for generating auth.json outside the runtime containers"
