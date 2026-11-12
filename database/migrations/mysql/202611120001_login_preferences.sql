-- migrate:up
ALTER TABLE auth_token ADD COLUMN persistent INTEGER NOT NULL DEFAULT 0 CHECK (persistent IN (0,1));

-- migrate:down
DELETE FROM auth_token WHERE persistent=1 OR kind='password_reset';
ALTER TABLE auth_token DROP COLUMN persistent;
