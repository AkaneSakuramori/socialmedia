-- Sprint 1 — Account security, recovery & production hardening (milestone 5).
-- Source of truth: DATABASE.md §4.1 (users), §8.5 (audit_logs),
-- SECURITY_SPEC.md §2 (AUTH-7/AUTH-9), §8 (PASS-4), §9 (OTP), §29 (REC-1/4/6).
-- Migrations are forward-only; do not edit after merge.

-- auth_tokens: single-use recovery/verification tokens (password reset,
-- email/phone change). Only the SHA-256 hash is persisted (the REFR-2 pattern);
-- Consume is a single UPDATE so a token is single-use and TTL-bounded atomically.
CREATE TABLE IF NOT EXISTS auth_tokens (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose    TEXT NOT NULL CHECK (purpose IN ('password_reset','email_change','phone_change')),
    token_hash TEXT NOT NULL,
    data       JSONB NOT NULL DEFAULT '{}'::jsonb,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS auth_tokens_token_hash_idx
    ON auth_tokens (token_hash);

CREATE INDEX IF NOT EXISTS auth_tokens_user_purpose_idx
    ON auth_tokens (user_id, purpose, created_at);

-- login_history: per-login security-review trail (login history screen,
-- SECURITY_SPEC.md MON-5; unknown-identifier failures carry a NULL user_id).
-- Writes are best-effort and append-only.
CREATE TABLE IF NOT EXISTS login_history (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT REFERENCES users(id) ON DELETE SET NULL,
    identifier TEXT NOT NULL,
    method     TEXT NOT NULL CHECK (method IN ('password','otp','passkey')),
    success    BOOLEAN NOT NULL,
    new_device BOOLEAN NOT NULL DEFAULT false,
    device_id  TEXT,
    ip_address INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS login_history_user_created_idx
    ON login_history (user_id, created_at DESC);

-- audit_logs — the immutable audit trail (DATABASE.md §8.5, SECURITY_SPEC.md
-- AUD-1/AUD-6). Append-only by convention; retention per compliance policy.
CREATE TABLE IF NOT EXISTS audit_logs (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action         TEXT NOT NULL,
    resource_type  TEXT NOT NULL DEFAULT 'user',
    resource_id    BIGINT,
    ip_address     INET,
    details        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_logs_created_action_idx
    ON audit_logs (created_at, action);

CREATE INDEX IF NOT EXISTS audit_logs_actor_created_idx
    ON audit_logs (actor_user_id, created_at);

CREATE INDEX IF NOT EXISTS audit_logs_resource_idx
    ON audit_logs (resource_type, resource_id);
