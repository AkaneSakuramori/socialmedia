-- Sprint 2 — Core messaging (milestone 1): conversation domain & management.
-- Source of truth: DATABASE.md §5.1 (conversations), §5.2 (conversation_members),
-- §5.4 (conversation_sequences), §7.1 (change_log transactional outbox / sync feed).
-- Migrations are forward-only; do not edit after merge.

-- conversations — the chat module's aggregate root (DATABASE.md §5.1). Per-user
-- state (roles, cursors, mute/pin/archive) lives in conversation_members; the
-- last_message_* columns are deliberately denormalized and updated in the same
-- transaction that inserts a message (§5.1), so the chat list is one indexed
-- query per user.
CREATE TABLE IF NOT EXISTS conversations (
    id                   BIGINT PRIMARY KEY,
    type                 TEXT NOT NULL CHECK (type IN ('direct','group')),
    title                TEXT,
    photo_media_id       BIGINT, -- FK -> media_objects.id lands with the media milestone
    description          TEXT,
    created_by           BIGINT NOT NULL REFERENCES users(id),
    last_message_at      TIMESTAMPTZ,
    last_message_seq     BIGINT,
    last_message_snippet TEXT,
    last_sender_id       BIGINT REFERENCES users(id),
    settings             JSONB,
    retention_days       INT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ,
    CHECK (type = 'direct' OR title IS NOT NULL),
    CHECK (last_message_at IS NULL OR last_message_seq IS NOT NULL),
    CHECK (retention_days IS NULL OR retention_days > 0)
);

-- Chat-list ordering key (COALESCE(last_message_at, created_at) DESC at read time).
CREATE INDEX IF NOT EXISTS conversations_last_message_at_idx
    ON conversations (last_message_at DESC);

-- conversation_members — membership, per-user read/delivery cursors, per-user
-- prefs (mute/pin/archive), and group roles (DATABASE.md §5.2). The cursors are
-- monotonic; unread is derived, never stored.
CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id    BIGINT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id            BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role               TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
    last_read_seq      BIGINT NOT NULL DEFAULT 0,
    last_delivered_seq BIGINT NOT NULL DEFAULT 0,
    last_read_at       TIMESTAMPTZ,
    muted_until        TIMESTAMPTZ,
    pinned_at          TIMESTAMPTZ,
    archived_at        TIMESTAMPTZ,
    joined_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at            TIMESTAMPTZ,
    invite_state       TEXT NOT NULL DEFAULT 'accepted' CHECK (invite_state IN ('accepted','invited','requested','none')),
    PRIMARY KEY (conversation_id, user_id),
    CHECK (last_read_seq >= 0),
    CHECK (last_delivered_seq >= last_read_seq OR last_read_seq = 0),
    CHECK (archived_at IS NULL OR pinned_at IS NULL)
);

-- "my conversations" + chat-list: (user_id, conversation_id) drives the list scan.
CREATE INDEX IF NOT EXISTS conversation_members_user_idx
    ON conversation_members (user_id, conversation_id);

-- Pinned section of the chat list, pin-time desc.
CREATE INDEX IF NOT EXISTS conversation_members_pinned_idx
    ON conversation_members (user_id, pinned_at DESC) WHERE pinned_at IS NOT NULL;

-- "who has read up to X" for group read receipts (milestone 3).
CREATE INDEX IF NOT EXISTS conversation_members_read_idx
    ON conversation_members (conversation_id, last_read_seq DESC);

-- conversation_sequences — durable per-conversation sequence counter. The Redis
-- counter is the hot path (milestone 3); this row is the recovery ground truth
-- and is idempotent via GREATEST max-merge (DATABASE.md §5.4).
CREATE TABLE IF NOT EXISTS conversation_sequences (
    conversation_id BIGINT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    last_sequence   BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- change_log — the transactional outbox / sync feed (DATABASE.md §7.1). Rows are
-- written in the same transaction as the domain write (outbox pattern,
-- ARCHITECTURE.md §37.4); consumers (sync engine, notification worker, search
-- indexer, realtime dispatcher) read it in global order. payload is a
-- self-contained event envelope so sync never re-queries domain tables.
CREATE SEQUENCE IF NOT EXISTS change_log_seq;

CREATE TABLE IF NOT EXISTS change_log (
    global_seq        BIGINT PRIMARY KEY DEFAULT nextval('change_log_seq'),
    event_type        TEXT NOT NULL CHECK (event_type IN (
        'message.created','message.edited','message.deleted','message.reaction',
        'receipt.read','receipt.delivered',
        'conversation.created','conversation.membership','conversation.settings',
        'user.updated','media.ready')),
    conversation_id   BIGINT REFERENCES conversations(id),
    entity_id         BIGINT,
    actor_user_id     BIGINT REFERENCES users(id),
    affected_user_ids BIGINT[],
    payload           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-entity event replay / backfill.
CREATE INDEX IF NOT EXISTS change_log_entity_idx
    ON change_log (entity_id, event_type);

-- Per-conversation sync reads (WHERE conversation_id = ? AND global_seq > ?).
CREATE INDEX IF NOT EXISTS change_log_conversation_idx
    ON change_log (conversation_id, global_seq);

-- "what changed for this user" fan-out scan.
CREATE INDEX IF NOT EXISTS change_log_affected_gin
    ON change_log USING GIN (affected_user_ids);
