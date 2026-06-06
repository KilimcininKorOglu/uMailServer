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
    totp_secret          TEXT        NOT NULL DEFAULT '',
    totp_enabled         BOOLEAN     NOT NULL DEFAULT FALSE,
    totp_last_used_step  BIGINT      NOT NULL DEFAULT 0,
    quota_used           BIGINT      NOT NULL DEFAULT 0,
    quota_limit          BIGINT      NOT NULL DEFAULT 0,
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
    phone                TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_accounts_domain ON accounts (domain);

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

-- Outgoing-mail signature (one per user).
CREATE TABLE IF NOT EXISTS user_signatures (
    user_email TEXT PRIMARY KEY,
    signature  TEXT NOT NULL DEFAULT ''
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
