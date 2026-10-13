#!/bin/sh
#
# The q15 bootstrap.
#
# Downloads the release built for this machine, verifies its signature against
# the public key below, installs one file and offers to run the guided setup:
#
#   curl -fsSL https://q15.co/install.sh | sh
#
# It writes one file, $HOME/.local/bin/q15. It never calls sudo, never installs
# a package and never pipes a second script into a shell. Everything else
# happens in a temporary directory that is removed on exit, and nothing is
# installed unless the signature verifies first.
#
# `--print-public-key` prints the key below and exits, so that CI can assert
# this copy still matches deploy/install/q15.pub.
#
set -eu

# The release public key, copied verbatim from deploy/install/q15.pub. A
# release this key does not accept is never installed.
Q15_PUBLIC_KEY='untrusted comment: minisign public key FB777393219F54FA
RWT6VJ8hk3N3+z0UvoNZdjzv3c5hRdQSJ0AVneARqwdeapv3rT5tX+cQ'

# The signature verifier, pinned by hash rather than by version, because the
# download is the thing that has to be right. minisign publishes a single Linux
# tarball carrying both architectures, and both of its binaries are pinned as
# well as the tarball, so a swapped archive cannot pass even if one of the
# hashes were stale.
MINISIGN_VERSION='0.12'
MINISIGN_TARBALL_SHA256='9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73'
MINISIGN_SHA256_X86_64='2c74dffcc1c9a5ee55957c60971998ace2b89f22585631594ec2152c588af8db'
MINISIGN_SHA256_AARCH64='cec9f88be8c975af76854a53b4d49c3d257feae38d916edb0d16fb55aacd3000'
MINISIGN_URL='https://github.com/jedisct1/minisign/releases/download'

# Where the release comes from. Q15_RELEASE_BASE_URL points the script at a
# staging release, a mirror or a local copy while developing. It is not a way
# around the signature: the key above still has to accept what it serves.
Q15_REPOSITORY='q15co/q15'
Q15_RELEASE_BASE_URL="${Q15_RELEASE_BASE_URL:-https://github.com/q15co/q15/releases/download}"

work_dir=''

die() {
	printf 'q15 install: %s\n' "$1" >&2
	exit 1
}

cleanup() {
	if [ -n "${work_dir}" ]; then
		rm -rf "${work_dir}"
		work_dir=''
	fi
}

usage() {
	cat <<'USAGE'
Install q15.

usage: install.sh [options]

  --version TAG  install a particular release instead of the latest one. The
                 tag is the one on the release, for example v0.1.0
  -y, --yes      do not ask before running the guided setup
  -h, --help     print this message

Environment:

  Q15_RELEASE_BASE_URL  fetch the release from somewhere else. The signature
                        still has to verify against the public key embedded in
                        this script.
USAGE
}

# has_terminal reports whether there is a controlling terminal to talk to. It
# matters because a download-and-run is normally piped into a shell, and then
# stdin is the script itself rather than the operator. The subshell keeps the
# shell's own "no such device" complaint off stderr when there is none.
has_terminal() {
	(: </dev/tty) >/dev/null 2>&1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		die 'no sha256 tool is installed (sha256sum or shasum)'
	fi
}

fetch() {
	# fetch URL DESTINATION
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --output "$2" "$1" || die "could not download $1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q --output-document="$2" "$1" || die "could not download $1"
	else
		die 'neither curl nor wget is installed'
	fi
}

# resolve_release fills release_tag and release_version. The tag is vX.Y.Z and
# the archive inside the release is q15_X.Y.Z_linux_ARCH.tar.gz, because
# GoReleaser's version template carries the tag with the v prefix trimmed.
resolve_release() {
	if [ -n "${version_option}" ]; then
		release_tag="${version_option}"
	else
		command -v curl >/dev/null 2>&1 ||
			die 'resolving the latest release needs curl; pass --version TAG'
		latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
			"https://github.com/${Q15_REPOSITORY}/releases/latest")" ||
			die 'could not resolve the latest q15 release'
		release_tag="${latest_url##*/}"
		[ "${release_tag}" != 'latest' ] ||
			die 'could not resolve the latest q15 release'
	fi

	case "${release_tag}" in
	v*) release_version="${release_tag#v}" ;;
	*) release_version="${release_tag}" ;;
	esac
	[ -n "${release_version}" ] || die 'the release tag is empty'
}

install_verifier() {
	verifier_tarball="${work_dir}/minisign-${MINISIGN_VERSION}-linux.tar.gz"
	fetch "${MINISIGN_URL}/${MINISIGN_VERSION}/minisign-${MINISIGN_VERSION}-linux.tar.gz" \
		"${verifier_tarball}"
	[ "$(sha256_of "${verifier_tarball}")" = "${MINISIGN_TARBALL_SHA256}" ] ||
		die 'the signature verifier download does not match its pinned hash'

	mkdir -p "${work_dir}/verifier"
	tar -xzf "${verifier_tarball}" -C "${work_dir}/verifier" \
		"minisign-linux/${arch_dir}/minisign" >/dev/null 2>&1 ||
		die 'the signature verifier archive is not shaped as expected'

	verifier="${work_dir}/verifier/minisign-linux/${arch_dir}/minisign"
	[ "$(sha256_of "${verifier}")" = "${verifier_sha256}" ] ||
		die 'the signature verifier does not match its pinned hash'
	chmod 0755 "${verifier}"
}

verify_release() {
	# verify_release ARCHIVE SIGNATURE
	printf '%s\n' "${Q15_PUBLIC_KEY}" >"${work_dir}/q15.pub"
	if ! "${verifier}" -V -q -p "${work_dir}/q15.pub" -m "$1" 2>"${work_dir}/verify.log"; then
		printf 'q15 install: the release signature did not verify\n' >&2
		if [ -s "${work_dir}/verify.log" ]; then
			cat "${work_dir}/verify.log" >&2 || true
		fi
		die 'refusing to install a release that does not verify'
	fi
}

run_guided_setup() {
	if [ "${assume_yes}" = 'no' ]; then
		if ! has_terminal; then
			printf '\nRun the guided setup when you are ready:\n  q15 init\n'
			return 0
		fi
		printf '\nRun the guided setup now (q15 init)? [y/N] ' >/dev/tty
		answer=''
		read -r answer </dev/tty || answer=''
		case "${answer}" in
		y | Y | yes | YES | Yes) ;;
		*)
			printf 'Skipped. Run it when you are ready:\n  q15 init\n'
			return 0
			;;
		esac
	fi

	# The temporary directory goes before the setup runs, because this is the
	# last thing the script does.
	cleanup
	if has_terminal; then
		exec "${target}" init </dev/tty
	fi
	exec "${target}" init
}

version_option=''
assume_yes='no'

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die '--version needs a release tag'
		version_option="$2"
		shift 2
		;;
	--print-public-key)
		printf '%s\n' "${Q15_PUBLIC_KEY}"
		exit 0
		;;
	-y | --yes)
		assume_yes='yes'
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "unknown option $1"
		;;
	esac
done

[ "$(uname -s)" = 'Linux' ] || die "q15 runs on Linux, not on $(uname -s)"

case "$(uname -m)" in
x86_64 | amd64)
	arch='amd64'
	arch_dir='x86_64'
	verifier_sha256="${MINISIGN_SHA256_X86_64}"
	;;
aarch64 | arm64)
	arch='arm64'
	arch_dir='aarch64'
	verifier_sha256="${MINISIGN_SHA256_AARCH64}"
	;;
*)
	die "there is no q15 release for $(uname -m)"
	;;
esac

[ -n "${HOME:-}" ] || die 'HOME is not set'
bin_dir="${HOME}/.local/bin"
target="${bin_dir}/q15"

resolve_release

work_dir="$(mktemp -d)" || die 'could not create a temporary directory'
trap 'cleanup' EXIT INT TERM HUP

archive_name="q15_${release_version}_linux_${arch}.tar.gz"
archive="${work_dir}/${archive_name}"
release_url="${Q15_RELEASE_BASE_URL}/${release_tag}/${archive_name}"

printf 'Installing q15 %s for linux/%s\n' "${release_version}" "${arch}"

install_verifier
fetch "${release_url}" "${archive}"
fetch "${release_url}.minisig" "${archive}.minisig"
verify_release "${archive}" "${archive}.minisig"

mkdir -p "${work_dir}/unpacked"
tar -xzf "${archive}" -C "${work_dir}/unpacked" q15 >/dev/null 2>&1 ||
	die 'the release archive does not contain q15'

mkdir -p "${bin_dir}"
cp "${work_dir}/unpacked/q15" "${target}"
chmod 0755 "${target}"

printf 'Installed %s\n' "${target}"
case ":${PATH}:" in
*":${bin_dir}:"*) ;;
*)
	# shellcheck disable=SC2016 # $PATH belongs to the operator's shell
	printf '\n%s is not on your PATH. Add it, for example:\n  export PATH="%s:$PATH"\n' \
		"${bin_dir}" "${bin_dir}"
	;;
esac

run_guided_setup
