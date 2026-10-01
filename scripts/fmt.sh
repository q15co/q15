#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

mode="tracked"
if [[ ${1:-} == "--staged" ]]; then
	mode="staged"
	shift
fi

[[ $# -eq 0 ]] || die "unexpected arguments: $*"

ensure_repo_root
use_repo_tools

mapfile -t files < <(collect_existing_files "${mode}")
if [[ ${#files[@]} -eq 0 ]]; then
	log "no files to format"
	exit 0
fi

go_files=()
markdown_files=()
nix_files=()
shell_files=()
typescript_files=()

for rel_path in "${files[@]}"; do
	is_go_file "${rel_path}" && append_file go_files "${rel_path}"
	is_markdown_file "${rel_path}" && append_file markdown_files "${rel_path}"
	is_nix_file "${rel_path}" && append_file nix_files "${rel_path}"
	is_shell_file "${rel_path}" && append_file shell_files "${rel_path}"
	is_typescript_file "${rel_path}" && append_file typescript_files "${REPO_ROOT}/${rel_path}"
	if [[ ${rel_path} == systems/web/ui/* ]]; then
		case "${rel_path}" in
		*.css | *.html | *.json | *.webmanifest) append_file typescript_files "${REPO_ROOT}/${rel_path}" ;;
		esac
	fi
done

if [[ ${#typescript_files[@]} -gt 0 ]]; then
	ui_tool fmt "${typescript_files[@]}"
fi

if [[ ${#go_files[@]} -gt 0 ]]; then
	gofmt -w "${go_files[@]}"
	golines --ignore-generated --write-output "${go_files[@]}"
fi

if [[ ${#markdown_files[@]} -gt 0 ]]; then
	mdformat --extensions gfm --extensions frontmatter --wrap 100 "${markdown_files[@]}"
	markdownlint --fix -c "${MARKDOWNLINT_CONFIG}" "${markdown_files[@]}"
fi

if [[ ${#shell_files[@]} -gt 0 ]]; then
	shfmt -w -l -ln auto -s "${shell_files[@]}"
fi

if [[ ${#nix_files[@]} -gt 0 ]]; then
	alejandra "${nix_files[@]}"
fi
