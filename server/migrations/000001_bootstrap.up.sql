-- Sprint 0 marker migration.
--
-- Shared extensions (pgcrypto, pg_trgm) require superuser, so they are
-- installed at container bootstrap by infra/docker/postgres/init/01-bootstrap.sql,
-- which runs as POSTGRES_USER. This migration exists to exercise the
-- migrate up/down pipeline before business tables arrive; it must stay a
-- harmless no-op until the first real schema in a later sprint.
SELECT 1;
