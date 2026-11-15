-- migrate:up
ALTER TABLE classification_settings ADD COLUMN builtin_covers_initialized INTEGER NOT NULL DEFAULT 0;

-- migrate:down
ALTER TABLE classification_settings DROP COLUMN builtin_covers_initialized;
