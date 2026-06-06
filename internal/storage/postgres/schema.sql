-- uMailServer message-metadata + search schema (Faz 4 — the storage layer of
-- the bbolt -> PostgreSQL migration). This holds what internal/storage kept in
-- bbolt: per-mailbox state, per-message metadata, threads, and the search index.
--
-- Message BODIES are NOT here: they stay as Maildir files on disk/shared storage
-- regardless of backend (the design boundary). Only metadata and the search
-- index are relational.
--
-- The relational shape dissolves the single-writer landmines the report flagged:
--   - IMAP UID monotonicity -> mailboxes.uid_next with UPDATE ... RETURNING.
--   - RFC 7162 mod-sequence -> mailboxes.highest_modseq, bumped the same way.
--   - full-text search -> a generated tsvector column + GIN index (no separate
--     hand-maintained index bucket).
--
-- Every statement is idempotent so Migrate can run on every start.

-- Per-(user, mailbox) state. uid_next and highest_modseq are the monotonic
-- counters; a writer claims the next value with
--   UPDATE mailboxes SET uid_next = uid_next + 1 WHERE ... RETURNING uid_next - 1
-- which is atomic across concurrent nodes (no single-writer assumption).
CREATE TABLE IF NOT EXISTS mailboxes (
    user_email     TEXT     NOT NULL,
    name           TEXT     NOT NULL,
    uid_validity   BIGINT   NOT NULL DEFAULT 0,
    uid_next       BIGINT   NOT NULL DEFAULT 1,
    highest_modseq BIGINT   NOT NULL DEFAULT 0,
    subscribed     BOOLEAN  NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_email, name)
);

-- Per-message metadata. Flags, labels, and references are first-class arrays
-- (multi-valued but bounded per message), not child tables — they are always
-- read and written whole with the message row.
CREATE TABLE IF NOT EXISTS messages (
    user_email    TEXT     NOT NULL,
    mailbox       TEXT     NOT NULL,
    uid           BIGINT   NOT NULL,
    message_id    TEXT     NOT NULL DEFAULT '',
    flags         TEXT[]   NOT NULL DEFAULT '{}',
    labels        TEXT[]   NOT NULL DEFAULT '{}',
    internal_date TIMESTAMPTZ NOT NULL DEFAULT now(),
    size          BIGINT   NOT NULL DEFAULT 0,
    mod_seq       BIGINT   NOT NULL DEFAULT 0,
    subject       TEXT     NOT NULL DEFAULT '',
    date_hdr      TEXT     NOT NULL DEFAULT '',
    from_addr     TEXT     NOT NULL DEFAULT '',
    to_addr       TEXT     NOT NULL DEFAULT '',
    in_reply_to   TEXT     NOT NULL DEFAULT '',
    refs          TEXT[]   NOT NULL DEFAULT '{}',
    thread_id     TEXT     NOT NULL DEFAULT '',
    is_thread_root BOOLEAN NOT NULL DEFAULT FALSE,
    -- Full-text search vector, generated from the indexed headers so it is
    -- always consistent with the row and never separately maintained.
    search tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            coalesce(subject, '') || ' ' ||
            coalesce(from_addr, '') || ' ' ||
            coalesce(to_addr, ''))
    ) STORED,
    PRIMARY KEY (user_email, mailbox, uid),
    FOREIGN KEY (user_email, mailbox) REFERENCES mailboxes (user_email, name) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_search ON messages USING GIN (search);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages (user_email, thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_modseq ON messages (user_email, mailbox, mod_seq);

-- Conversation threads (JMAP/webmail grouping).
CREATE TABLE IF NOT EXISTS threads (
    user_email    TEXT     NOT NULL,
    thread_id     TEXT     NOT NULL,
    subject       TEXT     NOT NULL DEFAULT '',
    participants  TEXT[]   NOT NULL DEFAULT '{}',
    message_count INTEGER  NOT NULL DEFAULT 0,
    unread_count  INTEGER  NOT NULL DEFAULT 0,
    last_activity TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_email, thread_id)
);

-- IMAP ACLs (RFC 4314). rights is the ACLRights bitmask.
CREATE TABLE IF NOT EXISTS mailbox_acl (
    owner_email TEXT     NOT NULL,
    mailbox     TEXT     NOT NULL,
    grantee     TEXT     NOT NULL,
    rights      SMALLINT NOT NULL DEFAULT 0,
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_email, mailbox, grantee)
);
CREATE INDEX IF NOT EXISTS idx_mailbox_acl_grantee ON mailbox_acl (grantee);
