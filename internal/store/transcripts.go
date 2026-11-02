package store

import "gorm.io/gorm"

// migrateTaskTranscripts adds execution-output storage. It is additive (expand
// only): older binaries ignore the tables.
func migrateTaskTranscripts(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE coordinator_task_transcripts (
 task_id text NOT NULL REFERENCES coordinator_tasks(id) ON DELETE CASCADE,
 lease_epoch bigint NOT NULL,
 invocation integer NOT NULL DEFAULT 1,
 attempt_number bigint NOT NULL DEFAULT 0,
 worker_id text NOT NULL DEFAULT '',
 caller_run_id text NOT NULL,
 run_id text NOT NULL DEFAULT '',
 capture_id text NOT NULL DEFAULT '',
 availability text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT '',
 completeness text NOT NULL DEFAULT '',
 reason text NOT NULL DEFAULT '',
 summary jsonb,
 last_seq bigint NOT NULL DEFAULT 0,
 event_count bigint NOT NULL DEFAULT 0,
 stored_bytes bigint NOT NULL DEFAULT 0,
 capped boolean NOT NULL DEFAULT false,
 cursor text NOT NULL DEFAULT '',
 end_of_stream boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY (task_id, lease_epoch, invocation)
);
CREATE INDEX coordinator_task_transcripts_updated ON coordinator_task_transcripts (updated_at);
CREATE TABLE coordinator_task_transcript_events (
 task_id text NOT NULL,
 lease_epoch bigint NOT NULL,
 invocation integer NOT NULL DEFAULT 1,
 seq bigint NOT NULL CHECK (seq > 0),
 kind text NOT NULL,
 body jsonb NOT NULL,
 size integer NOT NULL,
 PRIMARY KEY (task_id, lease_epoch, invocation, seq),
 FOREIGN KEY (task_id, lease_epoch, invocation) REFERENCES coordinator_task_transcripts(task_id, lease_epoch, invocation) ON DELETE CASCADE
);`).Error
}
