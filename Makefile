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

COMPOSE_ENV := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME)

.DEFAULT_GOAL := build

.PHONY: all build build-agent build-auth build-exec build-proxy test compose-secrets-init compose-up compose-down compose-logs compose-ps clean fmt nix-update-vendor-hashes help

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

test:
	cd $(EXEC_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(PROXY_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(AGENT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(EXEC_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(PROXY_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...

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

fmt:
	gofmt -w $$(rg --files -g '*.go')

nix-update-vendor-hashes:
	./scripts/update-vendor-hashes.sh

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
	@echo "  test          Run Go tests for exec/proxy contracts + agent + exec + proxy"
	@echo "  compose-secrets-init  Seed ignored local Compose secret files from tracked examples"
	@echo "  compose-up    Build and start the local Docker Compose stack"
	@echo "  compose-down  Stop and remove the local Docker Compose stack"
	@echo "  compose-logs  Follow Docker Compose logs (set SERVICE=q15-agent|q15-exec|q15-proxy)"
	@echo "  compose-ps    Show Docker Compose service status"
	@echo "  fmt           Run gofmt over all Go files"
	@echo "  nix-update-vendor-hashes  Recompute flake buildGoModule vendorHash values"
	@echo "  clean         Remove ./bin and Go build/test caches"
	@echo ""
	@echo "Notes:"
	@echo "  - compose uses tracked YAML config examples plus ignored local secret files under ./deploy/compose"
	@echo "  - q15-auth is the interactive bootstrap tool for generating auth.json outside the runtime containers"
