UPDATE media_audio_job SET cleanup_state=CASE WHEN cleanup_state='PENDING' THEN 'PENDING' ELSE 'CHECK' END,cleanup_next_at=@{next} WHERE id=@{id} AND state IN ('CANCELLED','FAILED')
