package store

import "gorm.io/gorm"

func migrateCoordinator(tx *gorm.DB) error {
	return execAll(tx, `
CREATE TABLE coordinator_workers (
 id text PRIMARY KEY, tags text NOT NULL DEFAULT '[]' CHECK (json_valid(tags)), run_untagged boolean NOT NULL DEFAULT true,
 max_parallel integer NOT NULL CHECK (max_parallel > 0), roles text NOT NULL DEFAULT '[]' CHECK (json_valid(roles)),
 backend text NOT NULL DEFAULT '', model text NOT NULL DEFAULT '',
 created_at timestamp NOT NULL, updated_at timestamp NOT NULL
)`, `
CREATE TABLE coordinator_tasks (
 id text PRIMARY KEY, kind text NOT NULL CHECK (kind IN ('implement','repair','acceptance','reply')),
 repo text NOT NULL, issue integer NOT NULL DEFAULT 0, pr integer NOT NULL DEFAULT 0,
 operator text NOT NULL, payload text NOT NULL CHECK (json_valid(payload)), tags text NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
 priority integer NOT NULL DEFAULT 0, queue_position integer NOT NULL DEFAULT 0,
 state text NOT NULL CHECK (state IN ('pending','leased','done','failed','cancelled','head-mismatch')),
 attempt integer NOT NULL DEFAULT 0, lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamp, fencing_token integer NOT NULL DEFAULT 0,
 result text CHECK (result IS NULL OR json_valid(result)), created_at timestamp NOT NULL, updated_at timestamp NOT NULL,
 CHECK (issue > 0 OR pr > 0)
)`, `
CREATE UNIQUE INDEX coordinator_active_task ON coordinator_tasks
 (repo, (CASE WHEN pr > 0 THEN 'pr' ELSE 'issue' END), (CASE WHEN pr > 0 THEN pr ELSE issue END), kind)
 WHERE state IN ('pending','leased')`,
		`CREATE INDEX coordinator_pending_order ON coordinator_tasks (priority, queue_position, created_at, id) WHERE state = 'pending'`,
		`CREATE INDEX coordinator_leases ON coordinator_tasks (lease_owner, lease_expires_at) WHERE state = 'leased'`, `
CREATE TABLE coordinator_tokens (
 name text PRIMARY KEY, hash blob NOT NULL CHECK (length(hash) = 32),
 created_at timestamp NOT NULL, last_seen timestamp, revoked boolean NOT NULL DEFAULT false
)`, `
CREATE TABLE coordinator_audit (
 id integer PRIMARY KEY AUTOINCREMENT, worker text NOT NULL, task text NOT NULL,
 action text NOT NULL, created_at timestamp NOT NULL
)`, `CREATE INDEX coordinator_audit_task ON coordinator_audit (task, id)`,
		`CREATE INDEX coordinator_audit_heartbeat ON coordinator_audit (worker, action, created_at)`)
}
