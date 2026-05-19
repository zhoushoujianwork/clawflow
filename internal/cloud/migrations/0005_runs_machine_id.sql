-- 0005_runs_machine_id.sql: record which machine executed each run so the
-- worker-protocol handlers can verify ownership (issue #185).
-- Default '' preserves compatibility with runs created before this migration.

ALTER TABLE runs ADD COLUMN machine_id TEXT NOT NULL DEFAULT '';
