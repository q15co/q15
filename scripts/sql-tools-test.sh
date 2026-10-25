#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root

fixture_dir="$(mktemp -d "${REPO_ROOT}/.sql-tools-test.XXXXXX")"
trap 'rm -rf "${fixture_dir}"' EXIT
fixture="${fixture_dir#"${REPO_ROOT}/"}/fixture.sql"

cat >"${fixture}" <<'SQL'
-- +goose Up
create extension if not exists vector;
create table sql_tool_fixture (id bigint primary key, embedding vector(768));
-- +goose StatementBegin
do $$ begin perform 1; end $$;
-- +goose StatementEnd
-- name: FindFixture :many
select id from sql_tool_fixture order by embedding <=> sqlc.arg('embedding')::vector(768);
-- +goose Down
drop table sql_tool_fixture;
SQL

if make --no-print-directory lint-changed FILES="${fixture}" >"${fixture_dir}/lint.log" 2>&1; then
	die "SQL lint accepted unformatted SQL"
fi
make --no-print-directory fmt FILES="${fixture}" >"${fixture_dir}/format.log" 2>&1 || {
	cat "${fixture_dir}/format.log"
	die "SQL format failed on supported Postgres syntax"
}
make --no-print-directory lint-changed FILES="${fixture}"
cp "${fixture}" "${fixture_dir}/formatted.sql"
make --no-print-directory fmt FILES="${fixture}" >"${fixture_dir}/format.log" 2>&1
cmp -s "${fixture}" "${fixture_dir}/formatted.sql" || die "SQL formatting is not idempotent"

# SQLFluff skips files over 20 KB by default; large query files must still fail.
for ((line = 0; line < 1000; line++)); do
	printf '%s\n' '-- Padding keeps this fixture above the default SQLFluff size limit.'
done >"${fixture}"
printf '%s\n' 'SELEC 1;' >>"${fixture}"
if make --no-print-directory lint-changed FILES="${fixture}" >"${fixture_dir}/lint.log" 2>&1; then
	die "SQL lint accepted malformed SQL in a large file"
fi
if ! grep -Eq 'PRS|unparsable|parsing' "${fixture_dir}/lint.log"; then
	cat "${fixture_dir}/lint.log"
	die "large SQL fixture failed without a parse error"
fi

printf 'SQL tooling tests passed\n'
