#!/bin/sh
# InChat platform bootstrap roles (DEVOPS.md §7).
# Runs once when the postgres container is first initialized.
# Distinct runtime (app) and migration (migrator) roles — never shared.
#
# The passwords come from the compose environment (infra/docker/.env,
# gitignored) so no secret is ever baked into this file or the image.
# This is a .sh — not a .sql — because the postgres image only env-expands
# shell init scripts under /docker-entrypoint-initdb.d.

set -e

if [ -z "$APP_DB_PASSWORD" ] || [ -z "$MIGRATOR_DB_PASSWORD" ]; then
  echo "error: APP_DB_PASSWORD and MIGRATOR_DB_PASSWORD must be set" >&2
  exit 1
fi

# The bootstrapping superuser (POSTGRES_USER) creates the roles; local trust is
# active only during initdb, which is exactly when this runs.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
	CREATE ROLE app LOGIN PASSWORD '${APP_DB_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
	CREATE ROLE migrator LOGIN PASSWORD '${MIGRATOR_DB_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE;

	-- The app owns the database and its public schema.
	ALTER DATABASE ${POSTGRES_DB} OWNER TO app;
	ALTER SCHEMA public OWNER TO app;

	-- The migrator role may create objects (migrations run under it).
	GRANT USAGE ON SCHEMA public TO migrator;
	GRANT CREATE ON SCHEMA public TO migrator;

	-- Migrations run as migrator, so any object they create is owned by
	-- migrator. Give app full DML on everything migrator creates from now on,
	-- so the runtime role can read/write (DATABASE.md).
	ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public
	  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app;
	ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public
	  GRANT USAGE, SELECT ON SEQUENCES TO app;
EOSQL

# Shared extensions. These require superuser, so they are installed here (at
# first boot, running as POSTGRES_USER) rather than in migrations, which run
# under the unprivileged migrator role.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto;' \
  -c 'CREATE EXTENSION IF NOT EXISTS pg_trgm;'
