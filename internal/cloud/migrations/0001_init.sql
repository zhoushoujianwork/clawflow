-- 0001_init.sql: initial schema for the worker protocol store.

CREATE TABLE IF NOT EXISTS machines (
    id           TEXT PRIMARY KEY,
    hostname     TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    last_seen_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workers (
    id              TEXT PRIMARY KEY,
    machine_id      TEXT NOT NULL REFERENCES machines(id),
    token           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'online',
    capacity        INTEGER NOT NULL DEFAULT 1,
    active_runs_json TEXT NOT NULL DEFAULT '[]',
    last_seen_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    spec_json       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    bound_machine_id TEXT NOT NULL DEFAULT '',
    lease_worker_id TEXT NOT NULL DEFAULT '',
    lease_expires_at TEXT,
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    dedupe_key      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

-- Partial unique index enforces dedupe only when dedupe_key is non-empty.
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_dedupe_key
    ON jobs (dedupe_key) WHERE dedupe_key != '';

CREATE INDEX IF NOT EXISTS idx_jobs_status_created
    ON jobs (status, created_at);

CREATE TABLE IF NOT EXISTS runs (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL REFERENCES jobs(id),
    status     TEXT NOT NULL,
    outcome    TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at   TEXT
);

CREATE TABLE IF NOT EXISTS run_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL REFERENCES runs(id),
    time        TEXT NOT NULL,
    level       TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL,
    fields_json TEXT NOT NULL DEFAULT '{}'
);

-- Index for efficient ordered event retrieval per run.
CREATE INDEX IF NOT EXISTS idx_run_events_run_id
    ON run_events (run_id, time);
