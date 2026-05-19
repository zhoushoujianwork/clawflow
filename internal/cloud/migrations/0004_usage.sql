-- 0004_usage.sql: per-run / per-chat-session token & cost storage.
--
-- Worker uploads `cloud.Usage` after a claude subprocess exits; cloud
-- stores it here so /api/cloud/usage/summary (PR 4 / sub 4) can render
-- the same shape the local `clawflow web` Usage page already speaks.
--
-- Denormalized columns (repo, operator, user_id, machine_id) live on
-- the row itself so aggregation can GROUP BY without joining jobs /
-- runs / bindings. Per-model breakdown is stored as JSON because its
-- arity is per-claude-version and not worth a side table for the
-- five-or-fewer models a typical run touches.

-- user_id is stored as plain TEXT (no FK) because: (a) cloud authorises
-- by *current* user from the auth context, not by a stored FK, and (b)
-- a deleted user shouldn't cascade-delete historical usage. Same for
-- the run_id reference — we want usage to outlive a runs-table cleanup.
CREATE TABLE IF NOT EXISTS run_usage (
    run_id                       TEXT PRIMARY KEY,
    user_id                      TEXT NOT NULL DEFAULT '',
    machine_id                   TEXT NOT NULL DEFAULT '',
    repo                         TEXT NOT NULL DEFAULT '',
    operator                     TEXT NOT NULL DEFAULT '',
    duration_ms                  INTEGER NOT NULL DEFAULT 0,
    num_turns                    INTEGER NOT NULL DEFAULT 0,
    total_cost_usd               REAL NOT NULL DEFAULT 0,
    input_tokens                 INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens  INTEGER NOT NULL DEFAULT 0,
    model_usage_json             TEXT NOT NULL DEFAULT '{}',
    ended_at                     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_run_usage_user_ended ON run_usage (user_id, ended_at);
CREATE INDEX IF NOT EXISTS idx_run_usage_repo       ON run_usage (repo);
CREATE INDEX IF NOT EXISTS idx_run_usage_operator   ON run_usage (operator);

CREATE TABLE IF NOT EXISTS chat_usage (
    session_id                   TEXT PRIMARY KEY,
    user_id                      TEXT NOT NULL,
    machine_id                   TEXT NOT NULL DEFAULT '',
    repo                         TEXT NOT NULL DEFAULT '',
    duration_ms                  INTEGER NOT NULL DEFAULT 0,
    num_turns                    INTEGER NOT NULL DEFAULT 0,
    total_cost_usd               REAL NOT NULL DEFAULT 0,
    input_tokens                 INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens  INTEGER NOT NULL DEFAULT 0,
    model_usage_json             TEXT NOT NULL DEFAULT '{}',
    ended_at                     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_chat_usage_user_ended ON chat_usage (user_id, ended_at);
CREATE INDEX IF NOT EXISTS idx_chat_usage_repo       ON chat_usage (repo);
