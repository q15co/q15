#!/usr/bin/env bash
set -euo pipefail

agent=false
exec_service=false
proxy=false

matches_any() {
	local file="$1"
	shift
	local pattern

	for pattern in "$@"; do
		# shellcheck disable=SC2254 # Intentional path glob matching.
		case "${file}" in
		${pattern}) return 0 ;;
		esac
	done
	return 1
}

while IFS= read -r file; do
	[[ -n ${file} ]] || continue

	if matches_any "${file}" \
		"systems/agent/**" \
		"libs/exec-contract/**" \
		".dockerignore" \
		"go.work" \
		"go.work.sum" \
		"docker/agent.Dockerfile"; then
		agent=true
	fi

	if matches_any "${file}" \
		"systems/exec/**" \
		"libs/exec-contract/**" \
		"libs/proxy-contract/**" \
		".dockerignore" \
		"go.work" \
		"go.work.sum" \
		"docker/exec.Dockerfile"; then
		exec_service=true
	fi

	if matches_any "${file}" \
		"systems/proxy/**" \
		"libs/proxy-contract/**" \
		".dockerignore" \
		"go.work" \
		"go.work.sum" \
		"docker/proxy.Dockerfile"; then
		proxy=true
	fi
done

printf '{"agent":%s,"exec":%s,"proxy":%s}\n' "${agent}" "${exec_service}" "${proxy}"
