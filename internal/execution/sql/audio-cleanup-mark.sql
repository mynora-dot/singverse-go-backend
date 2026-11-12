UPDATE media_audio_job SET cleanup_state=@{state},cleanup_next_at=@{next} WHERE id=@{id}
