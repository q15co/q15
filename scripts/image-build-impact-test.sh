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

assert_impact '{"agent":true,"exec":false,"proxy":false,"web":false,"ui":false}' \
	'systems/agent/internal/app/bot.go'
assert_impact '{"agent":false,"exec":true,"proxy":false,"web":false,"ui":false}' \
	'systems/exec/internal/service/grpc.go'
assert_impact '{"agent":false,"exec":false,"proxy":true,"web":false,"ui":false}' \
	'systems/proxy/internal/service/grpc.go'
assert_impact '{"agent":true,"exec":true,"proxy":false,"web":false,"ui":false}' \
	'libs/exec-contract/proto/q15/exec/v1/execution.proto'
assert_impact '{"agent":true,"exec":false,"proxy":false,"web":true,"ui":false}' \
	'libs/chat-contract/proto/q15/chat/v1/chat.proto'
assert_impact '{"agent":false,"exec":true,"proxy":true,"web":false,"ui":false}' \
	'libs/proxy-contract/proto/q15/proxy/v1/proxy.proto'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":true,"ui":false}' \
	'go.work'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":true,"ui":true}' \
	'.dockerignore'
assert_impact '{"agent":true,"exec":true,"proxy":true,"web":false,"ui":false}' \
	'systems/agent/main.go' \
	'systems/exec/main.go' \
	'systems/proxy/main.go'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":true,"ui":false}' \
	'systems/web/internal/server/socket.go'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":false,"ui":false}' \
	'README.md' \
	'deploy/compose/README.md'

assert_impact '{"agent":false,"exec":false,"proxy":false,"web":true,"ui":true}' \
	'systems/web/ui/src/app.tsx'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":true,"ui":true}' \
	'docker/web.Dockerfile' \
	'scripts/tool-versions.sh'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":false,"ui":true}' \
	'scripts/lib/common.sh' \
	'Makefile'
assert_impact '{"agent":false,"exec":false,"proxy":false,"web":true,"ui":false}' \
	'systems/web/internal/protocol/testdata/frames.json'

printf 'image build impact tests passed\n'
