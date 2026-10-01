#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root
use_repo_tools
jq -e --arg manager "pnpm@${PNPM_VERSION}" --arg node "${NODE_VERSION}" \
	'.packageManager == $manager and .engines.node == $node' systems/web/ui/package.json >/dev/null ||
	die "UI tool pins must match scripts/tool-versions.sh"
