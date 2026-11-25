#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root
use_repo_tools
for file in compose.yaml deploy/compose/docker-compose.image-first.yml; do
	yq -o=json '.' "${file}" | jq -e '
		([.services | to_entries[] | select(any(.value.volumes[]?; .source == "q15_bridge")) | .key] | sort) == ["q15-agent", "q15-web"] and
		([.services | to_entries[] | select(any(.value.volumes[]?; .source == "q15_web_state")) | .key] | sort) == ["q15-web"] and
		.services["q15-web"].environment.Q15_WEB_STATE_DIR == "/var/lib/q15-web" and
		(.services["q15-web"].environment | has("Q15_WEB_TOKEN") | not) and
		([.services["q15-web"].volumes[] | select(.source == "q15_web_state") | .target]) == ["/var/lib/q15-web"] and
		(.volumes | has("q15_web_state")) and
		([.services | to_entries[] | select(any(.value.volumes[]?; .source == "q15_memory")) | .key] | sort) == ["q15-agent", "q15-exec"] and
		([.services["q15-exec"].volumes[] | select(.source == "q15_memory") | .read_only]) == [true] and
		([.services["q15-agent"].volumes[] | select(.source == "q15_memory") | (.read_only // false)]) == [false] and
		([.services["q15-agent"].volumes[], .services["q15-web"].volumes[] | select(.source == "q15_bridge") | .target] | unique) == ["/run/q15"] and
		.services["q15-web"].environment.Q15_WEB_BRIDGE == "unix:///run/q15/bridge.sock" and
		.services["q15-web"].user == "65532:1000" and
		.services["q15-web"].ports == ["127.0.0.1:8080:8080"] and
		.services["q15-agent"].group_add == ["1000"] and
		(.volumes | has("q15_bridge"))
	' >/dev/null || die "bridge isolation contract failed: ${file}"
done
yq -o=json '.' compose.yaml | jq -e '
	(.services["q15-agent"].volumes | any(.type == "bind" and .read_only and
		.source == "./deploy/compose/agent-config.yaml" and .target == "/etc/q15/agent/config.yaml")) and
	(.services["q15-proxy"].volumes | any(.type == "bind" and .read_only and
		.source == "./deploy/compose/proxy-policy.yaml" and .target == "/etc/q15/proxy/policy.yaml"))
' >/dev/null || die "local Podman configuration mounts must be read-only binds"
[[ $(yq '.spec.template.spec.containers[] | select(.name == "exec") | .volumeMounts[] | select(.name == "memory") | .readOnly' deploy/kubernetes/base/exec-deployment.yaml) == true ]] || die "exec memory mount must be read-only"
[[ $(yq '.spec.template.spec.containers[] | select(.name == "agent") | .volumeMounts[] | select(.name == "memory") | (.readOnly // false)' deploy/kubernetes/base/agent-deployment.yaml) == false ]] || die "agent memory mount must be writable"
[[ $(yq '.agent.bridge.listen_target' deploy/compose/agent-config.yaml) == 'unix:///run/q15/bridge.sock' ]] || die "agent bridge config differs"
grep -Fq 'DefaultBridgeListenTarget = "unix:///run/q15/bridge.sock"' systems/agent/internal/config/config.go || die "agent socket default differs"
printf 'compose bridge isolation contract passed\n'
