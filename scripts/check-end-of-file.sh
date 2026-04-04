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
	is_probably_text_file "${rel_path}" || continue

	if [[ -s ${abs_path} ]] && [[ "$(tail -c 1 "${abs_path}" | wc -l | tr -d '[:space:]')" -eq 0 ]]; then
		log "${rel_path}: missing trailing newline"
		status=1
	fi
done

exit "${status}"
