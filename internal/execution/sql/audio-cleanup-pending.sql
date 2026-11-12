SELECT * FROM media_audio_job WHERE cleanup_state IN ('PENDING','CHECK') AND cleanup_next_at<=@{now} AND state IN ('SUCCEEDED','FAILED','CANCELLED') ORDER BY cleanup_next_at,id LIMIT 4
