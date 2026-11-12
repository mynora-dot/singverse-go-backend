UPDATE audio_batch_item SET state=@{state},error=@{error},updated_at=@{now}
WHERE batch_id=@{batch} AND song_id=@{song} AND job_id=@{job} AND state=@{previous}
