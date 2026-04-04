#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

check_only=0
if [[ ${1:-} == "--check" ]]; then
	check_only=1
	shift
fi

[[ $# -eq 0 ]] || die "unexpected arguments: $*"

ensure_repo_root

required_tools=(
	golangci-lint
	staticcheck
	revive
	golines
	actionlint
	shfmt
	shellcheck
	jq
	yq
	alejandra
	deadnix
	statix
	mdformat
	markdownlint
)

have_expected_tools() {
	local tool

	for tool in "${required_tools[@]}"; do
		[[ -x "${TOOLS_BIN_DIR}/${tool}" ]] || return 1
	done
	[[ -x "${TOOLS_PYTHON_DIR}/bin/python" ]] || return 1
	[[ -d "${TOOLS_NODE_DIR}/node_modules/markdownlint-cli" ]] || return 1
}

assert_runtime_prereqs() {
	require_command go
	require_command node
}

assert_install_prereqs() {
	require_command curl
	require_command git
	require_command go
	require_command node
	require_command npm
	require_command python3
	require_command tar
}

shellcheck_asset_name() {
	local os arch

	os="$(uname -s)"
	arch="$(uname -m)"
	case "${os}/${arch}" in
	Linux/x86_64)
		printf 'shellcheck-%s.linux.x86_64.tar.xz\n' "${SHELLCHECK_VERSION}"
		;;
	Linux/aarch64 | Linux/arm64)
		printf 'shellcheck-%s.linux.aarch64.tar.xz\n' "${SHELLCHECK_VERSION}"
		;;
	Darwin/x86_64)
		printf 'shellcheck-%s.darwin.x86_64.tar.xz\n' "${SHELLCHECK_VERSION}"
		;;
	Darwin/arm64 | Darwin/aarch64)
		printf 'shellcheck-%s.darwin.aarch64.tar.xz\n' "${SHELLCHECK_VERSION}"
		;;
	*)
		die "unsupported platform for shellcheck bootstrap: ${os}/${arch}"
		;;
	esac
}

rustup_target() {
	local os arch

	os="$(uname -s)"
	arch="$(uname -m)"
	case "${os}/${arch}" in
	Linux/x86_64)
		printf 'x86_64-unknown-linux-gnu\n'
		;;
	Linux/aarch64 | Linux/arm64)
		printf 'aarch64-unknown-linux-gnu\n'
		;;
	Darwin/x86_64)
		printf 'x86_64-apple-darwin\n'
		;;
	Darwin/arm64 | Darwin/aarch64)
		printf 'aarch64-apple-darwin\n'
		;;
	*)
		die "unsupported platform for Rust bootstrap: ${os}/${arch}"
		;;
	esac
}

install_go_tool() {
	local install_target="$1"

	GOBIN="${TOOLS_BIN_DIR}" go install "${install_target}"
}

install_shellcheck() {
	local asset_name download_url extracted_dir tmp_dir

	asset_name="$(shellcheck_asset_name)"
	download_url="https://github.com/koalaman/shellcheck/releases/download/${SHELLCHECK_VERSION}/${asset_name}"
	extracted_dir="shellcheck-${SHELLCHECK_VERSION}"
	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "${tmp_dir}"' RETURN

	curl -fsSL "${download_url}" -o "${tmp_dir}/${asset_name}"
	tar -xf "${tmp_dir}/${asset_name}" -C "${tmp_dir}"
	install -m 0755 "${tmp_dir}/${extracted_dir}/shellcheck" "${TOOLS_BIN_DIR}/shellcheck"

	rm -rf "${tmp_dir}"
	trap - RETURN
}

jq_asset_name() {
	local os arch

	os="$(uname -s)"
	arch="$(uname -m)"
	case "${os}/${arch}" in
	Linux/x86_64)
		printf 'jq-linux-amd64\n'
		;;
	Linux/aarch64 | Linux/arm64)
		printf 'jq-linux-arm64\n'
		;;
	Darwin/x86_64)
		printf 'jq-macos-amd64\n'
		;;
	Darwin/arm64 | Darwin/aarch64)
		printf 'jq-macos-arm64\n'
		;;
	*)
		die "unsupported platform for jq bootstrap: ${os}/${arch}"
		;;
	esac
}

install_jq() {
	local asset_name download_url tmp_dir

	asset_name="$(jq_asset_name)"
	download_url="https://github.com/jqlang/jq/releases/download/${JQ_VERSION}/${asset_name}"
	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "${tmp_dir}"' RETURN

	curl -fsSL "${download_url}" -o "${tmp_dir}/jq"
	install -m 0755 "${tmp_dir}/jq" "${TOOLS_BIN_DIR}/jq"

	rm -rf "${tmp_dir}"
	trap - RETURN
}

yq_asset_name() {
	local os arch

	os="$(uname -s)"
	arch="$(uname -m)"
	case "${os}/${arch}" in
	Linux/x86_64)
		printf 'yq_linux_amd64\n'
		;;
	Linux/aarch64 | Linux/arm64)
		printf 'yq_linux_arm64\n'
		;;
	Darwin/x86_64)
		printf 'yq_darwin_amd64\n'
		;;
	Darwin/arm64 | Darwin/aarch64)
		printf 'yq_darwin_arm64\n'
		;;
	*)
		die "unsupported platform for yq bootstrap: ${os}/${arch}"
		;;
	esac
}

install_yq() {
	local asset_name download_url tmp_dir

	asset_name="$(yq_asset_name)"
	download_url="https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${asset_name}"
	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "${tmp_dir}"' RETURN

	curl -fsSL "${download_url}" -o "${tmp_dir}/yq"
	install -m 0755 "${tmp_dir}/yq" "${TOOLS_BIN_DIR}/yq"

	rm -rf "${tmp_dir}"
	trap - RETURN
}

ensure_rust_toolchain() {
	local rustup_init target rustup_url tmp_dir

	export RUSTUP_HOME="${TOOLS_RUSTUP_HOME}"
	export CARGO_HOME="${TOOLS_CARGO_HOME}"

	if [[ ! -x "${CARGO_HOME}/bin/rustup" ]]; then
		target="$(rustup_target)"
		rustup_url="https://static.rust-lang.org/rustup/archive/${RUSTUP_VERSION}/${target}/rustup-init"
		tmp_dir="$(mktemp -d)"
		trap 'rm -rf "${tmp_dir}"' RETURN

		rustup_init="${tmp_dir}/rustup-init"
		curl -fsSL "${rustup_url}" -o "${rustup_init}"
		chmod +x "${rustup_init}"
		"${rustup_init}" -y --no-modify-path --profile minimal --default-toolchain "${RUST_TOOLCHAIN_VERSION}"

		rm -rf "${tmp_dir}"
		trap - RETURN
	fi

	"${CARGO_HOME}/bin/rustup" toolchain install "${RUST_TOOLCHAIN_VERSION}" --profile minimal >/dev/null
}

install_cargo_tool() {
	local url="$1"
	local git_ref="$2"
	local package_name="$3"

	export RUSTUP_HOME="${TOOLS_RUSTUP_HOME}"
	export CARGO_HOME="${TOOLS_CARGO_HOME}"
	export RUSTUP_TOOLCHAIN="${RUST_TOOLCHAIN_VERSION}"

	"${CARGO_HOME}/bin/cargo" install \
		--locked \
		--force \
		--root "${TOOLS_DIR}" \
		--git "${url}" \
		--tag "${git_ref}" \
		"${package_name}"
}

install_python_env() {
	"${TOOLS_PYTHON_DIR}/bin/python" -m pip --version >/dev/null 2>&1 || python3 -m venv "${TOOLS_PYTHON_DIR}"
	"${TOOLS_PYTHON_DIR}/bin/python" -m pip install --quiet --upgrade pip
	"${TOOLS_PYTHON_DIR}/bin/python" -m pip install --quiet \
		"mdformat==${PYTHON_MDFORMAT_VERSION}" \
		"mdformat-gfm==${PYTHON_MDFORMAT_GFM_VERSION}" \
		"mdformat-frontmatter==${PYTHON_MDFORMAT_FRONTMATTER_VERSION}"

	ln -snf ../python/bin/mdformat "${TOOLS_BIN_DIR}/mdformat"
}

install_node_env() {
	mkdir -p "${TOOLS_NODE_DIR}"
	if [[ ! -f "${TOOLS_NODE_DIR}/package.json" ]]; then
		cat >"${TOOLS_NODE_DIR}/package.json" <<'EOF'
{
  "name": "q15-tools",
  "private": true
}
EOF
	fi

	npm --prefix "${TOOLS_NODE_DIR}" install --no-save --loglevel=error "markdownlint-cli@${MARKDOWNLINT_CLI_VERSION}" >/dev/null

	cat >"${TOOLS_BIN_DIR}/markdownlint" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec node "${SCRIPT_DIR}/../node/node_modules/markdownlint-cli/markdownlint.js" "$@"
EOF
	chmod +x "${TOOLS_BIN_DIR}/markdownlint"
}

if ((check_only)); then
	assert_runtime_prereqs
	assert_go_series
	manifest_is_current || die "repo-local tools are out of date; run make project-setup"
	have_expected_tools || die "repo-local tools are incomplete; run make project-setup"
	exit 0
fi

assert_install_prereqs
assert_go_series

mkdir -p "${TOOLS_BIN_DIR}" "${TOOLS_DOWNLOAD_DIR}"

if manifest_is_current && have_expected_tools; then
	log "repo-local tools already match the pinned manifest"
	exit 0
fi

log "installing repo-local tools into ${TOOLS_DIR}"

install_go_tool "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
install_go_tool "honnef.co/go/tools/cmd/staticcheck@${STATICCHECK_VERSION}"
install_go_tool "github.com/mgechev/revive@${REVIVE_VERSION}"
install_go_tool "github.com/segmentio/golines@${GOLINES_VERSION}"
install_go_tool "github.com/rhysd/actionlint/cmd/actionlint@${ACTIONLINT_VERSION}"
install_go_tool "mvdan.cc/sh/v3/cmd/shfmt@${SHFMT_VERSION}"

install_shellcheck
install_jq
install_yq
ensure_rust_toolchain
install_cargo_tool "${ALEJANDRA_GIT_URL}" "${ALEJANDRA_GIT_REF}" "${ALEJANDRA_CARGO_PACKAGE}"
install_cargo_tool "${DEADNIX_GIT_URL}" "${DEADNIX_GIT_REF}" "${DEADNIX_CARGO_PACKAGE}"
install_cargo_tool "${STATIX_GIT_URL}" "${STATIX_GIT_REF}" "${STATIX_CARGO_PACKAGE}"
install_python_env
install_node_env

have_expected_tools || die "tool installation finished without producing the expected binaries"
write_manifest_stamp

log "repo-local toolchain is ready"
