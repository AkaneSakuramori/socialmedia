-- InChat platform bootstrap roles (DEVOPS.md §7).
-- Runs once when the postgres container is first initialized.
-- Distinct runtime (app) and migration (migrator) roles — never shared.

CREATE ROLE app LOGIN PASSWORD 'app_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE migrator LOGIN PASSWORD 'migrator_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;

-- The app owns the database and its public schema.
ALTER DATABASE inchat OWNER TO app;
ALTER SCHEMA public OWNER TO app;

-- The migrator role may create objects (migrations run under it).
GRANT USAGE ON SCHEMA public TO migrator;
GRANT CREATE ON SCHEMA public TO migrator;

-- Shared extensions. These require superuser, so they are installed here (at
-- first boot, running as POSTGRES_USER) rather than in migrations, which run
-- under the unprivileged migrator role.
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- gen_random_uuid, pgp encryption primitives
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- trigram indexes (future search, DATABASE.md)
