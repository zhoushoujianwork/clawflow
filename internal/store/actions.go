package store

import "gorm.io/gorm"

func migrateActions(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE actions_attachments (key text PRIMARY KEY, payload jsonb NOT NULL);
CREATE TABLE actions_evidence (
 key text NOT NULL REFERENCES actions_attachments(key), run_id bigint NOT NULL,
 attempt integer NOT NULL, payload jsonb NOT NULL,
 PRIMARY KEY(key,run_id,attempt)
);`).Error
}
