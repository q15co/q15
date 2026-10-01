#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
impact_script="${SCRIPT_DIR}/image-build-impact.sh"

assert_impact() {
	local expected="$1"
	shift
	local actual

	actual="$(printf '%s\n' "$@" | "${impact_script}")"
	if [[ ${actual} != "${expected}" ]]; then
		printf 'image build impact mismatch\nexpected: %s\nactual:   %s\nfiles:\n' "${expected}" "${actual}" >&2
		printf '  %s\n' "$@" >&2
		exit 1
	fi
}

assert_impact '{"agent":true,"exec":false,"proxy":false,"web":false}' \
	'systems/agent/internal/app/bot.go'
assert_impact '{"agent":false,"exec":true,"proxy":false,"web":false}' \
	'systems/exec/internal/service/grpc.go'
assert_impact '{"agent":false,"exec":false,"proxy":true,"web":false}' \
	'systems/proxy/internal/service/grpc.go'
assert_impact '{"agent":true,"exec":true,"proxy":false,"web":false}' \
	'libs/exec-contract/proto/q15/exec/v1/execution.proto'
assert_impact '{"agent":true,"exec":false,"proxy":false,"web":true}' \
	'libs/chat-contract/proto/q15/chat/v1/chat.proto'
assert_impact '{"agent":false,"exec":true,"proxy":true,"web":false}' \
	'libs/proxy-contract/proto/q15/proxy/v1/proxy.proto'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":true}' \
	'go.work'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":true}' \
	'.dockerignore'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":false}' \
	'systems/agent/main.go' \
	'systems/exec/main.go' \
	'systems/proxy/main.go'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":true}' \
	'systems/web/internal/server/socket.go' \
	'docker/web.Dockerfile'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":false}' \
	'README.md' \
	'deploy/compose/README.md'

printf 'image build impact tests passed\n'
