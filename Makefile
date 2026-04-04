SHELL := bash

GO ?= go
DOCKER_COMPOSE ?= docker compose
BIN_DIR ?= bin
COMPOSE_FILE ?= docker-compose.yml
COMPOSE_PROJECT_NAME ?= q15-local
AGENT_MOD_DIR ?= systems/agent
EXEC_MOD_DIR ?= systems/exec
PROXY_MOD_DIR ?= systems/proxy
EXEC_CONTRACT_MOD_DIR ?= libs/exec-contract
PROXY_CONTRACT_MOD_DIR ?= libs/proxy-contract

TOOLS_BIN_DIR := $(CURDIR)/.tools/bin

export PATH := $(TOOLS_BIN_DIR):$(PATH)

COMPOSE_ENV := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME)

.DEFAULT_GOAL := build

.PHONY: all build build-agent build-auth build-exec build-proxy project-setup fmt lint lint-changed test verify verify-ci hooks-install hooks-uninstall compose-secrets-init compose-up compose-down compose-logs compose-ps clean help

all: build

build: build-agent build-auth build-exec build-proxy

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

project-setup:
	./scripts/project-setup.sh

fmt: project-setup
	FILES="$(FILES)" ./scripts/fmt.sh

test:
	cd $(EXEC_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(PROXY_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(AGENT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(EXEC_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(PROXY_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...

lint: project-setup
	./scripts/lint-changed.sh --tracked
	./scripts/go-static-checks.sh

lint-changed: project-setup
	FILES="$(FILES)" ./scripts/lint-changed.sh

verify: project-setup
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
	for example in deploy/compose/secrets/*.example; do \
		target=$${example%.example}; \
		if [ -f "$$target" ]; then \
			echo "kept $$target"; \
			continue; \
		fi; \
		cp "$$example" "$$target"; \
		echo "wrote $$target"; \
	done

compose-up:
	$(COMPOSE_ENV) $(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up --build -d

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
	@echo "  build         Build q15-agent, q15-auth, q15-exec, and q15-proxy into ./bin"
	@echo "  build-agent   Build ./bin/q15-agent from $(AGENT_MOD_DIR)"
	@echo "  build-auth    Build ./bin/q15-auth from $(AGENT_MOD_DIR)/cmd/q15-auth"
	@echo "  build-exec    Build ./bin/q15-exec from $(EXEC_MOD_DIR)"
	@echo "  build-proxy   Build ./bin/q15-proxy from $(PROXY_MOD_DIR)"
	@echo "  project-setup Install or refresh the pinned repo-local tooling under ./.tools"
	@echo "  fmt           Format tracked files (or FILES='a b' for an explicit subset)"
	@echo "  lint          Run full-repo file checks plus repo-wide Go static analysis"
	@echo "  lint-changed  Run fast changed-file checks (or FILES='a b' for an explicit subset)"
	@echo "  test          Run Go tests for exec/proxy contracts + agent + exec + proxy"
	@echo "  verify        Run project-setup, lint, and test"
	@echo "  verify-ci     Run changed-file checks plus repo-wide Go static analysis"
	@echo "  hooks-install Install the optional q15-managed pre-commit hook"
	@echo "  hooks-uninstall  Remove q15-managed or legacy generated git hooks"
	@echo "  compose-secrets-init  Seed ignored local Compose secret files from tracked examples"
	@echo "  compose-up    Build and start the local-development Docker Compose stack"
	@echo "  compose-down  Stop and remove the local-development Docker Compose stack"
	@echo "  compose-logs  Follow local-development Docker Compose logs (set SERVICE=q15-agent|q15-exec|q15-proxy)"
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
