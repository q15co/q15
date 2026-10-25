#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"
ensure_repo_root
require_command docker

container_name="q15-history-test-$(date +%s)-${RANDOM}"
postgres_image="${Q15_HISTORY_TEST_POSTGRES_IMAGE:-pgvector/pgvector:0.8.7-pg17}"
cleanup() { docker rm -f "${container_name}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run --detach --rm --name "${container_name}" \
	--label io.q15.test=history \
	--publish 127.0.0.1::5432 \
	--env POSTGRES_PASSWORD=q15-history-test \
	--env POSTGRES_DB=q15_history \
	"${postgres_image}" >/dev/null

ready=0
for ((attempt = 0; attempt < 60; attempt++)); do
	if docker exec "${container_name}" pg_isready --username postgres --dbname q15_history >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 1
done
if ((ready == 0)); then
	docker logs "${container_name}"
	die "test Postgres did not become ready"
fi
mapping="$(docker port "${container_name}" 5432/tcp)"
export Q15_TEST_POSTGRES_URL="postgresql://postgres:q15-history-test@127.0.0.1:${mapping##*:}/q15_history?sslmode=disable"
make test-history test-history-runtime
