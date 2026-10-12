#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

ensure_repo_root

# install.sh is downloaded and piped into a shell on someone else's machine, so
# it cannot read a file beside it: the release public key and the verifier pins
# live inside the script, and CI is what keeps those copies honest.
install_script="${REPO_ROOT}/install.sh"
key_file="${REPO_ROOT}/deploy/install/q15.pub"
workflow_file="${REPO_ROOT}/.github/workflows/release.yml"

[[ -f ${install_script} ]] || die "missing install.sh"
[[ -f ${key_file} ]] || die "missing deploy/install/q15.pub"

embedded_key="$(sh "${install_script}" --print-public-key)"
checked_in_key="$(cat "${key_file}")"
if [[ ${embedded_key} != "${checked_in_key}" ]]; then
	log "install.sh:                ${embedded_key}"
	log "deploy/install/q15.pub:    ${checked_in_key}"
	die "install.sh embeds a different public key than deploy/install/q15.pub"
fi

# The installer downloads a pinned verifier and the release workflow signs with
# one. They have to be the same artefact, or a release could be signed by a
# verifier the installer would never accept.
script_pin="$(grep -m1 -E '^MINISIGN_TARBALL_SHA256=' "${install_script}" | cut -d"'" -f2)"
workflow_pin="$(grep -m1 -E '^ +MINISIGN_TARBALL_SHA256: ' "${workflow_file}" |
	cut -d: -f2 | tr -d ' "' || true)"
[[ -n ${script_pin} ]] || die "install.sh does not pin MINISIGN_TARBALL_SHA256"
[[ -n ${workflow_pin} ]] || die "the release workflow does not pin MINISIGN_TARBALL_SHA256"
if [[ ${script_pin} != "${workflow_pin}" ]]; then
	log "install.sh:             ${script_pin}"
	log ".github/workflows:      ${workflow_pin}"
	die "the installer and the release workflow pin different signature verifiers"
fi

log "install.sh embeds the checked-in release key and the release verifier pin"
