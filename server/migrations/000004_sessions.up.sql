-- Sprint 1 — Device management & session administration (milestone 4).
-- Source of truth: DATABASE.md §4.1 (users), §4.4 (user_sessions),
-- SECURITY_SPEC.md SESS-6.

-- SESS-6: the global token version. Sign-out-everywhere bumps it so every
-- outstanding access token — which embeds this value as the `ver` claim
-- (JWT-5) — fails validation at the gateways.
ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version BIGINT NOT NULL DEFAULT 0;

-- Retention purge (DATABASE.md §4.4: revoked/expired rows archived after 90
-- days) scans on (state, updated_at).
CREATE INDEX IF NOT EXISTS user_sessions_state_updated_idx
    ON user_sessions (state, updated_at);
