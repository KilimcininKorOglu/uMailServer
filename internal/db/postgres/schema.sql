-- uMailServer relational schema (Faz 4 — bbolt -> PostgreSQL migration).
--
-- Scope: the unambiguous "net" surfaces whose shape is already fixed by the
-- typed db.DB methods — tenants, domains, accounts, aliases, mail groups, the
-- outbound queue (the FOR UPDATE SKIP LOCKED target), and the auth-side token
-- blacklist / client sessions. These map 1:1 to the structs in internal/db.
--
-- The typed-KV replacement tables ratified for the generic-KV buckets
-- (user_ui_prefs, user_signatures, user_vacation, ews_user_config) are NOT in
-- this file yet: per the sequencing decision they land together with their own
-- read/write implementation, not ahead of it. The schema here is fully typed
-- (real columns, FKs, child tables for open-ended sets) — never the rejected
-- universal (bucket, key, jsonb) KV shape.
--
-- Every statement is idempotent (IF NOT EXISTS) so Migrate can run on every
-- start without a separate version ledger for the initial schema.

-- Tenants -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tenants (
    id         TEXT PRIMARY KEY,
    name       TEXT        NOT NULL DEFAULT '',
    is_active  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-tenant branding/feature flags. A child table keeps the settings map
-- relational instead of an opaque blob on the tenant row.
CREATE TABLE IF NOT EXISTS tenant_settings (
    tenant_id TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    key       TEXT NOT NULL,
    value     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, key)
);

-- Domains -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS domains (
    name                   TEXT PRIMARY KEY,
    tenant_id              TEXT        REFERENCES tenants (id) ON DELETE RESTRICT,
    max_accounts           INTEGER     NOT NULL DEFAULT 0,
    max_mailbox_size       BIGINT      NOT NULL DEFAULT 0,
    quota_warn             BIGINT      NOT NULL DEFAULT 0,
    quota_prohibit_send    BIGINT      NOT NULL DEFAULT 0,
    dkim_selector          TEXT        NOT NULL DEFAULT '',
    dkim_public_key        TEXT        NOT NULL DEFAULT '',
    dkim_private_key       TEXT        NOT NULL DEFAULT '',
    catch_all_target       TEXT        NOT NULL DEFAULT '',
    company_name           TEXT        NOT NULL DEFAULT '',
    from_template_internal TEXT        NOT NULL DEFAULT '',
    from_template_external TEXT        NOT NULL DEFAULT '',
    is_active              BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_domains_tenant ON domains (tenant_id);
ALTER TABLE domains ADD COLUMN IF NOT EXISTS quota_warn          BIGINT NOT NULL DEFAULT 0;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS quota_prohibit_send BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS domain_settings (
    domain TEXT NOT NULL REFERENCES domains (name) ON DELETE CASCADE,
    key    TEXT NOT NULL,
    value  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (domain, key)
);

-- Accounts ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS accounts (
    email                TEXT PRIMARY KEY,
    local_part           TEXT        NOT NULL,
    domain               TEXT        NOT NULL REFERENCES domains (name) ON DELETE CASCADE,
    password_hash        TEXT        NOT NULL DEFAULT '',
    apop_hash            TEXT        NOT NULL DEFAULT '',
    nt_hash              TEXT        NOT NULL DEFAULT '',
    totp_secret          TEXT        NOT NULL DEFAULT '',
    totp_enabled         BOOLEAN     NOT NULL DEFAULT FALSE,
    totp_last_used_step  BIGINT      NOT NULL DEFAULT 0,
    quota_used           BIGINT      NOT NULL DEFAULT 0,
    quota_limit          BIGINT      NOT NULL DEFAULT 0,
    quota_warn           BIGINT      NOT NULL DEFAULT 0,
    quota_prohibit_send  BIGINT      NOT NULL DEFAULT 0,
    quota_warn_sent      BOOLEAN     NOT NULL DEFAULT FALSE,
    max_message_size     BIGINT      NOT NULL DEFAULT 0,
    forward_to           TEXT        NOT NULL DEFAULT '',
    forward_keep_copy    BOOLEAN     NOT NULL DEFAULT FALSE,
    sieve_script         TEXT        NOT NULL DEFAULT '',
    vacation_settings    TEXT        NOT NULL DEFAULT '',
    must_change_password BOOLEAN     NOT NULL DEFAULT FALSE,
    is_admin             BOOLEAN     NOT NULL DEFAULT FALSE,
    is_tenant_admin      BOOLEAN     NOT NULL DEFAULT FALSE,
    is_active            BOOLEAN     NOT NULL DEFAULT TRUE,
    compatibility_tier   SMALLINT    NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at        TIMESTAMPTZ,
    avatar               BYTEA,
    avatar_type          TEXT        NOT NULL DEFAULT '',
    display_name         TEXT        NOT NULL DEFAULT '',
    title                TEXT        NOT NULL DEFAULT '',
    department           TEXT        NOT NULL DEFAULT '',
    phone                TEXT        NOT NULL DEFAULT '',
    timezone             TEXT        NOT NULL DEFAULT '',
    locale               TEXT        NOT NULL DEFAULT '',
    theme                TEXT        NOT NULL DEFAULT '',
    onboarded            BOOLEAN     NOT NULL DEFAULT FALSE,
    send_policy          TEXT        NOT NULL DEFAULT '',
    receive_policy       TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_accounts_domain ON accounts (domain);
-- Additive column migrations for accounts (idempotent; CREATE TABLE IF NOT EXISTS
-- above is a no-op on an already-created table, so new columns are added here).
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS timezone       TEXT    NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS locale         TEXT    NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS theme          TEXT    NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS onboarded      BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS send_policy    TEXT    NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS receive_policy TEXT    NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_warn          BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_prohibit_send BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_warn_sent     BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS nt_hash             TEXT    NOT NULL DEFAULT '';

-- Aliases. The alias column holds the local part (e.g. "info"); identity is
-- (domain, local part). Matching the bbolt store, the key is case-insensitive
-- (it lower-cased the local part), so uniqueness and lookups use lower(alias).
CREATE TABLE IF NOT EXISTS aliases (
    domain     TEXT        NOT NULL REFERENCES domains (name) ON DELETE CASCADE,
    alias      TEXT        NOT NULL,
    target     TEXT        NOT NULL,
    is_active  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_aliases_key ON aliases (domain, lower(alias));

-- Mail groups ---------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mail_groups (
    email                 TEXT PRIMARY KEY,
    local_part            TEXT        NOT NULL,
    domain                TEXT        NOT NULL REFERENCES domains (name) ON DELETE CASCADE,
    description           TEXT        NOT NULL DEFAULT '',
    is_active             BOOLEAN     NOT NULL DEFAULT TRUE,
    dynamic               BOOLEAN     NOT NULL DEFAULT FALSE,
    dynamic_domain        TEXT        NOT NULL DEFAULT '',
    -- nullable on purpose: NULL = any, TRUE = admins only, FALSE = non-admins only.
    dynamic_admin_only    BOOLEAN,
    dynamic_local_pattern TEXT        NOT NULL DEFAULT '',
    sender_policy         TEXT        NOT NULL DEFAULT 'internal',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_mail_groups_domain ON mail_groups (domain);

-- Static membership for non-dynamic groups, ordered to preserve input order.
CREATE TABLE IF NOT EXISTS mail_group_members (
    group_email TEXT    NOT NULL REFERENCES mail_groups (email) ON DELETE CASCADE,
    ord         INTEGER NOT NULL,
    member      TEXT    NOT NULL,
    PRIMARY KEY (group_email, ord)
);

-- Outbound queue (FOR UPDATE SKIP LOCKED claim target) ----------------------
CREATE TABLE IF NOT EXISTS mail_queue (
    id           TEXT PRIMARY KEY,
    -- "from" is a reserved word; the Go field From maps to this column.
    sender       TEXT        NOT NULL DEFAULT '',
    message_path TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_retry   TIMESTAMPTZ NOT NULL DEFAULT now(),
    retry_count  INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'pending',
    priority     SMALLINT    NOT NULL DEFAULT 1,
    notify       INTEGER     NOT NULL DEFAULT 0,
    ret          INTEGER     NOT NULL DEFAULT 0
);
-- A worker claims due entries with
--   SELECT ... WHERE status='pending' AND next_retry<=now()
--   ORDER BY priority DESC, next_retry FOR UPDATE SKIP LOCKED
-- so this index serves the claim scan directly.
CREATE INDEX IF NOT EXISTS idx_mail_queue_due
    ON mail_queue (status, next_retry, priority DESC);

CREATE TABLE IF NOT EXISTS mail_queue_recipients (
    queue_id  TEXT    NOT NULL REFERENCES mail_queue (id) ON DELETE CASCADE,
    ord       INTEGER NOT NULL,
    recipient TEXT    NOT NULL,
    PRIMARY KEY (queue_id, ord)
);

-- Scheduled ("send later") messages -----------------------------------------
-- The canonical record a leader-gated release loop reads; the "Scheduled" system
-- folder is a visibility projection (folder_uid links them). A due pending row is
-- claimed (status->sending, claimed_at) before release; stale 'sending' rows are
-- reset to 'pending' for crash recovery.
CREATE TABLE IF NOT EXISTS scheduled_messages (
    id           TEXT PRIMARY KEY,
    owner        TEXT        NOT NULL DEFAULT '',
    -- "from" is a reserved word; the Go field From maps to this column.
    sender       TEXT        NOT NULL DEFAULT '',
    message_path TEXT        NOT NULL DEFAULT '',
    send_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at   TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00',
    status       TEXT        NOT NULL DEFAULT 'pending',
    source       TEXT        NOT NULL DEFAULT '',
    file_sent    BOOLEAN     NOT NULL DEFAULT false,
    folder_uid   BIGINT      NOT NULL DEFAULT 0,
    blob_key     TEXT        NOT NULL DEFAULT '',
    retry_count  INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_scheduled_due ON scheduled_messages (status, send_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_owner ON scheduled_messages (owner);

CREATE TABLE IF NOT EXISTS scheduled_message_recipients (
    scheduled_id TEXT    NOT NULL REFERENCES scheduled_messages (id) ON DELETE CASCADE,
    ord          INTEGER NOT NULL,
    recipient    TEXT    NOT NULL,
    PRIMARY KEY (scheduled_id, ord)
);

-- Recoverable Items (soft-delete dumpster) ----------------------------------
-- A permanently deleted message is held here for a retention window so it can be
-- restored; the leader-gated cleaner purges rows once deleted_at ages out. The
-- "Recoverable Items" system folder is the visibility projection (folder_uid +
-- blob_key link the two).
CREATE TABLE IF NOT EXISTS recoverable_items (
    id              TEXT PRIMARY KEY,
    owner           TEXT        NOT NULL DEFAULT '',
    original_folder TEXT        NOT NULL DEFAULT '',
    blob_key        TEXT        NOT NULL DEFAULT '',
    folder_uid      BIGINT      NOT NULL DEFAULT 0,
    deleted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    size            BIGINT      NOT NULL DEFAULT 0,
    subject         TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_recoverable_expiry ON recoverable_items (deleted_at);
CREATE INDEX IF NOT EXISTS idx_recoverable_owner ON recoverable_items (owner);

-- Auth: token blacklist + portal sessions -----------------------------------
-- The revoked-token blacklist is canonical DB state today (bbolt); on Postgres
-- it becomes cluster-shared automatically (the HA reason it is not duplicated
-- into Redis).
CREATE TABLE IF NOT EXISTS revoked_tokens (
    token_hash TEXT PRIMARY KEY,
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_revoked_tokens_expires ON revoked_tokens (expires_at);

CREATE TABLE IF NOT EXISTS client_sessions (
    id          TEXT PRIMARY KEY,
    email       TEXT        NOT NULL,
    token_hash  TEXT        NOT NULL,
    device_type TEXT        NOT NULL DEFAULT 'unknown',
    client_ip   TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked     BOOLEAN     NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_client_sessions_email ON client_sessions (email);

-- Exchange ActiveSync device partnerships: one row per (email, device_id),
-- holding the provisioning policy key the device must echo, the negotiated
-- protocol version, and an admin-requested remote-wipe flag.
CREATE TABLE IF NOT EXISTS activesync_devices (
    email            TEXT        NOT NULL,
    device_id        TEXT        NOT NULL,
    device_type      TEXT        NOT NULL DEFAULT '',
    user_agent       TEXT        NOT NULL DEFAULT '',
    policy_key       TEXT        NOT NULL DEFAULT '',
    protocol_version TEXT        NOT NULL DEFAULT '',
    wipe_requested   BOOLEAN     NOT NULL DEFAULT FALSE,
    first_sync       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sync        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (email, device_id)
);
CREATE INDEX IF NOT EXISTS idx_activesync_devices_email ON activesync_devices (email);
-- Device-identity attributes reported via the Settings DeviceInformation command
-- (MS-ASCMD). Informational metadata only; access control never depends on them.
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS model           TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS imei            TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS friendly_name   TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS os              TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS os_language     TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS phone_number    TEXT NOT NULL DEFAULT '';
ALTER TABLE activesync_devices ADD COLUMN IF NOT EXISTS mobile_operator TEXT NOT NULL DEFAULT '';

-- Typed replacements for the generic-KV preference/vacation buckets ----------
-- These hold what the bbolt store kept as opaque JSON under BucketPreferences /
-- BucketVacation, now as real columns (ratified: fully typed, with an opaque
-- exception only for Outlook's protocol-opaque UserConfiguration).

-- Webmail UI toggles (map[string]bool under the bare user key).
CREATE TABLE IF NOT EXISTS user_ui_prefs (
    user_email TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_email, key)
);

-- Outgoing-mail signatures (multi-row).
-- Old single-row layout (user_email PK, signature TEXT) is migrated column-by-column
-- so existing data is preserved and the old GetSignature/PutSignature keep working.
ALTER TABLE user_signatures ADD COLUMN IF NOT EXISTS sig_name TEXT NOT NULL DEFAULT '';
ALTER TABLE user_signatures ADD COLUMN IF NOT EXISTS sig_body TEXT NOT NULL DEFAULT '';
ALTER TABLE user_signatures ADD COLUMN IF NOT EXISTS sig_html BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE user_signatures ADD COLUMN IF NOT EXISTS sig_ord INTEGER NOT NULL DEFAULT 0;

-- Migrate legacy single-row data into the new multi-row layout.
-- The old "signature" column holds plain-text body; sig_name='default', sig_ord=0.
UPDATE user_signatures
   SET sig_body  = COALESCE(NULLIF(sig_body, ''), signature),
       sig_name  = CASE WHEN sig_name = '' THEN 'default' ELSE sig_name END,
       sig_ord   = CASE WHEN sig_ord = 0 THEN 0 ELSE sig_ord END
 WHERE sig_body = '' AND signature <> '';

-- Only one 'default' row per user is needed; delete extras.
DELETE FROM user_signatures a USING user_signatures b
 WHERE a.user_email = b.user_email
   AND a.sig_name = 'default'
   AND a.sig_ord = 0
   AND a.ctid > b.ctid;

-- Drop the legacy column; the new columns are the canonical store.
ALTER TABLE user_signatures DROP COLUMN IF EXISTS signature;

-- Composite PK replaces the old single-column PK.
ALTER TABLE user_signatures DROP CONSTRAINT IF EXISTS user_signatures_pkey;
ALTER TABLE user_signatures ADD PRIMARY KEY (user_email, sig_name);

-- Webmail message categories (ordered name + color per user).
CREATE TABLE IF NOT EXISTS user_categories (
    user_email TEXT    NOT NULL,
    ord        INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    color      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (user_email, ord)
);

-- Message templates / snippets (per-user, named).
CREATE TABLE IF NOT EXISTS user_templates (
    user_email    TEXT    NOT NULL,
    tmpl_name     TEXT    NOT NULL,
    tmpl_subject  TEXT    NOT NULL DEFAULT '',
    tmpl_body     TEXT    NOT NULL DEFAULT '',
    tmpl_html     BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_email, tmpl_name)
);

-- Vacation / auto-reply config (the legacy fallback store; the canonical OOF
-- lives in the semantic core). send_interval is stored as nanoseconds to match
-- the Go time.Duration round-trip.
CREATE TABLE IF NOT EXISTS user_vacation (
    user_email    TEXT PRIMARY KEY,
    enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
    start_date    TIMESTAMPTZ,
    end_date      TIMESTAMPTZ,
    subject       TEXT        NOT NULL DEFAULT '',
    message       TEXT        NOT NULL DEFAULT '',
    html_message  TEXT        NOT NULL DEFAULT '',
    send_interval BIGINT      NOT NULL DEFAULT 0,
    ignore_lists  BOOLEAN     NOT NULL DEFAULT FALSE,
    ignore_bulk   BOOLEAN     NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS user_vacation_excludes (
    user_email TEXT    NOT NULL REFERENCES user_vacation (user_email) ON DELETE CASCADE,
    ord        INTEGER NOT NULL,
    address    TEXT    NOT NULL,
    PRIMARY KEY (user_email, ord)
);

-- Outlook EWS UserConfiguration: protocol-opaque roaming config. The values are
-- opaque to the server (a dictionary blob, an XML blob, a base64 binary blob),
-- so this is the deliberate typed-but-blob exception — keyed by (owner, name),
-- never the universal (bucket, key, jsonb) shape.
CREATE TABLE IF NOT EXISTS ews_user_config (
    owner       TEXT NOT NULL,
    name        TEXT NOT NULL,
    dictionary  TEXT NOT NULL DEFAULT '',
    xml_data    TEXT NOT NULL DEFAULT '',
    binary_data TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (owner, name)
);

-- Message metadata + search (the internal/storage layer) -------------------
-- This is the storage half of the migration: per-mailbox state, per-message
-- metadata, threads, and the search index. Message BODIES stay as Maildir files
-- on disk regardless of backend; only metadata and the search index are here.
-- It lives in this one schema file (and applies through one connection) because
-- it is the same PostgreSQL database as everything above.
--
-- The relational shape dissolves the single-writer landmines: IMAP UID
-- monotonicity -> mailboxes.uid_next with UPDATE ... RETURNING; RFC 7162
-- mod-sequence -> mailboxes.highest_modseq; full-text search -> a generated
-- tsvector column + GIN index (no separately maintained index bucket).

CREATE TABLE IF NOT EXISTS mailboxes (
    user_email     TEXT     NOT NULL,
    name           TEXT     NOT NULL,
    uid_validity   BIGINT   NOT NULL DEFAULT 0,
    uid_next       BIGINT   NOT NULL DEFAULT 1,
    highest_modseq BIGINT   NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_email, name)
);

-- IMAP subscriptions are independent of mailbox existence (RFC 3501: a client
-- may stay subscribed to a name that has no mailbox), so they are their own
-- table, NOT a column on mailboxes — matching the separate bbolt subscription
-- bucket and keeping one canonical source for the subscribed set.
CREATE TABLE IF NOT EXISTS mailbox_subscriptions (
    user_email TEXT NOT NULL,
    mailbox    TEXT NOT NULL,
    PRIMARY KEY (user_email, mailbox)
);

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
    search tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            coalesce(subject, '') || ' ' ||
            coalesce(from_addr, '') || ' ' ||
            coalesce(to_addr, ''))
    ) STORED,
    PRIMARY KEY (user_email, mailbox, uid),
    -- ON UPDATE CASCADE so RenameMailbox is a single UPDATE of mailboxes.name
    -- that carries the messages along (preserving their UIDs).
    FOREIGN KEY (user_email, mailbox) REFERENCES mailboxes (user_email, name)
        ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_messages_search ON messages USING GIN (search);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages (user_email, thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_modseq ON messages (user_email, mailbox, mod_seq);

-- RFC 7162 QRESYNC expunge tombstones: each expunged UID and the mailbox
-- mod-sequence at which it vanished, so a later QRESYNC SELECT can replay
-- VANISHED (EARLIER). The FK mirrors messages so tombstones drop on mailbox
-- delete and follow a rename (preserving their UIDs).
CREATE TABLE IF NOT EXISTS expunged_messages (
    user_email TEXT   NOT NULL,
    mailbox    TEXT   NOT NULL,
    uid        BIGINT NOT NULL,
    mod_seq    BIGINT NOT NULL,
    PRIMARY KEY (user_email, mailbox, uid),
    FOREIGN KEY (user_email, mailbox) REFERENCES mailboxes (user_email, name)
        ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_expunged_modseq ON expunged_messages (user_email, mailbox, mod_seq);

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
    granted_by  TEXT     NOT NULL DEFAULT '',
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_email, mailbox, grantee)
);
CREATE INDEX IF NOT EXISTS idx_mailbox_acl_grantee ON mailbox_acl (grantee);

-- Per-user change journal (JMAP incremental sync). seq is a global BIGSERIAL;
-- since change-state tokens are opaque to clients and each user's entries get
-- strictly increasing seq values, GetChangesSince(user, type, sinceSeq) is
-- monotonic per user without a per-user counter.
CREATE TABLE IF NOT EXISTS changes (
    seq        BIGSERIAL PRIMARY KEY,
    user_email TEXT NOT NULL,
    type       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    id         TEXT NOT NULL DEFAULT '',
    mailbox    TEXT NOT NULL DEFAULT '',
    at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_changes_user_type_seq ON changes (user_email, type, seq);

-- Bayesian spam token counts. class is the bucket name ('spam_tokens' /
-- 'ham_tokens'); the bbolt store keyed the same data by bucket + token.
CREATE TABLE IF NOT EXISTS spam_tokens (
    class TEXT   NOT NULL,
    token TEXT   NOT NULL,
    count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (class, token)
);

-- Singleton row holding the corpus totals the classifier reads as a fast path
-- (mirrors the bbolt "spam_stats" bucket's total_ham / total_spam keys).
CREATE TABLE IF NOT EXISTS spam_stats (
    id         SMALLINT PRIMARY KEY DEFAULT 1,
    total_ham  BIGINT NOT NULL DEFAULT 0,
    total_spam BIGINT NOT NULL DEFAULT 0
);

-- Per-user daily-sent quota counters, persisted so they survive a restart
-- (mirrors the bbolt "ratelimit_users" bucket's "<user>:sent_today" keys).
CREATE TABLE IF NOT EXISTS ratelimit_quota (
    user_email TEXT   NOT NULL PRIMARY KEY,
    sent_today BIGINT NOT NULL DEFAULT 0
);

-- Scheduled backup jobs (admin backup API). Typed columns mirror storage.BackupJob;
-- the bbolt store kept the same record as a JSON blob in "backup_jobs".
CREATE TABLE IF NOT EXISTS backup_jobs (
    id            TEXT    NOT NULL PRIMARY KEY,
    name          TEXT    NOT NULL DEFAULT '',
    type          TEXT    NOT NULL DEFAULT '',
    target        TEXT    NOT NULL DEFAULT '',
    schedule      TEXT    NOT NULL DEFAULT '',
    retention     INTEGER NOT NULL DEFAULT 0,
    enabled       BOOLEAN NOT NULL DEFAULT false,
    last_run      TIMESTAMPTZ,
    next_run      TIMESTAMPTZ,
    destinations  TEXT    NOT NULL DEFAULT '',
    options       TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT '',
    last_error    TEXT    NOT NULL DEFAULT ''
);

-- Stored backup manifests (admin backup API). Typed columns mirror
-- storage.BackupManifest; the bbolt store kept this as a JSON blob in
-- "backup_manifests".
CREATE TABLE IF NOT EXISTS backup_manifests (
    id              TEXT   NOT NULL PRIMARY KEY,
    filename        TEXT   NOT NULL DEFAULT '',
    size            BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    type            TEXT   NOT NULL DEFAULT '',
    target          TEXT   NOT NULL DEFAULT '',
    checksum        TEXT   NOT NULL DEFAULT '',
    encrypted       BOOLEAN NOT NULL DEFAULT false,
    retention_until TIMESTAMPTZ,
    destination     TEXT   NOT NULL DEFAULT '',
    path            TEXT   NOT NULL DEFAULT '',
    metadata        TEXT   NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_backup_manifests_target ON backup_manifests (target);

-- Semantic-core lifecycle events (the canonical audit/sync journal). Each
-- mailbox has a strictly increasing seq (the bbolt store kept a per-mailbox
-- counter bucket); semcore_lifecycle_seq is the atomic allocator and
-- semcore_lifecycle holds the events. kind is the numeric semcore.LifecycleKind.
CREATE TABLE IF NOT EXISTS semcore_lifecycle_seq (
    mailbox_id TEXT   NOT NULL PRIMARY KEY,
    seq_next   BIGINT NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS semcore_lifecycle (
    mailbox_id     TEXT     NOT NULL,
    seq            BIGINT   NOT NULL,
    folder_id      TEXT     NOT NULL DEFAULT '',
    item_id        TEXT     NOT NULL DEFAULT '',
    kind           SMALLINT NOT NULL DEFAULT 0,
    at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor          TEXT     NOT NULL DEFAULT '',
    change_key     TEXT     NOT NULL DEFAULT '',
    delegate_email TEXT     NOT NULL DEFAULT '',
    delegate_id    TEXT     NOT NULL DEFAULT '',
    PRIMARY KEY (mailbox_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_semcore_lifecycle_at ON semcore_lifecycle (at);

-- Semantic-core mailbox identities: the stable random MailboxId for each account
-- email, plus the IMAP UIDVALIDITY / modseq baseline. The bbolt store keyed these
-- by "e:"+email inside a shared bucket; here the dedicated table keys by email.
CREATE TABLE IF NOT EXISTS semcore_mailbox_identity (
    email          TEXT   NOT NULL PRIMARY KEY,
    mailbox_id     TEXT   NOT NULL UNIQUE,
    uid_validity   BIGINT NOT NULL DEFAULT 1,
    highest_modseq BIGINT NOT NULL DEFAULT 0
);

-- Semantic-core folder identities, keyed by (mailbox key, folder name) like the
-- bbolt store's "mboxKey\x00folderName" key. The folder's MailboxID is the
-- mailbox key itself (the bbolt store stored MailboxId{raw: mboxKey}).
CREATE TABLE IF NOT EXISTS semcore_folder_identity (
    mbox_key       TEXT    NOT NULL,
    folder_name    TEXT    NOT NULL,
    folder_id      TEXT    NOT NULL UNIQUE,
    parent_id      TEXT    NOT NULL DEFAULT '',
    role           TEXT    NOT NULL DEFAULT '',
    sort_order     INTEGER NOT NULL DEFAULT 0,
    highest_modseq BIGINT  NOT NULL DEFAULT 0,
    is_subscribed  BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (mbox_key, folder_name)
);
CREATE INDEX IF NOT EXISTS idx_semcore_folder_role ON semcore_folder_identity (mbox_key, role);
-- search_definition, when non-null, marks the folder as a MAPI/Outlook search
-- folder; it holds the JSON-encoded semcore.SearchFolderDef (saved criteria +
-- base folder set). Plain folders leave it NULL.
ALTER TABLE semcore_folder_identity ADD COLUMN IF NOT EXISTS search_definition JSONB;

-- Semantic-core item identities. Keyed by the bbolt storage_key (default
-- "email\x00msgKey", or an explicit key for the same content in another folder);
-- item_id is the canonical ItemId, indexed for id-based lookups/updates.
CREATE TABLE IF NOT EXISTS semcore_item_identity (
    storage_key     BYTEA   NOT NULL PRIMARY KEY,  -- bbolt key may contain NUL separators
    item_id         TEXT    NOT NULL,
    mailbox_id      TEXT    NOT NULL DEFAULT '',
    folder_id       TEXT    NOT NULL DEFAULT '',
    change_key      TEXT    NOT NULL DEFAULT '',
    conversation_id TEXT    NOT NULL DEFAULT '',
    msg_key         TEXT    NOT NULL DEFAULT '',
    email           TEXT    NOT NULL DEFAULT '',
    is_read         BOOLEAN NOT NULL DEFAULT false,
    categories      TEXT[]  NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_semcore_item_id ON semcore_item_identity (item_id);
CREATE INDEX IF NOT EXISTS idx_semcore_item_folder ON semcore_item_identity (folder_id);

-- Semantic-core conversation identities (ConversationId -> owning mailbox).
CREATE TABLE IF NOT EXISTS semcore_conversation_identity (
    conversation_id TEXT NOT NULL PRIMARY KEY,
    mailbox_id      TEXT NOT NULL DEFAULT ''
);

-- Semantic-core per-client sync state (EWS SyncFolderItems watermarks). Keyed by
-- (mailbox, folder, client); version is bumped on every write and folder_gone is
-- set when the folder is deleted after a token was issued.
CREATE TABLE IF NOT EXISTS semcore_sync_state (
    mailbox_id  TEXT   NOT NULL,
    folder_id   TEXT   NOT NULL DEFAULT '',
    client_id   TEXT   NOT NULL,
    watermark   TEXT   NOT NULL DEFAULT '',
    version     BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    folder_gone BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (mailbox_id, folder_id, client_id)
);
CREATE INDEX IF NOT EXISTS idx_semcore_sync_folder ON semcore_sync_state (folder_id);

-- Semantic-core tombstones (deletion records for incremental sync). Keyed by
-- (mailbox, folder, item, kind); a newer deletion supersedes an older one for the
-- same key. kind is the numeric semcore.LifecycleKind.
CREATE TABLE IF NOT EXISTS semcore_tombstone (
    mailbox_id TEXT     NOT NULL,
    folder_id  TEXT     NOT NULL DEFAULT '',
    item_id    TEXT     NOT NULL DEFAULT '',
    kind       SMALLINT NOT NULL DEFAULT 0,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor      TEXT     NOT NULL DEFAULT '',
    PRIMARY KEY (mailbox_id, folder_id, item_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_semcore_tombstone_since ON semcore_tombstone (mailbox_id, deleted_at);

-- Semantic-core push/pull subscriptions (EWS notifications). expires_at/drained_at
-- are NULL when unset; folder_ids empty means all folders. kind is the numeric
-- semcore.SubscriptionKind.
CREATE TABLE IF NOT EXISTS semcore_subscription (
    id         TEXT     NOT NULL PRIMARY KEY,
    mailbox_id TEXT     NOT NULL,
    kind       SMALLINT NOT NULL DEFAULT 0,
    folder_ids TEXT[]   NOT NULL DEFAULT '{}',
    last_seq   BIGINT   NOT NULL DEFAULT 0,
    push_url   TEXT     NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    drained_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_semcore_subscription_mbox ON semcore_subscription (mailbox_id);

-- Semantic-core delegate grants (EWS delegation). Keyed by id; the natural
-- (owner, delegate_email) pair is unique so PutDelegate can upsert. The six
-- per-folder permission levels are flattened to typed columns (the
-- DelegateFolderPermissions struct has a fixed folder set).
CREATE TABLE IF NOT EXISTS semcore_delegate (
    id                       TEXT NOT NULL PRIMARY KEY,
    owner_id                 TEXT NOT NULL,
    delegate_email           TEXT NOT NULL,
    delegate_user_id         TEXT NOT NULL DEFAULT '',
    perm_calendar            TEXT NOT NULL DEFAULT '',
    perm_tasks               TEXT NOT NULL DEFAULT '',
    perm_inbox               TEXT NOT NULL DEFAULT '',
    perm_contacts            TEXT NOT NULL DEFAULT '',
    perm_notes               TEXT NOT NULL DEFAULT '',
    perm_journal             TEXT NOT NULL DEFAULT '',
    view_private_items       BOOLEAN NOT NULL DEFAULT false,
    receive_copies           BOOLEAN NOT NULL DEFAULT false,
    deliver_meeting_requests TEXT NOT NULL DEFAULT '',
    can_send_as              BOOLEAN NOT NULL DEFAULT false,
    can_send_on_behalf       BOOLEAN NOT NULL DEFAULT false,
    granted_by               TEXT NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, delegate_email)
);
CREATE INDEX IF NOT EXISTS idx_semcore_delegate_owner ON semcore_delegate (owner_id);
CREATE INDEX IF NOT EXISTS idx_semcore_delegate_email ON semcore_delegate (delegate_email);

-- Semantic-core inbox rules. Scalar fields are typed columns; the variant
-- condition/action lists are JSONB payloads (the Q1 "typed table + opaque
-- payload" pattern — NOT the rejected generic (bucket,key,jsonb) shape).
CREATE TABLE IF NOT EXISTS semcore_rule (
    id         TEXT     NOT NULL PRIMARY KEY,
    mailbox_id TEXT     NOT NULL,
    change_key TEXT     NOT NULL DEFAULT '',
    name       TEXT     NOT NULL DEFAULT '',
    enabled    BOOLEAN  NOT NULL DEFAULT false,
    priority   INTEGER  NOT NULL DEFAULT 0,
    match_all  BOOLEAN  NOT NULL DEFAULT false,
    conditions JSONB    NOT NULL DEFAULT '[]',
    actions    JSONB    NOT NULL DEFAULT '[]',
    created    TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_semcore_rule_mbox ON semcore_rule (mailbox_id);

-- Semantic-core out-of-office policies (OOFId == MailboxId; one per mailbox).
-- enum fields (state/reply_style/audience) are stored as their numeric or string
-- values; exclude_addresses round-trips as TEXT[].
CREATE TABLE IF NOT EXISTS semcore_oof (
    id                    TEXT    NOT NULL PRIMARY KEY,
    mailbox_id            TEXT    NOT NULL,
    change_key            TEXT    NOT NULL DEFAULT '',
    enabled               BOOLEAN NOT NULL DEFAULT false,
    state                 TEXT    NOT NULL DEFAULT '',
    start_time            TIMESTAMPTZ,
    end_time              TIMESTAMPTZ,
    timezone              TEXT    NOT NULL DEFAULT '',
    subject               TEXT    NOT NULL DEFAULT '',
    text_body             TEXT    NOT NULL DEFAULT '',
    html_body             TEXT    NOT NULL DEFAULT '',
    reply_style           SMALLINT NOT NULL DEFAULT 0,
    internal_reply        TEXT    NOT NULL DEFAULT '',
    external_reply        TEXT    NOT NULL DEFAULT '',
    audience              SMALLINT NOT NULL DEFAULT 0,
    exclude_addresses     TEXT[]  NOT NULL DEFAULT '{}',
    ignore_lists          BOOLEAN NOT NULL DEFAULT false,
    ignore_bulk           BOOLEAN NOT NULL DEFAULT false,
    ignore_auto_replies   BOOLEAN NOT NULL DEFAULT false,
    send_interval_seconds BIGINT  NOT NULL DEFAULT 0
);

-- Semantic-core resource (room/equipment) booking policies.
CREATE TABLE IF NOT EXISTS semcore_resource (
    id                   TEXT     NOT NULL PRIMARY KEY,
    mailbox_id           TEXT     NOT NULL,
    change_key           TEXT     NOT NULL DEFAULT '',
    name                 TEXT     NOT NULL DEFAULT '',
    kind                 SMALLINT NOT NULL DEFAULT 0,
    email                TEXT     NOT NULL DEFAULT '',
    capacity             INTEGER  NOT NULL DEFAULT 0,
    description          TEXT     NOT NULL DEFAULT '',
    decision             SMALLINT NOT NULL DEFAULT 0,
    delegate_email       TEXT     NOT NULL DEFAULT '',
    allow_recurring      BOOLEAN  NOT NULL DEFAULT false,
    max_duration_minutes INTEGER  NOT NULL DEFAULT 0,
    min_notice_minutes   INTEGER  NOT NULL DEFAULT 0,
    allow_conflicts      BOOLEAN  NOT NULL DEFAULT false,
    max_conflicts        INTEGER  NOT NULL DEFAULT 0,
    hidden_from_gal      BOOLEAN  NOT NULL DEFAULT false,
    created              TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Semantic-core room lists (named groups of room resource emails).
CREATE TABLE IF NOT EXISTS semcore_room_list (
    id       TEXT   NOT NULL PRIMARY KEY,
    name     TEXT   NOT NULL DEFAULT '',
    rooms    TEXT[] NOT NULL DEFAULT '{}',
    created  TIMESTAMPTZ NOT NULL DEFAULT now(),
    modified TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Semantic-core collaboration identities (calendar items, tasks, contacts).
-- Keyed by msg_key (the blob key the writing surface chose); located by
-- (folder, ical_uid). RawData holds the canonical iCalendar/vCard payload.
CREATE TABLE IF NOT EXISTS semcore_calendar_item (
    msg_key    TEXT     NOT NULL PRIMARY KEY,
    id         TEXT     NOT NULL DEFAULT '',
    master_id  TEXT     NOT NULL DEFAULT '',
    folder_id  TEXT     NOT NULL DEFAULT '',
    mailbox_id TEXT     NOT NULL DEFAULT '',
    change_key TEXT     NOT NULL DEFAULT '',
    kind       SMALLINT NOT NULL DEFAULT 0,
    ical_uid   TEXT     NOT NULL DEFAULT '',
    raw_hash   TEXT     NOT NULL DEFAULT '',
    etag       TEXT     NOT NULL DEFAULT '',
    raw_data   TEXT     NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_semcore_calitem_uid ON semcore_calendar_item (folder_id, ical_uid);
CREATE INDEX IF NOT EXISTS idx_semcore_calitem_id ON semcore_calendar_item (id);

CREATE TABLE IF NOT EXISTS semcore_task (
    msg_key    TEXT NOT NULL PRIMARY KEY,
    id         TEXT NOT NULL DEFAULT '',
    folder_id  TEXT NOT NULL DEFAULT '',
    mailbox_id TEXT NOT NULL DEFAULT '',
    change_key TEXT NOT NULL DEFAULT '',
    ical_uid   TEXT NOT NULL DEFAULT '',
    raw_hash   TEXT NOT NULL DEFAULT '',
    etag       TEXT NOT NULL DEFAULT '',
    raw_data   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_semcore_task_uid ON semcore_task (folder_id, ical_uid);
CREATE INDEX IF NOT EXISTS idx_semcore_task_id ON semcore_task (id);

CREATE TABLE IF NOT EXISTS semcore_contact (
    msg_key    TEXT NOT NULL PRIMARY KEY,
    id         TEXT NOT NULL DEFAULT '',
    folder_id  TEXT NOT NULL DEFAULT '',
    mailbox_id TEXT NOT NULL DEFAULT '',
    change_key TEXT NOT NULL DEFAULT '',
    ical_uid   TEXT NOT NULL DEFAULT '',
    raw_hash   TEXT NOT NULL DEFAULT '',
    etag       TEXT NOT NULL DEFAULT '',
    raw_data   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_semcore_contact_uid ON semcore_contact (folder_id, ical_uid);
CREATE INDEX IF NOT EXISTS idx_semcore_contact_id ON semcore_contact (id);

-- Semantic-core background jobs (migration/backfill/rollback scheduler). Scalar
-- fields are typed columns; the variant step list is a JSONB payload.
CREATE TABLE IF NOT EXISTS semcore_job (
    id            TEXT     NOT NULL PRIMARY KEY,
    kind          TEXT     NOT NULL DEFAULT '',
    target        TEXT     NOT NULL DEFAULT '',
    mailbox_id    TEXT     NOT NULL DEFAULT '',
    state         TEXT     NOT NULL DEFAULT '',
    priority      INTEGER  NOT NULL DEFAULT 0,
    steps         JSONB    NOT NULL DEFAULT '[]',
    cursor        TEXT     NOT NULL DEFAULT '',
    errors        INTEGER  NOT NULL DEFAULT 0,
    last_error    TEXT     NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ,
    started_at    TIMESTAMPTZ,
    checkpoint_at TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    actor         TEXT     NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_semcore_job_kind_state ON semcore_job (kind, state);

-- TLS certificate-cache blob storage (generic key -> raw bytes) so ACME-issued
-- certificates and account keys are shared across active-active nodes and
-- survive restarts. Kept backend-agnostic; the autocert/CertMagic adapter lives
-- in internal/tls.
CREATE TABLE IF NOT EXISTS tls_cache (
    key        TEXT        PRIMARY KEY,
    data       BYTEA       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Distributed coordination locks for TLS certificate issuance/renewal across
-- active-active nodes (the CertMagic "instances sharing the same storage are
-- the same cluster" model). A row is a TTL'd lease: owner holds name until
-- expires_at, and a node may steal a row once its lease has expired, so a
-- crashed node cannot wedge certificate management for the cluster.
CREATE TABLE IF NOT EXISTS tls_locks (
    name       TEXT        PRIMARY KEY,
    owner      TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- Admin RBAC -------------------------------------------------------------------------

-- Named bundles of permissions that can be assigned to user accounts.
CREATE TABLE IF NOT EXISTS admin_roles (
    id          TEXT        PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Permissions granted to a role. The params column carries permission-specific
-- configuration (currently unused for all built-in permissions).
CREATE TABLE IF NOT EXISTS admin_role_permission_relation (
    id          TEXT        PRIMARY KEY,
    role_id     TEXT        NOT NULL REFERENCES admin_roles(id) ON DELETE CASCADE,
    permission  TEXT        NOT NULL,
    params      JSONB       NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_role_perms_role ON admin_role_permission_relation (role_id);

-- Many-to-many user-role assignments. A user may hold multiple roles.
CREATE TABLE IF NOT EXISTS admin_user_role_relation (
    id       TEXT PRIMARY KEY,
    user_id  TEXT NOT NULL,  -- account email (the account's canonical ID)
    role_id  TEXT NOT NULL REFERENCES admin_roles(id) ON DELETE CASCADE,
    UNIQUE (user_id, role_id)
);
CREATE INDEX IF NOT EXISTS idx_user_roles_user ON admin_user_role_relation (user_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_role ON admin_user_role_relation (role_id);

-- Spam check history for admin visibility.
CREATE TABLE IF NOT EXISTS spam_history (
    id          TEXT PRIMARY KEY,
    mail_from   TEXT NOT NULL,
    rcpt_to     TEXT NOT NULL,
    from_header TEXT NOT NULL DEFAULT '',
    subject     TEXT NOT NULL DEFAULT '',
    score       DOUBLE PRECISION NOT NULL DEFAULT 0,
    verdict     TEXT NOT NULL DEFAULT '',
    reasons     JSONB NOT NULL DEFAULT '[]',
    client_ip   TEXT NOT NULL DEFAULT '',
    helo        TEXT NOT NULL DEFAULT '',
    message_id  TEXT NOT NULL DEFAULT '',
    size        BIGINT NOT NULL DEFAULT 0,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_spam_history_timestamp ON spam_history (timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_spam_history_rcpt_to ON spam_history (rcpt_to);
CREATE INDEX IF NOT EXISTS idx_spam_history_verdict ON spam_history (verdict);

-- S/MIME signing certificate and private key per user.
CREATE TABLE IF NOT EXISTS user_smime_keys (
    user_id    TEXT PRIMARY KEY,
    cert_pem   TEXT NOT NULL,
    key_pem    TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
