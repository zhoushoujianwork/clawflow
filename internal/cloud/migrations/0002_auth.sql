-- 0002_auth.sql: identity, session, and API token tables for GitHub App auth.
--
-- ClawFlow runs cloud-only after this migration: every cloud-config or worker
-- request is authenticated against a row in either `sessions` (browser cookie)
-- or `api_tokens` (CLI / worker Bearer header).

CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    github_id  INTEGER NOT NULL UNIQUE,
    login      TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions (user_id);

-- api_tokens stores SHA-256 hashes of personal and machine-scoped Bearer tokens.
-- The plaintext token is shown to the user exactly once at creation time.
-- `kind` is either 'personal' (CLI login) or 'machine' (worker).
-- `machine_id` is non-empty only when kind='machine'.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id),
    hash         TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,
    machine_id   TEXT NOT NULL DEFAULT '',
    label        TEXT NOT NULL DEFAULT '',
    last_used_at TEXT,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens (user_id);

-- installations mirrors the GitHub App installations a user has authorized.
-- We refresh this list on each login and on installation webhook events.
CREATE TABLE IF NOT EXISTS installations (
    id                     TEXT PRIMARY KEY,
    github_installation_id INTEGER NOT NULL UNIQUE,
    account_login          TEXT NOT NULL,
    account_type           TEXT NOT NULL,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

-- Many-to-many: a user can authorize the App into multiple orgs/accounts,
-- and an installation can be visible to multiple users (org members).
CREATE TABLE IF NOT EXISTS user_installations (
    user_id         TEXT NOT NULL REFERENCES users(id),
    installation_id TEXT NOT NULL REFERENCES installations(id),
    PRIMARY KEY (user_id, installation_id)
);
