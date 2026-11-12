-- migrate:up
ALTER TABLE auth_token ADD COLUMN persistent INTEGER NOT NULL DEFAULT 0 CHECK (persistent IN (0,1));

-- migrate:down
-- Do not turn previously persistent sessions into ordinary active sessions.
DELETE FROM auth_token WHERE persistent=1 OR kind='password_reset';
ALTER TABLE auth_token DROP COLUMN persistent;
