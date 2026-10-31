package store

import "gorm.io/gorm"

func migrateActions(tx *gorm.DB) error {
	return execAll(tx,
		`CREATE TABLE actions_attachments (key text PRIMARY KEY, payload text NOT NULL CHECK (json_valid(payload)))`, `
CREATE TABLE actions_evidence (
 key text NOT NULL REFERENCES actions_attachments(key), run_id integer NOT NULL,
 attempt integer NOT NULL, payload text NOT NULL CHECK (json_valid(payload)),
 PRIMARY KEY(key,run_id,attempt)
)`)
}
