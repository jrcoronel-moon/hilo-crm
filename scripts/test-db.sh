#!/bin/sh
# Starts (or recreates with -r) the throw-away test Postgres and migrates it.
# Used by `make test`; prints the env vars the test suite needs.
set -eu
cd "$(dirname "$0")/.."
PORT="${HILO_TEST_PG_PORT:-54329}"
if [ "${1:-}" = "-r" ]; then
  docker compose -f deploy/compose.test.yml down -v >/dev/null 2>&1 || true
fi
docker compose -f deploy/compose.test.yml up -d --wait >/dev/null
MIGRATOR_DATABASE_URL="postgres://hilo_migrator:test-only-migrator@127.0.0.1:${PORT}/hilo?sslmode=disable" \
  NO_COLOR=1 go run ./cmd/migrate >/dev/null
