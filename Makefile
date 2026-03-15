GO ?= go
DOCKER_COMPOSE ?= docker compose
BIN_DIR ?= bin
CONFIG ?= $(HOME)/.config/q15/config.toml
PROXY_CONFIG ?= $(HOME)/.config/q15/proxy-service.toml
COMPOSE_FILE ?= docker-compose.yml
COMPOSE_PROJECT_NAME ?= q15-local
COMPOSE_CONFIG ?= $(HOME)/.config/q15/config.compose.toml
COMPOSE_PROXY_CONFIG ?= $(HOME)/.config/q15/proxy-service.compose.toml
COMPOSE_AUTH ?= $(HOME)/.config/q15/auth.json
COMPOSE_WORKSPACES_HOST_DIR ?= $(HOME)/q15-workspaces
COMPOSE_SKILLS_HOST_DIR ?= $(HOME)/.q15-skills
COMPOSE_EXEC_WORKSPACE_HOST_DIR ?= $(COMPOSE_WORKSPACES_HOST_DIR)/jared
COMPOSE_EXEC_MEMORY_HOST_DIR ?= $(COMPOSE_EXEC_WORKSPACE_HOST_DIR)/.q15-memory
EXEC_SERVICE_LISTEN ?= 127.0.0.1:50051
PROXY_ADMIN_ADDRESS ?= 127.0.0.1:50052
AGENT_MOD_DIR ?= systems/agent
EXEC_SERVICE_MOD_DIR ?= systems/exec-service
PROXY_SERVICE_MOD_DIR ?= systems/proxy-service
EXEC_CONTRACT_MOD_DIR ?= libs/exec-contract
PROXY_CONTRACT_MOD_DIR ?= libs/proxy-contract

RUN_ENV := GOMAXPROCS=1 GODEBUG=updatemaxprocs=0
COMPOSE_ENV := COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) Q15_COMPOSE_CONFIG=$(COMPOSE_CONFIG) Q15_COMPOSE_PROXY_CONFIG=$(COMPOSE_PROXY_CONFIG) Q15_COMPOSE_AUTH=$(COMPOSE_AUTH) Q15_COMPOSE_WORKSPACES_HOST_DIR=$(COMPOSE_WORKSPACES_HOST_DIR) Q15_COMPOSE_SKILLS_HOST_DIR=$(COMPOSE_SKILLS_HOST_DIR) Q15_COMPOSE_EXEC_WORKSPACE_HOST_DIR=$(COMPOSE_EXEC_WORKSPACE_HOST_DIR) Q15_COMPOSE_EXEC_MEMORY_HOST_DIR=$(COMPOSE_EXEC_MEMORY_HOST_DIR)

.DEFAULT_GOAL := build

.PHONY: all build build-main build-exec-service build-proxy-service test run run-local-stack compose-up compose-down compose-logs compose-ps clean fmt nix-update-vendor-hashes help

all: build

build: build-main build-exec-service build-proxy-service

build-main:
	@mkdir -p $(BIN_DIR)
	cd $(AGENT_MOD_DIR) && $(GO) build -o ../../$(BIN_DIR)/q15 .

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

run: build
	@if [ ! -f "$(CONFIG)" ]; then echo "missing config: $(CONFIG)"; exit 1; fi
	$(RUN_ENV) ./$(BIN_DIR)/q15 start --config $(CONFIG)

run-local-stack: build
	@if [ ! -f "$(CONFIG)" ]; then echo "missing config: $(CONFIG)"; exit 1; fi
	@if [ ! -f "$(PROXY_CONFIG)" ]; then echo "missing proxy config: $(PROXY_CONFIG)"; exit 1; fi
	@set -eu; \
		proxy_log=$$(mktemp -t q15-proxy-service.XXXXXX.log); \
		exec_log=$$(mktemp -t q15-exec-service.XXXXXX.log); \
		cleanup() { \
			status=$$?; \
			trap - INT TERM EXIT; \
			for pid in $${agent_pid:-} $${exec_pid:-} $${proxy_pid:-}; do \
				if [ -n "$$pid" ]; then \
					kill "$$pid" 2>/dev/null || true; \
				fi; \
			done; \
			wait $${agent_pid:-} $${exec_pid:-} $${proxy_pid:-} 2>/dev/null || true; \
			exit $$status; \
		}; \
		trap cleanup INT TERM EXIT; \
		echo "proxy-service log: $$proxy_log"; \
		echo "exec-service log: $$exec_log"; \
		./$(BIN_DIR)/q15-proxy-service serve --config "$(PROXY_CONFIG)" >"$$proxy_log" 2>&1 & \
		proxy_pid=$$!; \
		sleep 1; \
		if ! kill -0 "$$proxy_pid" 2>/dev/null; then \
			cat "$$proxy_log"; \
			exit 1; \
		fi; \
		./$(BIN_DIR)/q15-exec-service serve --listen "$(EXEC_SERVICE_LISTEN)" --proxy-admin-address "$(PROXY_ADMIN_ADDRESS)" >"$$exec_log" 2>&1 & \
		exec_pid=$$!; \
		sleep 1; \
		if ! kill -0 "$$exec_pid" 2>/dev/null; then \
			cat "$$exec_log"; \
			exit 1; \
		fi; \
		echo "started proxy-service ($$proxy_pid) and exec-service ($$exec_pid)"; \
		echo "starting q15 with config $(CONFIG)"; \
		$(RUN_ENV) ./$(BIN_DIR)/q15 start --config "$(CONFIG)" & \
		agent_pid=$$!; \
		wait "$$agent_pid"

compose-up:
	@if [ ! -f "$(COMPOSE_CONFIG)" ]; then echo "missing compose config: $(COMPOSE_CONFIG)"; exit 1; fi
	@if [ ! -f "$(COMPOSE_PROXY_CONFIG)" ]; then echo "missing compose proxy config: $(COMPOSE_PROXY_CONFIG)"; exit 1; fi
	@if [ ! -f "$(COMPOSE_AUTH)" ]; then echo "missing auth store: $(COMPOSE_AUTH)"; exit 1; fi
	@if [ -z "$${JARED_GH_TOKEN:-}" ]; then echo "missing env: JARED_GH_TOKEN"; exit 1; fi
	@mkdir -p "$(COMPOSE_WORKSPACES_HOST_DIR)" "$(COMPOSE_EXEC_WORKSPACE_HOST_DIR)" "$(COMPOSE_EXEC_MEMORY_HOST_DIR)" "$(COMPOSE_SKILLS_HOST_DIR)"
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
	@echo "  build         Build q15, q15-exec-service, and q15-proxy-service into ./bin"
	@echo "  build-main    Build ./bin/q15 from $(AGENT_MOD_DIR)"
	@echo "  build-exec-service  Build ./bin/q15-exec-service from $(EXEC_SERVICE_MOD_DIR)"
	@echo "  build-proxy-service  Build ./bin/q15-proxy-service from $(PROXY_SERVICE_MOD_DIR)"
	@echo "  test          Run Go tests for exec/proxy contracts + agent + exec service + proxy service"
	@echo "  run           Build and start q15 with dev runtime defaults"
	@echo "  run-local-stack  Build and start q15-proxy-service + q15-exec-service + q15 with local config files"
	@echo "  compose-up    Build and start the local Docker Compose stack (agent + exec-service + proxy-service)"
	@echo "  compose-down  Stop and remove the local Docker Compose stack"
	@echo "  compose-logs  Follow Docker Compose logs (optionally set SERVICE=agent|exec-service|proxy-service)"
	@echo "  compose-ps    Show Docker Compose service status"
	@echo "  fmt           Run gofmt over all Go files"
	@echo "  nix-update-vendor-hashes  Recompute flake buildGoModule vendorHash values"
	@echo "  clean         Remove ./bin and Go build/test caches"
	@echo ""
	@echo "Notes:"
	@echo "  - Set MOONSHOT_API_KEY before running the app"
	@echo "  - Export any proxy-service secret env vars (for example JARED_GH_TOKEN) before run-local-stack"
	@echo "  - Override CONFIG=... and PROXY_CONFIG=... to use different local config files"
	@echo "  - compose-up uses $(COMPOSE_CONFIG), $(COMPOSE_PROXY_CONFIG), and $(COMPOSE_AUTH)"
