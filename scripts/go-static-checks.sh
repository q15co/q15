#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root
use_repo_tools

export CGO_ENABLED="${CGO_ENABLED:-0}"

cleanup_revive_log() {
	rm -f "${REPO_ROOT}/revive.log"
}

trap cleanup_revive_log EXIT
cleanup_revive_log

mapfile -t modules < <(list_go_modules)
[[ ${#modules[@]} -gt 0 ]] || die "no Go modules found in go.work"

for module_dir in "${modules[@]}"; do
	log "go vet ${module_dir}"
	(
		cd "${REPO_ROOT}/${module_dir}"
		go vet ./...
	)

	log "staticcheck ${module_dir}"
	(
		cd "${REPO_ROOT}/${module_dir}"
		staticcheck ./...
	)

	log "revive ${module_dir}"
	(
		cd "${REPO_ROOT}/${module_dir}"
		revive -formatter friendly -set_exit_status ./...
	)
	cleanup_revive_log

	log "golangci-lint ${module_dir}"
	(
		cd "${REPO_ROOT}/${module_dir}"
		golangci-lint run --config "${REPO_ROOT}/.golangci.yml" ./...
	)
done
