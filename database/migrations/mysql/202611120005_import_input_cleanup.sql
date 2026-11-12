-- migrate:up
ALTER TABLE media_import_job ADD COLUMN input_state VARCHAR(20) NOT NULL DEFAULT 'RETAINED';
ALTER TABLE media_import_job ADD COLUMN input_cleanup_error VARCHAR(300);
ALTER TABLE media_import_job ADD COLUMN input_cleanup_after VARCHAR(40);
ALTER TABLE media_import_job ADD COLUMN verification_source_binding VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE media_import_entry ADD COLUMN verification_level VARCHAR(20);
CREATE INDEX idx_import_input_cleanup ON media_import_job(input_state,input_cleanup_after,id);
UPDATE media_import_job SET input_state='PENDING' WHERE phase='VERIFYING';
-- migrate:down
DROP INDEX idx_import_input_cleanup ON media_import_job;
ALTER TABLE media_import_entry DROP COLUMN verification_level;
ALTER TABLE media_import_job DROP COLUMN verification_source_binding;
ALTER TABLE media_import_job DROP COLUMN input_cleanup_after;
ALTER TABLE media_import_job DROP COLUMN input_cleanup_error;
ALTER TABLE media_import_job DROP COLUMN input_state;
