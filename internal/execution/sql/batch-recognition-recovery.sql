SELECT item.*,job.result_json,job.error AS job_error
FROM audio_batch_item item
JOIN audio_batch batch ON batch.id=item.batch_id
JOIN media_audio_job job ON job.id=item.job_id AND job.fingerprint=item.fingerprint
WHERE batch.kind IN ('FAST','ANALYZE') AND (item.state='REVIEW' OR (item.state='FAILED' AND item.error='AUDIO_EXECUTION_FAILED'))
AND (item.batch_id>@{batch} OR (item.batch_id=@{batch} AND item.song_id>@{song}))
ORDER BY item.batch_id,item.song_id LIMIT 200
