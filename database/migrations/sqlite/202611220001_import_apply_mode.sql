-- migrate:up
ALTER TABLE media_import_job ADD COLUMN apply_mode VARCHAR(8) NOT NULL DEFAULT 'preview';

-- migrate:down
ALTER TABLE media_import_job DROP COLUMN apply_mode;
