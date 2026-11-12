SELECT job.*, reads.fingerprint AS read_fingerprint FROM media_audio_job job LEFT JOIN media_analysis_read reads ON reads.id=job.read_session_id WHERE job.id=@{id}
