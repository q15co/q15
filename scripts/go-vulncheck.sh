#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root
use_repo_tools

export CGO_ENABLED="${CGO_ENABLED:-0}"

mapfile -t modules < <(list_go_modules)
[[ ${#modules[@]} -gt 0 ]] || die "no Go modules found in go.work"

# The Go vulnerability database records these Ollama application advisories at
# module level without affected package or symbol metadata. q15 links the API
# client and does not build the Ollama server. Keep the exclusions scoped to
# the agent module so every other module and future advisory ID still fails.
agent_ollama_server_exclusions=(
	GO-2025-3557
	GO-2025-3558
	GO-2025-3559
	GO-2025-3582
	GO-2025-3689
	GO-2025-3695
	GO-2025-3824
	GO-2025-4251
)

is_excluded() {
	local module_dir="$1"
	local vulnerability_id="$2"
	local excluded_id

	[[ ${module_dir} == "./systems/agent" ]] || return 1
	for excluded_id in "${agent_ollama_server_exclusions[@]}"; do
		[[ ${vulnerability_id} == "${excluded_id}" ]] && return 0
	done
	return 1
}

for module_dir in "${modules[@]}"; do
	result_file="$(mktemp)"
	trap 'rm -f "${result_file}"' EXIT

	log "govulncheck ${module_dir}"
	(
		cd "${REPO_ROOT}/${module_dir}"
		govulncheck -format=json ./...
	) >"${result_file}"

	mapfile -t vulnerability_ids < <(
		jq -r '
      select(.finding != null and any(.finding.trace[]?; has("function")))
      | .finding.osv
    ' "${result_file}" | sort -u
	)

	unexpected_ids=()
	for vulnerability_id in "${vulnerability_ids[@]}"; do
		if ! is_excluded "${module_dir}" "${vulnerability_id}"; then
			unexpected_ids+=("${vulnerability_id}")
		fi
	done

	if [[ ${#unexpected_ids[@]} -gt 0 ]]; then
		log "reachable vulnerabilities found in ${module_dir}: ${unexpected_ids[*]}"
		log "run govulncheck ./... in ${module_dir} for call traces"
		exit 1
	fi

	if [[ ${#vulnerability_ids[@]} -eq 0 ]]; then
		log "no reachable vulnerabilities in ${module_dir}"
	else
		log "only documented Ollama application exclusions found in ${module_dir}: ${vulnerability_ids[*]}"
	fi

	rm -f "${result_file}"
	trap - EXIT
done
