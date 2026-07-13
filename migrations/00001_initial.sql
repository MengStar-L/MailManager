-- +goose Up
PRAGMA foreign_keys = ON;

CREATE TABLE admins (
    id TEXT PRIMARY KEY,
    singleton INTEGER NOT NULL DEFAULT 1 CHECK (singleton = 1) UNIQUE,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    totp_secret_encrypted BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE setup_tokens (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE,
    username TEXT COLLATE NOCASE,
    totp_secret_encrypted BLOB,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX setup_tokens_active_idx ON setup_tokens (expires_at) WHERE used_at IS NULL;

CREATE TABLE auth_challenges (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX auth_challenges_active_idx ON auth_challenges (expires_at) WHERE used_at IS NULL;

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL UNIQUE,
    csrf_hash BLOB NOT NULL,
    expires_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX sessions_admin_idx ON sessions (admin_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE recovery_codes (
    id TEXT PRIMARY KEY,
    admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    code_hash BLOB NOT NULL UNIQUE,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX recovery_codes_admin_idx ON recovery_codes (admin_id) WHERE used_at IS NULL;

CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('google','microsoft','qq','163','imap')),
    display_name TEXT NOT NULL,
    email TEXT NOT NULL COLLATE NOCASE,
    color TEXT NOT NULL DEFAULT '#2563EB' CHECK (
        length(color) = 7 AND color GLOB '#[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]'
    ),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','syncing','ready','error','reauth_required','disabled')),
    auth_type TEXT NOT NULL CHECK (auth_type IN ('oauth2','password')),
    imap_host TEXT,
    imap_port INTEGER,
    smtp_host TEXT,
    smtp_port INTEGER,
    credential_encrypted BLOB,
    oauth_token_encrypted BLOB,
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    last_error_code TEXT,
    last_error_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX accounts_email_provider_idx ON accounts (email, provider);

CREATE TABLE provider_oauth_configs (
    provider TEXT PRIMARY KEY CHECK (provider IN ('google','microsoft')),
    client_id TEXT NOT NULL,
    client_secret_encrypted BLOB NOT NULL,
    configured_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE identities (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    email TEXT NOT NULL COLLATE NOCASE,
    display_name TEXT NOT NULL DEFAULT '',
    signature_html TEXT NOT NULL DEFAULT '',
    is_primary INTEGER NOT NULL DEFAULT 1 CHECK (is_primary IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX identities_one_primary_idx ON identities (account_id) WHERE is_primary = 1;

CREATE TABLE folders (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    remote_name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    delimiter TEXT,
    role TEXT NOT NULL DEFAULT 'other' CHECK (role IN ('inbox','sent','drafts','archive','trash','junk','all','other')),
    role_source TEXT NOT NULL DEFAULT 'needs_user' CHECK (role_source IN ('special_use','provider_preset','needs_user','user')),
    uid_validity INTEGER,
    uid_next INTEGER,
    highest_modseq INTEGER,
    selectable INTEGER NOT NULL DEFAULT 1 CHECK (selectable IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, remote_name)
);
CREATE INDEX folders_account_role_idx ON folders (account_id, role);

CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    thread_key TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    preview TEXT NOT NULL DEFAULT '',
    latest_at INTEGER NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0,
    unread_count INTEGER NOT NULL DEFAULT 0,
    starred INTEGER NOT NULL DEFAULT 0 CHECK (starred IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, thread_key)
);
CREATE INDEX conversations_latest_idx ON conversations (latest_at DESC, id DESC);
CREATE INDEX conversations_account_latest_idx ON conversations (account_id, latest_at DESC, id DESC);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    rfc_message_id TEXT,
    in_reply_to TEXT,
    references_json TEXT NOT NULL DEFAULT '[]',
    gmail_message_id TEXT,
    subject TEXT NOT NULL DEFAULT '',
    from_json TEXT NOT NULL DEFAULT '[]',
    to_json TEXT NOT NULL DEFAULT '[]',
    cc_json TEXT NOT NULL DEFAULT '[]',
    bcc_json TEXT NOT NULL DEFAULT '[]',
    reply_to_json TEXT NOT NULL DEFAULT '[]',
    sent_at INTEGER,
    received_at INTEGER NOT NULL,
    preview TEXT NOT NULL DEFAULT '',
    body_text TEXT NOT NULL DEFAULT '',
    body_html_clean TEXT NOT NULL DEFAULT '',
    search_tokens TEXT NOT NULL DEFAULT '',
    seen INTEGER NOT NULL DEFAULT 0 CHECK (seen IN (0,1)),
    flagged INTEGER NOT NULL DEFAULT 0 CHECK (flagged IN (0,1)),
    answered INTEGER NOT NULL DEFAULT 0 CHECK (answered IN (0,1)),
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX messages_conversation_idx ON messages (conversation_id, received_at, id);
CREATE INDEX messages_account_message_id_idx ON messages (account_id, rfc_message_id);
CREATE UNIQUE INDEX messages_gmail_id_idx ON messages (account_id, gmail_message_id) WHERE gmail_message_id IS NOT NULL;

CREATE TABLE message_locations (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_id TEXT NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid_validity INTEGER NOT NULL,
    uid INTEGER NOT NULL,
    flags_json TEXT NOT NULL DEFAULT '[]',
    labels_json TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, folder_id, uid_validity, uid)
);
CREATE INDEX message_locations_message_idx ON message_locations (message_id);

CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    part_id TEXT NOT NULL,
    filename TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    content_id TEXT,
    disposition TEXT NOT NULL DEFAULT 'attachment' CHECK (disposition IN ('attachment','inline')),
    size_bytes INTEGER NOT NULL DEFAULT 0,
    cache_path TEXT,
    cache_sha256 BLOB,
    cached_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (message_id, part_id)
);
CREATE INDEX attachments_message_idx ON attachments (message_id);

CREATE TABLE sync_checkpoints (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_id TEXT NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid_validity INTEGER,
    last_uid INTEGER,
    highest_modseq INTEGER,
    backfill_before INTEGER,
    state TEXT NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','syncing','backfilling','error','paused')),
    last_success_at INTEGER,
    last_error_code TEXT,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, folder_id)
);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('mark_read','mark_unread','star','unstar','archive','move','trash','delete')),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','cancelled','unknown','needs_attention')),
    payload_json TEXT NOT NULL,
    execute_after INTEGER NOT NULL,
    undo_until INTEGER,
    started_at INTEGER,
    completed_at INTEGER,
    error_code TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX operations_queue_idx ON operations (status, execute_after);

CREATE TABLE drafts (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    identity_id TEXT NOT NULL REFERENCES identities(id),
    reply_to_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    forward_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    to_json TEXT NOT NULL DEFAULT '[]',
    cc_json TEXT NOT NULL DEFAULT '[]',
    bcc_json TEXT NOT NULL DEFAULT '[]',
    subject TEXT NOT NULL DEFAULT '',
    body_text TEXT NOT NULL DEFAULT '',
    body_html TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX drafts_updated_idx ON drafts (updated_at DESC, id DESC);

CREATE TABLE draft_attachments (
    id TEXT PRIMARY KEY,
    draft_id TEXT NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes INTEGER NOT NULL,
    storage_path TEXT NOT NULL,
    sha256 BLOB NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE outbox (
    id TEXT PRIMARY KEY,
    draft_id TEXT REFERENCES drafts(id) ON DELETE SET NULL,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','sending','sent','failed','unknown')),
    message_id TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    sent_at INTEGER,
    error_code TEXT,
    error_detail TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX outbox_queue_idx ON outbox (state, next_attempt_at);

CREATE TABLE oauth_states (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('google','microsoft')),
    state_hash BLOB NOT NULL UNIQUE,
    pkce_verifier_encrypted BLOB NOT NULL,
    redirect_uri TEXT NOT NULL,
    pending_account_json TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX oauth_states_expiry_idx ON oauth_states (expires_at) WHERE used_at IS NULL;

CREATE TABLE remote_image_rules (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    sender_pattern TEXT NOT NULL COLLATE NOCASE,
    allow_remote_images INTEGER NOT NULL CHECK (allow_remote_images IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, sender_pattern)
);

CREATE TABLE app_settings (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE VIRTUAL TABLE message_search USING fts5(
    message_id UNINDEXED,
    account_id UNINDEXED,
    subject_tokens,
    address_tokens,
    body_tokens,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- +goose Down
DROP TABLE IF EXISTS message_search;
DROP TABLE IF EXISTS app_settings;
DROP TABLE IF EXISTS remote_image_rules;
DROP TABLE IF EXISTS oauth_states;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS draft_attachments;
DROP TABLE IF EXISTS drafts;
DROP TABLE IF EXISTS operations;
DROP TABLE IF EXISTS sync_checkpoints;
DROP TABLE IF EXISTS attachments;
DROP TABLE IF EXISTS message_locations;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS conversations;
DROP TABLE IF EXISTS folders;
DROP TABLE IF EXISTS identities;
DROP TABLE IF EXISTS provider_oauth_configs;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS recovery_codes;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS auth_challenges;
DROP TABLE IF EXISTS setup_tokens;
DROP TABLE IF EXISTS admins;
