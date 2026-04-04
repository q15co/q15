#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

[[ $# -eq 2 ]] || die "usage: $0 <base-sha> <head-sha>"

base_sha="$1"
head_sha="$2"

ensure_repo_root

if [[ -z ${base_sha} || ${base_sha} =~ ^0+$ ]]; then
	git -C "${REPO_ROOT}" diff-tree --no-commit-id --name-only -r --diff-filter=ACMR "${head_sha}"
	exit 0
fi

git -C "${REPO_ROOT}" diff --name-only --diff-filter=ACMR "${base_sha}" "${head_sha}"
