#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
flake_file="${repo_root}/flake.nix"
flake_ref="path:${repo_root}"
fake_hash="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

if [[ ! -f ${flake_file} ]]; then
	echo "error: missing ${flake_file}" >&2
	exit 1
fi

log_file="$(mktemp)"
trap 'rm -f "${log_file}"' EXIT

set_vendor_hash() {
	local pname="$1"
	local hash="$2"

	P_NAME="${pname}" V_HASH="${hash}" perl -0777 -i -pe '
    my $p = $ENV{P_NAME};
    my $h = $ENV{V_HASH};
    s/(pname = "\Q$p\E";.*?vendorHash = )"[^"]+";/$1"$h";/s
      or die "failed to set vendorHash for $p\n";
  ' "${flake_file}"
}

extract_hash() {
	sed -n 's/.*got:[[:space:]]*\(sha256-[A-Za-z0-9+\/=]*\).*/\1/p' "${log_file}" | tail -n1
}

refresh_hash_for() {
	local attr="$1"
	local pname="$2"

	set_vendor_hash "${pname}" "${fake_hash}"

	if nix build "${flake_ref}#${attr}" --no-link >"${log_file}" 2>&1; then
		echo "error: expected hash mismatch while refreshing ${attr}, but build succeeded" >&2
		exit 1
	fi

	local resolved_hash
	resolved_hash="$(extract_hash)"
	if [[ -z ${resolved_hash} ]]; then
		echo "error: failed to extract vendor hash for ${attr}" >&2
		cat "${log_file}" >&2
		exit 1
	fi

	set_vendor_hash "${pname}" "${resolved_hash}"
	echo "updated ${attr} vendorHash -> ${resolved_hash}"
}

refresh_hash_for "q15-agent" "q15-agent"
refresh_hash_for "q15-exec-service" "q15-exec-service"
refresh_hash_for "q15-proxy-service" "q15-proxy-service"
refresh_hash_for "q15-sandbox-helper" "q15-sandbox-helper"

nix build "${flake_ref}#q15" --no-link
echo "vendor hashes refreshed and flake build validated"
