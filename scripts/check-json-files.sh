#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root
use_repo_tools

for path in "$@"; do
	if ! jq empty "${path}" >/dev/null; then
		printf '%s: invalid JSON\n' "${path}" >&2
		exit 1
	fi
done
