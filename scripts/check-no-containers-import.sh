#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root
use_repo_tools

# Podman's Go bindings must not reach the agent module: they cost a 33 MB
# binary and hundreds of modules for a CLI-only reason, and they widen the
# vulnerability-scan surface. Read-only podman interaction shells out instead.
#
# Two checks, because neither is sufficient alone. The import graph is the
# truth about what a build actually links; the source scan additionally sees an
# import that a build tag keeps out of this platform's graph.
agent_module="systems/agent"
banned_prefix="github.com/containers/"

imports="$(cd "${REPO_ROOT}/${agent_module}" && go list -deps -test ./...)" ||
	die "go list failed for ${agent_module}"

banned="$(printf '%s\n' "${imports}" | grep -F "${banned_prefix}" || true)"
if [[ -n ${banned} ]]; then
	log "${banned}"
	die "${agent_module} links ${banned_prefix}: shell out to podman instead of importing its bindings"
fi

banned_sources="$(grep -rIl --include='*.go' -F "\"${banned_prefix}" "${REPO_ROOT}/${agent_module}" || true)"
if [[ -n ${banned_sources} ]]; then
	log "${banned_sources}"
	die "${agent_module} imports ${banned_prefix} in source, possibly behind a build tag"
fi

log "no ${banned_prefix} imports in ${agent_module}"
