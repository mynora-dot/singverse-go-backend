-- migrate:up
ALTER TABLE metadata_batch ADD COLUMN auto_apply_explicit INT NOT NULL DEFAULT 0;
CREATE INDEX idx_metadata_item_applied ON metadata_item (status, song_id, fingerprint);

-- migrate:down
DROP INDEX idx_metadata_item_applied ON metadata_item;
ALTER TABLE metadata_batch DROP COLUMN auto_apply_explicit;
