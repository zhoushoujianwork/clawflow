package store

import "gorm.io/gorm"

// migrateWorkflow adds the durable-workflow tables. The coordinator_tasks state
// CHECK already admits 'head-mismatch' in the SQLite-native schema.
func migrateWorkflow(tx *gorm.DB) error {
	return execAll(tx,
		`CREATE UNIQUE INDEX coordinator_active_implementation ON coordinator_tasks(repo,issue)
 WHERE kind = 'implement' AND issue > 0 AND state IN ('pending','leased')`, `
CREATE TABLE task_attempts (
 task_id text NOT NULL REFERENCES coordinator_tasks(id), attempt_number integer NOT NULL,
 lease_epoch integer NOT NULL, worker_id text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('infra','product')), status text NOT NULL,
 started_at timestamp NOT NULL, ended_at timestamp, error_kind text NOT NULL DEFAULT '',
 PRIMARY KEY (task_id, attempt_number), UNIQUE(task_id, lease_epoch)
)`, `
CREATE TABLE inbox_events (
 source text NOT NULL, external_id text NOT NULL, task_id text NOT NULL REFERENCES coordinator_tasks(id),
 received_at timestamp NOT NULL, processed_at timestamp NOT NULL,
 PRIMARY KEY(source, external_id)
)`, `
CREATE TABLE effect_intents (
 id text PRIMARY KEY, task_id text NOT NULL REFERENCES coordinator_tasks(id), lease_epoch integer NOT NULL,
 position integer NOT NULL, kind text NOT NULL, repo text NOT NULL, number integer NOT NULL DEFAULT 0,
 idempotency_key text NOT NULL UNIQUE, payload_digest text NOT NULL, payload text NOT NULL CHECK (json_valid(payload)),
 status text NOT NULL CHECK (status IN ('pending','applied','uncertain','failed')),
 applied_reference text NOT NULL DEFAULT '', updated_at timestamp NOT NULL
)`,
		`CREATE INDEX effect_intents_reconcile ON effect_intents(task_id,position) WHERE status IN ('pending','uncertain')`)
}
