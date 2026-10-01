#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root
for layer in protocol server; do
	for source in "systems/web/internal/${layer}/testdata/"*.json; do
		copy="systems/web/ui/src/fixtures/${layer}/$(basename "${source}")"
		cmp -s "${source}" "${copy}" || die "browser fixture drift: ${copy} must match ${source}"
	done
done
generated="$(mktemp)"
trap 'rm -f "${generated}"' EXIT
python3 "${SCRIPT_DIR}/generate-ui-protocol.py" >"${generated}"
cmp -s "${generated}" systems/web/ui/src/generated/protocol.ts || die "browser types drift: regenerate with scripts/generate-ui-protocol.py"
printf 'browser fixtures and generated types match\n'
