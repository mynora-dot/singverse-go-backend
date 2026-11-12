UPDATE media_audio_job SET cleanup_next_at=@{next} WHERE id=@{id} AND cleanup_state IN ('PENDING','CHECK')
