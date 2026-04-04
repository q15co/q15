#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

mode="staged"
if [[ ${1:-} == "--tracked" ]]; then
	mode="tracked"
	shift
fi

[[ $# -eq 0 ]] || die "unexpected arguments: $*"

ensure_repo_root
use_repo_tools

mapfile -t files < <(collect_existing_files "${mode}")
if [[ ${#files[@]} -eq 0 ]]; then
	log "no files to lint"
	exit 0
fi

go_files=()
markdown_files=()
yaml_files=()
json_files=()
nix_files=()
shell_files=()
workflow_files=()

for rel_path in "${files[@]}"; do
	is_go_file "${rel_path}" && append_file go_files "${rel_path}"
	is_markdown_file "${rel_path}" && append_file markdown_files "${rel_path}"
	is_yaml_file "${rel_path}" && append_file yaml_files "${rel_path}"
	is_json_file "${rel_path}" && append_file json_files "${rel_path}"
	is_nix_file "${rel_path}" && append_file nix_files "${rel_path}"
	is_shell_file "${rel_path}" && append_file shell_files "${rel_path}"
	is_workflow_file "${rel_path}" && append_file workflow_files "${rel_path}"
done

"${SCRIPT_DIR}/check-added-large-files.sh" "${files[@]}"
"${SCRIPT_DIR}/check-merge-conflicts.sh" "${files[@]}"
"${SCRIPT_DIR}/check-end-of-file.sh" "${files[@]}"
"${SCRIPT_DIR}/check-trailing-whitespace.sh" "${files[@]}"

if [[ ${#json_files[@]} -gt 0 ]]; then
	"${SCRIPT_DIR}/check-json-files.sh" "${json_files[@]}"
fi

if [[ ${#yaml_files[@]} -gt 0 ]]; then
	"${SCRIPT_DIR}/check-yaml-files.sh" "${yaml_files[@]}"
fi

if [[ ${#go_files[@]} -gt 0 ]]; then
	gofmt_output="$(gofmt -l "${go_files[@]}")"
	if [[ -n ${gofmt_output} ]]; then
		printf 'gofmt needs to run on:\n%s\n' "${gofmt_output}" >&2
		exit 1
	fi

	golines_output="$(golines --ignore-generated --dry-run "${go_files[@]}")"
	if [[ -n ${golines_output} ]]; then
		printf '%s\n' "${golines_output}" >&2
		exit 1
	fi
fi

if [[ ${#markdown_files[@]} -gt 0 ]]; then
	mdformat --check --extensions gfm --extensions frontmatter --wrap 100 "${markdown_files[@]}"
	markdownlint -c "${MARKDOWNLINT_CONFIG}" "${markdown_files[@]}"
fi

if [[ ${#shell_files[@]} -gt 0 ]]; then
	shfmt -d -ln auto -s "${shell_files[@]}"
	shellcheck "${shell_files[@]}"
fi

if [[ ${#workflow_files[@]} -gt 0 ]]; then
	actionlint "${workflow_files[@]}"
fi

if [[ ${#nix_files[@]} -gt 0 ]]; then
	alejandra --check "${nix_files[@]}"
	deadnix --fail "${nix_files[@]}"
	for rel_path in "${nix_files[@]}"; do
		statix check --format errfmt "${rel_path}"
	done
fi
