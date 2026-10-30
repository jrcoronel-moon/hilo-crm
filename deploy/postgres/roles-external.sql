-- Roles and schema for an external Postgres (Aurora, RDS or any managed
-- cluster), the equivalent of initdb/01-roles.sh, run once by hand as the
-- master user, connected to the `hilo` database:
--
--   psql "host=<writer endpoint> dbname=hilo user=<master> sslmode=verify-full sslrootcert=rds-ca.pem" \
--        -v migrator_pw='…' -v app_pw='…' -f deploy/postgres/roles-external.sql
--
-- hilo_migrator owns the schema (used only by the one-shot migrate container);
-- hilo_app has DML only, so Row-Level Security always applies to it.
CREATE ROLE hilo_migrator LOGIN PASSWORD :'migrator_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;
CREATE ROLE hilo_app      LOGIN PASSWORD :'app_pw'      NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS;

-- Managed clusters give the master user no superuser: it must be a member of
-- a role to own objects or set default privileges on its behalf.
GRANT hilo_migrator TO CURRENT_USER;

REVOKE ALL ON DATABASE hilo FROM PUBLIC;
GRANT CONNECT ON DATABASE hilo TO hilo_migrator, hilo_app;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;

CREATE SCHEMA hilo AUTHORIZATION hilo_migrator;
GRANT USAGE ON SCHEMA hilo TO hilo_app;
ALTER ROLE hilo_migrator SET search_path = hilo, public;
ALTER ROLE hilo_app      SET search_path = hilo, public;

ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO hilo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT USAGE, SELECT ON SEQUENCES TO hilo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE hilo_migrator IN SCHEMA hilo
    GRANT EXECUTE ON FUNCTIONS TO hilo_app;
