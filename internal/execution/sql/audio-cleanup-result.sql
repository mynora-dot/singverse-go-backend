UPDATE media_audio_job SET result_json=@{result},updated_at=@{now} WHERE id=@{id} AND cleanup_state IN ('PENDING','CHECK') AND state IN ('SUCCEEDED','FAILED','CANCELLED')
