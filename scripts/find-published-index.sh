#!/usr/bin/env bash
#
# Print the tag and digest of the newest immutable image index one commit published.
#
# Every push to main that publishes images tags them with an immutable DateVer tag
# (`<YYYY.MM.DD>.<run-number>`) whose index carries the annotations
# io.q15.release.{version,revision,component,source-digest}. A release needs the
# index its own commit produced, so that the release workflow can alias it under
# the release tag: that tag is the tag a rendered unit pins, and the images have to
# be reachable under it. Nothing else records the mapping, and a commit's DateVer
# is not derivable from a release tag, so it is looked up here.
#
# Usage: find-published-index.sh <image> <revision>
#   <image>    a qualified image reference without a tag, e.g. ghcr.io/q15co/q15-agent
#   <revision> the commit the release was cut from, spelled the long way, because
#              that is what the published index annotates itself with
#
# One line is printed on success: the DateVer tag the index was published under, a
# space, and its digest. The caller needs the tag as well as the digest to insist
# that the four images of one release came from one published release, rather than
# from four lookups that each happened to match.
#
# The access is anonymous on purpose. These images have to be pullable by an
# operator who has no registry credentials, so a check that needed one would be
# checking a different thing than the one that has to work.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

registry="ghcr.io"
# One page of tags is one request, and a release only ever walks the recent ones.
# The guard below refuses rather than reporting "no release found" from a list the
# registry cut short, because "the images were never published" and "the tags did
# not fit on one page" have different remedies.
tag_page_size=1000

if [[ $# -ne 2 ]]; then
	printf 'usage: %s <image> <revision>\n' "${0##*/}" >&2
	exit 2
fi

image="$1"
revision="$2"

[[ ${image} == "${registry}/"* ]] ||
	die "${image} is not a ${registry} image, which is where q15 publishes its images"
repository="${image#"${registry}/"}"
[[ ${revision} =~ ^[0-9a-f]{40}$ ]] ||
	die "${revision} is not a full commit, and a published index annotates itself with the full one"

page="$(mktemp)"
trap 'rm -f "${page}"' EXIT

token="$(
	curl --fail --silent --show-error --location \
		"https://${registry}/token?scope=repository:${repository}:pull&service=${registry}" |
		jq --raw-output '.token // empty'
)"
[[ -n ${token} ]] || die "the registry handed out no pull token for ${repository}"

curl --fail --silent --show-error --location \
	--header "Authorization: Bearer ${token}" \
	"https://${registry}/v2/${repository}/tags/list?n=${tag_page_size}" >"${page}"

tag_count="$(jq --raw-output '.tags | length' "${page}")"
[[ ${tag_count} -lt ${tag_page_size} ]] ||
	die "${repository} answered a full page of ${tag_page_size} tags, so the list may be cut short"

mapfile -t candidates < <(
	jq --raw-output '.tags[]? // empty' "${page}" |
		grep --extended-regexp '^[0-9]{4}\.[0-9]{2}\.[0-9]{2}\.[0-9]+$' |
		sort --version-sort --reverse
)
[[ ${#candidates[@]} -gt 0 ]] || die "${repository} has no release-shaped tag, so nothing was ever published for it"

# Newest first, because a commit is normally released soon after it is published,
# and the walk stops at the first index whose annotation names the revision. A
# rerun of the publish workflow for the same commit tags it twice, which is why the
# newest match wins rather than the first one seen.
for tag in "${candidates[@]}"; do
	index="$(mktemp)"
	headers="$(mktemp)"
	if ! curl --fail --silent --show-error --location \
		--header "Authorization: Bearer ${token}" \
		--header "Accept: application/vnd.oci.image.index.v1+json" \
		--dump-header "${headers}" \
		--output "${index}" \
		"https://${registry}/v2/${repository}/manifests/${tag}"; then
		rm -f "${index}" "${headers}"
		log "skipping ${tag}: the registry did not answer for it"
		continue
	fi

	found="$(jq --raw-output '.annotations["io.q15.release.revision"] // empty' "${index}")"
	if [[ ${found} == "${revision}" ]]; then
		# The digest the registry reports for the bytes it just served is the
		# digest the walk matched on; taking it from the response rather than
		# hashing the body keeps the two from drifting apart.
		digest="$(grep --ignore-case '^docker-content-digest:' "${headers}" |
			tail -n 1 | tr -d '\r' | cut -d ' ' -f 2)"
		printf '%s %s\n' "${tag}" "${digest}"
		rm -f "${index}" "${headers}"
		exit 0
	fi
	rm -f "${index}" "${headers}"
done

die "no published ${repository} index names the revision ${revision}: the images for this commit were never published, so cut the release from a commit whose publish run went green"
