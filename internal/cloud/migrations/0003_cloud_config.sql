-- 0003_cloud_config.sql: persist projects / repos / bindings / vcs_connections.
--
-- Up to PR 1 these tables lived in MemoryStore and SQLiteStore delegated
-- reads/writes back to the in-memory map (TODO at sqlite_store.go:33). This
-- migration moves them onto real SQL so the cloud server survives restarts.
--
-- owner_user_id is NULLable for now to avoid breaking the single-user self-
-- host scenario; a follow-up PR will enforce multi-tenant filtering.

CREATE TABLE IF NOT EXISTS projects (
    id            TEXT PRIMARY KEY,
    owner_user_id TEXT REFERENCES users(id),
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects (owner_user_id);

CREATE TABLE IF NOT EXISTS repos (
    id            TEXT PRIMARY KEY,
    owner_user_id TEXT REFERENCES users(id),
    name          TEXT NOT NULL,                 -- "owner/repo" slug
    platform      TEXT NOT NULL DEFAULT 'github',
    project_id    TEXT REFERENCES projects(id),
    base_branch   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_repos_owner   ON repos (owner_user_id);
CREATE INDEX IF NOT EXISTS idx_repos_project ON repos (project_id);
-- A repo is identified by its (owner_user_id, name). Two users may track the
-- same upstream repo independently; the partial unique index lets owner=NULL
-- coexist (single-user self-host) with future multi-tenant rows.
CREATE UNIQUE INDEX IF NOT EXISTS idx_repos_owner_name
    ON repos (owner_user_id, name);

CREATE TABLE IF NOT EXISTS bindings (
    id            TEXT PRIMARY KEY,
    owner_user_id TEXT REFERENCES users(id),
    machine_id    TEXT NOT NULL REFERENCES machines(id),
    repo_id       TEXT REFERENCES repos(id),
    project_id    TEXT REFERENCES projects(id),
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bindings_owner   ON bindings (owner_user_id);
CREATE INDEX IF NOT EXISTS idx_bindings_machine ON bindings (machine_id);
CREATE INDEX IF NOT EXISTS idx_bindings_repo    ON bindings (repo_id);
CREATE INDEX IF NOT EXISTS idx_bindings_project ON bindings (project_id);

-- VCS connections store GitHub App installation metadata per repo. The
-- github_app field is stored as JSON because its sub-shape (app_id /
-- installation_id / webhook_secret_ref / private_key_ref) is opaque to SQL.
CREATE TABLE IF NOT EXISTS vcs_connections (
    id              TEXT PRIMARY KEY,
    owner_user_id   TEXT REFERENCES users(id),
    repo            TEXT NOT NULL UNIQUE,
    platform        TEXT NOT NULL DEFAULT 'github',
    bound_machine_id TEXT NOT NULL DEFAULT '',
    github_app_json TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_vcs_owner ON vcs_connections (owner_user_id);
