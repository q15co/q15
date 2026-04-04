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

	if LC_ALL=C grep -nH -E '[[:blank:]]$' "${abs_path}" >/dev/null; then
		LC_ALL=C grep -nH -E '[[:blank:]]$' "${abs_path}" >&2
		status=1
	fi
done

exit "${status}"
