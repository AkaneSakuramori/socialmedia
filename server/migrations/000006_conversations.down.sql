-- Sprint 2 milestone 1 rollback: conversations, members, sequences, outbox.
-- Forward-only in production; this file exists for local migrate down 1.

DROP TABLE IF EXISTS change_log;
DROP SEQUENCE IF EXISTS change_log_seq;
DROP TABLE IF EXISTS conversation_sequences;
DROP TABLE IF EXISTS conversation_members;
DROP TABLE IF EXISTS conversations;
