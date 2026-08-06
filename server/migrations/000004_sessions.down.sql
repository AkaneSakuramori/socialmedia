-- Reverse of 000004_sessions.up.sql.
DROP INDEX IF EXISTS user_sessions_state_updated_idx;
ALTER TABLE users DROP COLUMN IF EXISTS token_version;
