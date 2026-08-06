-- Sprint 1 — Refresh-token rotation rollback (reverse order of 000003_refresh.up.sql).

DROP INDEX IF EXISTS user_sessions_refresh_token_previous_hash_idx;

DROP INDEX IF EXISTS user_sessions_refresh_token_hash_idx;

ALTER TABLE user_sessions DROP COLUMN IF EXISTS refresh_expires_at;

ALTER TABLE user_sessions DROP COLUMN IF EXISTS refresh_token_previous_hash;
