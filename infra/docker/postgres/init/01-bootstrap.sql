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

-- Migrations run as migrator, so any object they create is owned by migrator.
-- Give app full DML on everything migrator creates from now on, so the runtime
-- role can read/write (DATABASE.md). Applied retroactively to the live dev DB:
--   GRANT USAGE ON SCHEMA public TO app;
--   GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app;
--   GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app;
ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app;
ALTER DEFAULT PRIVILEGES FOR ROLE migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app;

-- Shared extensions. These require superuser, so they are installed here (at
-- first boot, running as POSTGRES_USER) rather than in migrations, which run
-- under the unprivileged migrator role.
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- gen_random_uuid, pgp encryption primitives
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- trigram indexes (future search, DATABASE.md)
