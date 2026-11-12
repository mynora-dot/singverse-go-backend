-- migrate:up
ALTER TABLE media_audio_job ADD COLUMN read_session_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE media_audio_job ADD COLUMN cleanup_state VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE media_audio_job ADD COLUMN cleanup_next_at BIGINT NOT NULL DEFAULT 0;
CREATE INDEX media_audio_job_cleanup ON media_audio_job(cleanup_state,cleanup_next_at,id);
CREATE TABLE media_analysis_read (
 id VARCHAR(36) PRIMARY KEY,
 fingerprint VARCHAR(64) NOT NULL,
 max_bytes BIGINT NOT NULL,
 max_requests BIGINT NOT NULL,
 max_elapsed_ms BIGINT NOT NULL,
 used_bytes BIGINT NOT NULL DEFAULT 0,
 request_count BIGINT NOT NULL DEFAULT 0,
 elapsed_ms BIGINT NOT NULL DEFAULT 0,
 source_size BIGINT NULL,
 etag VARCHAR(512) NOT NULL DEFAULT '',
 modified VARCHAR(128) NOT NULL DEFAULT '',
 updated_at BIGINT NOT NULL
);
CREATE INDEX media_analysis_read_updated ON media_analysis_read(updated_at);

-- migrate:down
DROP TABLE media_analysis_read;
DROP INDEX media_audio_job_cleanup ON media_audio_job;
ALTER TABLE media_audio_job DROP COLUMN cleanup_next_at;
ALTER TABLE media_audio_job DROP COLUMN cleanup_state;
ALTER TABLE media_audio_job DROP COLUMN read_session_id;
