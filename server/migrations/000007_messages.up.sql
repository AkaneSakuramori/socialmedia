-- Sprint 2 — Core messaging (milestone 2): message domain & persistence.
-- Source of truth: DATABASE.md §5.3 (messages), §5.5 (message_edits),
-- §5.6 (message_reactions), §7.1 (change_log outbox).
-- Migrations are forward-only; do not edit after merge.

-- messages — the system of record for all message content (DATABASE.md §5.3).
-- Composite PK (conversation_id, sequence) clusters each conversation's rows;
-- every read is `WHERE conversation_id = ? AND sequence < ? ORDER BY sequence
-- DESC` (keyset, index range scan, no sort). Sequence assignment is atomic
-- (Redis INCR hot path, PG conversation_sequences fallback/max-merge); the PK
-- is the final guard against reuse.
CREATE SEQUENCE IF NOT EXISTS messages_global_seq;

CREATE TABLE IF NOT EXISTS messages (
    conversation_id    BIGINT NOT NULL REFERENCES conversations(id),
    sequence           BIGINT NOT NULL,
    id                 BIGINT NOT NULL,
    client_msg_id      TEXT,              -- client idempotency key (retry dedupe)
    sender_id          BIGINT REFERENCES users(id), -- NULL for system events
    type               TEXT NOT NULL CHECK (type IN ('text','media','system','reply','forwarded')),
    content            TEXT,              -- text body (empty for media-only)
    attachment_envelope JSONB,            -- media refs + captions (media milestone validates refs)
    mentions           BIGINT[],          -- validated conversation members (API.md §8.2)
    reply_to_id        BIGINT REFERENCES messages(id), -- self-referencing across conversations
    forwarded_from_id  BIGINT REFERENCES messages(id),
    edit_count         INT NOT NULL DEFAULT 0,
    edited_at          TIMESTAMPTZ,
    deleted_at         TIMESTAMPTZ,       -- delete-for-all tombstone
    deleted_by         BIGINT REFERENCES users(id),
    global_seq         BIGINT NOT NULL DEFAULT nextval('messages_global_seq'),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, sequence),
    UNIQUE (id),
    UNIQUE (global_seq),
    CHECK (sequence > 0),
    CHECK (type <> 'text' OR content IS NOT NULL),
    CHECK (deleted_at IS NULL OR deleted_by IS NOT NULL)
);

-- The exactly-once send guard. ENGINEERING.md §29.3 scopes idempotency per
-- (userID, key): two different senders may use the same client_msg_id value.
-- `ON CONFLICT (sender_id, client_msg_id) DO NOTHING RETURNING` collapses a
-- retried send; the original row is re-selected and returned (API.md §8.2).
-- (Refinement of DATABASE.md §5.3's global partial unique to the per-user
-- scope the idempotency contract defines.)
CREATE UNIQUE INDEX IF NOT EXISTS messages_client_msg_dedupe_idx
    ON messages (sender_id, client_msg_id) WHERE client_msg_id IS NOT NULL AND sender_id IS NOT NULL;

-- "messages by a user in a conversation" (search/moderation).
CREATE INDEX IF NOT EXISTS messages_sender_idx
    ON messages (conversation_id, sender_id, sequence);

-- Purge jobs scan tombstones.
CREATE INDEX IF NOT EXISTS messages_deleted_idx
    ON messages (deleted_at) WHERE deleted_at IS NOT NULL;

-- Delta-sync poll path (API.md §8.1 after_global_seq).
CREATE INDEX IF NOT EXISTS messages_conversation_global_seq_idx
    ON messages (conversation_id, global_seq);

-- Reply resolution: given (conversation_id, sequence) find the message id
-- (and vice versa for the rendered reply_to object).
CREATE INDEX IF NOT EXISTS messages_reply_lookup_idx
    ON messages (conversation_id, id);

-- message_edits — append-only edit history for the "edited" badge and audit
-- (DATABASE.md §5.5). Edits are never destructive; prior bodies are retained.
CREATE TABLE IF NOT EXISTS message_edits (
    id          BIGINT PRIMARY KEY,
    message_id  BIGINT NOT NULL REFERENCES messages(id),
    edited_by   BIGINT NOT NULL REFERENCES users(id),
    old_content TEXT NOT NULL,
    edited_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS message_edits_message_idx
    ON message_edits (message_id, edited_at DESC);

CREATE INDEX IF NOT EXISTS message_edits_edited_at_idx
    ON message_edits (edited_at);

-- message_reactions — one row per (message, user, emoji); counts are derived
-- by GROUP BY, never stored (DATABASE.md §5.6).
CREATE TABLE IF NOT EXISTS message_reactions (
    id         BIGINT PRIMARY KEY,
    message_id BIGINT NOT NULL REFERENCES messages(id),
    user_id    BIGINT NOT NULL REFERENCES users(id),
    emoji      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, user_id, emoji)
);

-- Reaction chip aggregates.
CREATE INDEX IF NOT EXISTS message_reactions_emoji_idx
    ON message_reactions (message_id, emoji);

-- The user's own reaction state.
CREATE INDEX IF NOT EXISTS message_reactions_user_idx
    ON message_reactions (message_id, user_id);
