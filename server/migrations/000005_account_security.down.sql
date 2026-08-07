-- Sprint 1 — Account security, recovery & production hardening (milestone 5).
-- Reverse of 000005_account_security.up.sql. Forward-only policy means this is
-- a rollback aid only; do not run against a shared database.

DROP TABLE IF EXISTS audit_logs;

DROP TABLE IF EXISTS login_history;

DROP TABLE IF EXISTS auth_tokens;
