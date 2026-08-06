-- Sprint 1 — Refresh-token rotation (SECURITY_SPEC.md §6, API.md §4.4).
--
-- Extends user_sessions with:
--  - refresh_token_previous_hash: the last-rotated-out token hash, enabling
--    reuse detection (REFR-4/REFR-5): presenting a token that matches the
--    previous hash is a theft signal -> 410 + revoke all sessions.
--  - refresh_expires_at: the sliding refresh lifetime (REFR-6, 30-90 days),
--    extended on every successful rotation.
-- Migrations are forward-only; do not edit after merge.

ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS refresh_token_previous_hash TEXT;

ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS refresh_expires_at TIMESTAMPTZ;

-- Lookup by token hash is the refresh hot path (API.md §4.4: hash lookup +
-- rotation in one transaction).
CREATE INDEX IF NOT EXISTS user_sessions_refresh_token_hash_idx
    ON user_sessions (refresh_token_hash);

CREATE INDEX IF NOT EXISTS user_sessions_refresh_token_previous_hash_idx
    ON user_sessions (refresh_token_previous_hash);
