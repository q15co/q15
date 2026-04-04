#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root

status=0
for rel_path in "$@"; do
	abs_path="${REPO_ROOT}/${rel_path}"
	[[ -f ${abs_path} ]] || continue

	size_bytes="$(wc -c <"${abs_path}" | tr -d '[:space:]')"
	if ((size_bytes > MAX_FILE_SIZE_BYTES)); then
		log "${rel_path}: exceeds ${MAX_FILE_SIZE_BYTES} bytes"
		status=1
	fi
done

exit "${status}"
