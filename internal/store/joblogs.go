package store

import "gorm.io/gorm"

// migrateTaskLogs adds additive tables for bounded execution output. They are
// keyed by task, lease epoch and Offload invocation so a retry never mixes
// into an earlier attempt. Dashboard snapshots never read these tables.
func migrateTaskLogs(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE task_log_streams (
 task_id text NOT NULL REFERENCES coordinator_tasks(id), lease_epoch bigint NOT NULL, invocation text NOT NULL,
 worker_id text NOT NULL, offload_run_id text NOT NULL DEFAULT '', backend text NOT NULL DEFAULT '', model text NOT NULL DEFAULT '',
 availability text NOT NULL, reason text NOT NULL DEFAULT '', state text NOT NULL DEFAULT '', completeness text NOT NULL DEFAULT '',
 summary jsonb, last_seq bigint NOT NULL DEFAULT 0, event_count integer NOT NULL DEFAULT 0, bytes_total bigint NOT NULL DEFAULT 0,
 dropped_events integer NOT NULL DEFAULT 0, limited boolean NOT NULL DEFAULT false, ended boolean NOT NULL DEFAULT false,
 purged boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 PRIMARY KEY (task_id, lease_epoch, invocation)
);
CREATE TABLE task_log_events (
 id bigserial PRIMARY KEY, task_id text NOT NULL, lease_epoch bigint NOT NULL, invocation text NOT NULL,
 seq bigint NOT NULL, event jsonb NOT NULL, size integer NOT NULL,
 UNIQUE (task_id, lease_epoch, invocation, seq),
 FOREIGN KEY (task_id, lease_epoch, invocation) REFERENCES task_log_streams(task_id, lease_epoch, invocation)
);
CREATE INDEX task_log_events_read ON task_log_events (task_id, lease_epoch, id);`).Error
}
