-- Reverse of 000002_auth.up.sql (Sprint 1). Order matters: children before parents.
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS user_credentials;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS set_updated_at();
