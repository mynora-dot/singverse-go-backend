-- migrate:up
ALTER TABLE audio_batch ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT 'SEPARATE';
CREATE INDEX idx_audio_batch_kind_date ON audio_batch(kind,created_at,id);

-- migrate:down
DROP INDEX idx_audio_batch_kind_date ON audio_batch;
ALTER TABLE audio_batch DROP COLUMN kind;
