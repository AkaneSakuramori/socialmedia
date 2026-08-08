-- Sprint 2 milestone 2 rollback: messages, edits, reactions.
-- Forward-only in production; this file exists for local migrate down 1.

DROP TABLE IF EXISTS message_reactions;
DROP TABLE IF EXISTS message_edits;
DROP TABLE IF EXISTS messages;
DROP SEQUENCE IF EXISTS messages_global_seq;
