SELECT job.*, read_session.fingerprint AS read_fingerprint FROM media_audio_job job LEFT JOIN media_analysis_read read_session ON read_session.id=job.read_session_id WHERE job.id=@{id}
