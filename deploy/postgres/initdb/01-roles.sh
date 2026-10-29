#!/bin/sh
# Runs once, as the Postgres superuser, when the data volume is first created.
#
# Creates two least-privilege roles and the `hilo` schema:
#   hilo_migrator  owns the schema and its objects; used only by the one-shot
#                  `migrate` container.
#   hilo_app       DML only (no DDL, no ownership, so Row-Level Security always
#                  applies to it); used by the `web` and `mcp` containers.
# The superuser itself is never used by the application.
set -eu

: "${HILO_MIGRATOR_PASSWORD:?HILO_MIGRATOR_PASSWORD is required}"
: "${HILO_APP_PASSWORD:?HILO_APP_PASSWORD is required}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v migrator_pw="$HILO_MIGRATOR_PASSWORD" -v app_pw="$HILO_APP_PASSWORD" \
     -v db="$POSTGRES_DB" <<'SQL'
CREATE ROLE hilo_migrator LOGIN PASSWORD :'migrator_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE ROLE hilo_app      LOGIN PASSWORD :'app_pw'      NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;

REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO hilo_migrator, hilo_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- Extensions need elevated rights; install them here, once.
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;

CREATE SCHEMA hilo AUTHORIZATION hilo_migrator;
GRANT USAGE ON SCHEMA hilo TO hilo_app;
ALTER ROLE hilo_migrator SET search_path = hilo, public;
ALTER ROLE hilo_app      SET search_path = hilo, public;

-- Whatever the migrator creates later is usable (DML only) by the app.
ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO hilo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT USAGE, SELECT ON SEQUENCES TO hilo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT EXECUTE ON FUNCTIONS TO hilo_app;
SQL
