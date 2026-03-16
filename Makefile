GO ?= go
DOCKER_COMPOSE ?= docker compose
BIN_DIR ?= bin
COMPOSE_FILE ?= docker-compose.yml
COMPOSE_PROJECT_NAME ?= q15-local
AGENT_MOD_DIR ?= systems/agent
EXEC_SERVICE_MOD_DIR ?= systems/exec-service
PROXY_SERVICE_MOD_DIR ?= systems/proxy-service
EXEC_CONTRACT_MOD_DIR ?= libs/exec-contract
PROXY_CONTRACT_MOD_DIR ?= libs/proxy-contract

COMPOSE_ENV := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME)

.DEFAULT_GOAL := build

.PHONY: all build build-main build-auth build-exec-service build-proxy-service test compose-secrets-init compose-up compose-down compose-logs compose-ps clean fmt nix-update-vendor-hashes help

all: build

build: build-main build-auth build-exec-service build-proxy-service

build-main:
	@mkdir -p $(BIN_DIR)
	cd $(AGENT_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15 .

build-auth:
	@mkdir -p $(BIN_DIR)
	cd $(AGENT_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-auth ./cmd/q15-auth

build-exec-service:
	@mkdir -p $(BIN_DIR)
	cd $(EXEC_SERVICE_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-exec-service .

build-proxy-service:
	@mkdir -p $(BIN_DIR)
	cd $(PROXY_SERVICE_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15-proxy-service .

test:
	cd $(EXEC_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(PROXY_CONTRACT_MOD_DIR) && $(GO) test ./...
	cd $(AGENT_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(EXEC_SERVICE_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...
	cd $(PROXY_SERVICE_MOD_DIR) && CGO_ENABLED=0 $(GO) test ./...

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
	@echo "  build         Build q15, q15-auth, q15-exec-service, and q15-proxy-service into ./bin"
	@echo "  build-main    Build ./bin/q15 from $(AGENT_MOD_DIR)"
	@echo "  build-auth    Build ./bin/q15-auth from $(AGENT_MOD_DIR)/cmd/q15-auth"
	@echo "  build-exec-service  Build ./bin/q15-exec-service from $(EXEC_SERVICE_MOD_DIR)"
	@echo "  build-proxy-service  Build ./bin/q15-proxy-service from $(PROXY_SERVICE_MOD_DIR)"
	@echo "  test          Run Go tests for exec/proxy contracts + agent + exec service + proxy service"
	@echo "  compose-secrets-init  Seed ignored local Compose secret files from tracked examples"
	@echo "  compose-up    Build and start the local Docker Compose stack"
	@echo "  compose-down  Stop and remove the local Docker Compose stack"
	@echo "  compose-logs  Follow Docker Compose logs (set SERVICE=q15-agent|q15-exec-service|q15-proxy-service)"
	@echo "  compose-ps    Show Docker Compose service status"
	@echo "  fmt           Run gofmt over all Go files"
	@echo "  nix-update-vendor-hashes  Recompute flake buildGoModule vendorHash values"
	@echo "  clean         Remove ./bin and Go build/test caches"
	@echo ""
	@echo "Notes:"
	@echo "  - compose uses tracked YAML config examples plus ignored local secret files under ./deploy/compose"
	@echo "  - q15-auth is the interactive bootstrap tool for generating auth.json outside the runtime containers"
