SELECT ranked.*, read_session.fingerprint AS read_fingerprint FROM (
 SELECT job.*,ROW_NUMBER() OVER(PARTITION BY song_id ORDER BY created_at DESC,id DESC) AS ordinal
 FROM media_audio_job job
) ranked LEFT JOIN media_analysis_read read_session ON read_session.id=ranked.read_session_id WHERE ordinal<=20 ORDER BY ranked.song_id,ranked.created_at DESC,ranked.id DESC
