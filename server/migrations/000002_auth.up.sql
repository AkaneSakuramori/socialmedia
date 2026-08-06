-- Sprint 1 — Authentication & Identity schema.
-- Source of truth: DATABASE.md §4.1 (users), §4.3 (user_credentials),
-- §4.4 (user_sessions). Migrations are forward-only; do not edit after merge.

-- Shared updated_at trigger (used by all tables with standard audit columns).
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- users — the identity root aggregate (DATABASE.md §4.1).
CREATE TABLE IF NOT EXISTS users (
    id                 BIGINT PRIMARY KEY,                 -- snowflake from idgen
    username           TEXT,                               -- lowercase, immutable
    display_name       TEXT NOT NULL,
    avatar_media_id    BIGINT,                             -- FK -> media_objects.id added when the media module lands
    bio                TEXT,
    phone_number       TEXT UNIQUE,                        -- normalized E.164
    email              TEXT UNIQUE,                        -- normalized lowercase
    account_state      TEXT NOT NULL DEFAULT 'active' CHECK (account_state IN ('active','suspended','deleted')),
    primary_identifier TEXT NOT NULL DEFAULT 'phone' CHECK (primary_identifier IN ('phone','email','username')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ
);

-- One active username slot per account; usernames are reusable after deletion.
CREATE UNIQUE INDEX IF NOT EXISTS users_username_unique_active
    ON users (username) WHERE deleted_at IS NULL;

-- "Find contact / search people" prefix search (DATABASE.md §4.1 FTS).
CREATE INDEX IF NOT EXISTS users_search_trgm
    ON users USING gin (display_name gin_trgm_ops, username gin_trgm_ops);

CREATE TRIGGER users_updated_at
    BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- user_credentials — verifiable auth material, isolated from profile data
-- (DATABASE.md §4.3). credential_data: password -> {"hash": "<argon2id phc>"}.
CREATE TABLE IF NOT EXISTS user_credentials (
    id              BIGINT PRIMARY KEY,                    -- snowflake from idgen
    user_id         BIGINT NOT NULL REFERENCES users(id),
    method          TEXT NOT NULL CHECK (method IN ('password','passkey','oauth')),
    provider        TEXT,                                  -- for oauth (google/apple)
    credential_data JSONB NOT NULL,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, method, provider)
);

CREATE INDEX IF NOT EXISTS user_credentials_user_id_idx
    ON user_credentials (user_id);

CREATE TRIGGER user_credentials_updated_at
    BEFORE UPDATE ON user_credentials FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- user_sessions — the session registry (DATABASE.md §4.4). One active session
-- slot per (user_id, device_id).
--
-- refresh_token_hash is a Sprint 1 addition required by SECURITY_SPEC.md REFR-2:
-- the opaque refresh token is stored only as its SHA-256 hash "alongside the
-- session", which the documented column set did not provide. Rotation and reuse
-- detection (REFR-4/REFR-5) compare against this hash.
CREATE TABLE IF NOT EXISTS user_sessions (
    id                  BIGINT PRIMARY KEY,                -- snowflake from idgen
    user_id             BIGINT NOT NULL REFERENCES users(id),
    device_id           TEXT NOT NULL,                     -- client-generated, validated
    device_name         TEXT,
    platform            TEXT,                              -- ios | android | web
    app_version         TEXT,
    push_token          TEXT,
    refresh_token_family BIGINT NOT NULL DEFAULT 0,        -- increments on rotation
    refresh_token_hash  TEXT,                              -- SHA-256 of the opaque refresh token
    ip_address          INET,
    user_agent          TEXT,
    last_active_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    state               TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked','expired','suspended')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, device_id),
    CONSTRAINT user_sessions_active_has_activity
        CHECK (state <> 'active' OR last_active_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS user_sessions_user_state_idx
    ON user_sessions (user_id, state);

CREATE INDEX IF NOT EXISTS user_sessions_push_token_idx
    ON user_sessions (push_token);

CREATE TRIGGER user_sessions_updated_at
    BEFORE UPDATE ON user_sessions FOR EACH ROW EXECUTE FUNCTION set_updated_at();
