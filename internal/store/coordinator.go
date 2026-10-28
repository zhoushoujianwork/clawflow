package store

import "gorm.io/gorm"

func migrateCoordinator(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE coordinator_workers (
 id text PRIMARY KEY, tags jsonb NOT NULL DEFAULT '[]', run_untagged boolean NOT NULL DEFAULT true,
 max_parallel integer NOT NULL CHECK (max_parallel > 0), roles jsonb NOT NULL DEFAULT '[]',
 backend text NOT NULL DEFAULT '', model text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE TABLE coordinator_tasks (
 id text PRIMARY KEY, kind text NOT NULL CHECK (kind IN ('implement','repair','acceptance','reply')),
 repo text NOT NULL, issue bigint NOT NULL DEFAULT 0, pr bigint NOT NULL DEFAULT 0,
 operator text NOT NULL, payload jsonb NOT NULL, tags jsonb NOT NULL DEFAULT '[]',
 priority integer NOT NULL DEFAULT 0, queue_position bigint NOT NULL DEFAULT 0,
 state text NOT NULL CHECK (state IN ('pending','leased','done','failed','cancelled')),
 attempt integer NOT NULL DEFAULT 0, lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz, fencing_token bigint NOT NULL DEFAULT 0,
 result jsonb, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 CHECK (issue > 0 OR pr > 0)
);
CREATE UNIQUE INDEX coordinator_active_task ON coordinator_tasks
 (repo, (CASE WHEN pr > 0 THEN 'pr' ELSE 'issue' END), (CASE WHEN pr > 0 THEN pr ELSE issue END), kind)
 WHERE state IN ('pending','leased');
CREATE INDEX coordinator_pending_order ON coordinator_tasks (priority, queue_position, created_at, id) WHERE state = 'pending';
CREATE INDEX coordinator_leases ON coordinator_tasks (lease_owner, lease_expires_at) WHERE state = 'leased';
CREATE TABLE coordinator_tokens (
 name text PRIMARY KEY, hash bytea NOT NULL CHECK (octet_length(hash) = 32),
 created_at timestamptz NOT NULL, last_seen timestamptz, revoked boolean NOT NULL DEFAULT false
);
CREATE TABLE coordinator_audit (
 id bigserial PRIMARY KEY, worker text NOT NULL, task text NOT NULL,
 action text NOT NULL, created_at timestamptz NOT NULL
);`).Error
}
