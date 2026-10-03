#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root
use_repo_tools
for file in docker-compose.yml deploy/compose/docker-compose.image-first.yml; do
	yq -o=json '.' "${file}" | jq -e '
		([.services | to_entries[] | select(any(.value.volumes[]?; .source == "q15_bridge")) | .key] | sort) == ["q15-agent", "q15-web"] and
		([.services | to_entries[] | select(any(.value.volumes[]?; .source == "q15_memory")) | .key] | sort) == ["q15-agent", "q15-exec"] and
		([.services["q15-agent"].volumes[], .services["q15-web"].volumes[] | select(.source == "q15_bridge") | .target] | unique) == ["/run/q15"] and
		.services["q15-web"].environment.Q15_WEB_BRIDGE == "unix:///run/q15/bridge.sock" and
		.services["q15-web"].user == "65532:1000" and
		.services["q15-web"].ports == ["127.0.0.1:8080:8080"] and
		.services["q15-agent"].group_add == ["1000"] and
		(.volumes | has("q15_bridge"))
	' >/dev/null || die "bridge isolation contract failed: ${file}"
done
[[ $(yq '.agent.bridge.listen_target' deploy/compose/agent-config.yaml) == 'unix:///run/q15/bridge.sock' ]] || die "agent bridge config differs"
grep -Fq 'DefaultBridgeListenTarget = "unix:///run/q15/bridge.sock"' systems/agent/internal/config/config.go || die "agent socket default differs"
printf 'compose bridge isolation contract passed\n'
