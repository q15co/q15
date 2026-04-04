#!/usr/bin/env bash

if [[ -n ${Q15_COMMON_SH_LOADED:-} ]]; then
	return 0
fi
Q15_COMMON_SH_LOADED=1

COMMON_SH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${COMMON_SH_DIR}/../.." && pwd)"
TOOLS_DIR="${REPO_ROOT}/.tools"
TOOLS_BIN_DIR="${TOOLS_DIR}/bin"
TOOLS_PYTHON_DIR="${TOOLS_DIR}/python"
TOOLS_NODE_DIR="${TOOLS_DIR}/node"
TOOLS_DOWNLOAD_DIR="${TOOLS_DIR}/downloads"
TOOLS_RUSTUP_HOME="${TOOLS_DIR}/rustup"
TOOLS_CARGO_HOME="${TOOLS_DIR}/cargo"
TOOLS_MANIFEST_STAMP="${TOOLS_DIR}/.manifest.applied"
VERSIONS_FILE="${REPO_ROOT}/scripts/tool-versions.sh"
MARKDOWNLINT_CONFIG="${REPO_ROOT}/.markdownlint.json"

export COMMON_SH_DIR
export REPO_ROOT
export TOOLS_DIR
export TOOLS_BIN_DIR
export TOOLS_PYTHON_DIR
export TOOLS_NODE_DIR
export TOOLS_DOWNLOAD_DIR
export TOOLS_RUSTUP_HOME
export TOOLS_CARGO_HOME
export TOOLS_MANIFEST_STAMP
export VERSIONS_FILE
export MARKDOWNLINT_CONFIG

# shellcheck disable=SC1090
source "${VERSIONS_FILE}"

log() {
	printf '%s\n' "$*" >&2
}

die() {
	log "error: $*"
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

ensure_repo_root() {
	cd "${REPO_ROOT}" || exit 1
}

use_repo_tools() {
	export PATH="${TOOLS_BIN_DIR}:${PATH}"
}

go_required_series() {
	awk '
    /^go[[:space:]]+/ {
      split($2, parts, ".")
      print parts[1] "." parts[2]
      exit
    }
  ' "${REPO_ROOT}/go.work"
}

current_go_series() {
	local go_version

	go_version="$(go env GOVERSION 2>/dev/null || true)"
	if [[ -z ${go_version} ]]; then
		go_version="$(go version | awk '{print $3}')"
	fi

	go_version="${go_version#go}"
	IFS=. read -r major minor _ <<<"${go_version}"
	printf '%s.%s\n' "${major}" "${minor}"
}

assert_go_series() {
	local current required

	required="$(go_required_series)"
	current="$(current_go_series)"
	if [[ ${current} != "${required}" ]]; then
		die "Go ${required}.x is required by go.work, found ${current}.x"
	fi
}

write_manifest_stamp() {
	mkdir -p "${TOOLS_DIR}"
	{
		printf 'GO_REQUIRED_SERIES=%s\n' "$(go_required_series)"
		cat "${VERSIONS_FILE}"
	} >"${TOOLS_MANIFEST_STAMP}"
}

manifest_is_current() {
	local expected

	[[ -f ${TOOLS_MANIFEST_STAMP} ]] || return 1
	expected="$(mktemp)"
	{
		printf 'GO_REQUIRED_SERIES=%s\n' "$(go_required_series)"
		cat "${VERSIONS_FILE}"
	} >"${expected}"
	if cmp -s "${TOOLS_MANIFEST_STAMP}" "${expected}"; then
		rm -f "${expected}"
		return 0
	fi
	rm -f "${expected}"
	return 1
}

collect_files() {
	local mode="${1:-staged}"

	if [[ -n ${FILES:-} ]]; then
		printf '%s\n' "${FILES}" | tr ' ' '\n'
		return 0
	fi

	case "${mode}" in
	tracked)
		git -C "${REPO_ROOT}" ls-files
		;;
	staged)
		git -C "${REPO_ROOT}" diff --cached --name-only --diff-filter=ACMR
		;;
	*)
		die "unknown file collection mode: ${mode}"
		;;
	esac
}

collect_existing_files() {
	local mode="${1:-staged}"

	collect_files "${mode}" | awk 'NF && !seen[$0]++' | while IFS= read -r rel; do
		[[ -f "${REPO_ROOT}/${rel}" ]] || continue
		printf '%s\n' "${rel}"
	done
}

is_markdown_file() {
	[[ $1 == *.md ]]
}

is_yaml_file() {
	[[ $1 == *.yaml || $1 == *.yml ]]
}

is_json_file() {
	[[ $1 == *.json ]]
}

is_nix_file() {
	[[ $1 == *.nix ]]
}

is_go_file() {
	[[ $1 == *.go ]]
}

is_workflow_file() {
	[[ $1 == .github/workflows/* ]] && is_yaml_file "$1"
}

is_shell_file() {
	local abs_path="${REPO_ROOT}/$1"

	case "$1" in
	*.sh | *.bash | *.zsh | .envrc)
		return 0
		;;
	esac

	[[ -f ${abs_path} ]] || return 1
	head -n 1 "${abs_path}" | grep -Eq '^#!.*\b(bash|sh|zsh)\b'
}

is_probably_text_file() {
	local abs_path="${REPO_ROOT}/$1"

	[[ -f ${abs_path} ]] || return 1
	if [[ ! -s ${abs_path} ]]; then
		return 0
	fi
	LC_ALL=C grep -Iq . "${abs_path}"
}

append_file() {
	local array_name="$1"

	eval "${array_name}+=(\"\${2}\")"
}

list_go_modules() {
	awk '
    /^use[[:space:]]*\($/ {
      in_block = 1
      next
    }
    in_block && /^[[:space:]]*\)/ {
      in_block = 0
      next
    }
    in_block {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $0)
      if ($0 != "") {
        print $0
      }
      next
    }
    /^use[[:space:]]+/ {
      sub(/^use[[:space:]]+/, "", $0)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $0)
      if ($0 != "") {
        print $0
      }
    }
  ' "${REPO_ROOT}/go.work"
}
