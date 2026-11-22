UPDATE media_import_entry
SET verification='SUPERSEDED', verification_level=NULL, verification_error='媒体位置或预期大小已更新，请核验当前资料中的位置'
WHERE job_id=@{id} AND ordinal BETWEEN @{first} AND @{last} AND song_id IS NOT NULL
  AND (verification IS NULL OR verification<>'SUPERSEDED')
  AND NOT EXISTS (
    SELECT 1 FROM media_song s JOIN media_shared_song shared ON shared.song_id=s.id
    WHERE s.id=media_import_entry.song_id AND s.source_id=@{sourceId} AND shared.source_id=s.source_id
      AND s.path_hash=media_import_entry.mapped_hash AND s.location_state NOT IN ('UNBOUND','REPLACED')
      AND (shared.expected_size=media_import_entry.file_size OR
           (shared.expected_size IS NULL AND (media_import_entry.file_size IS NULL OR s.file_size=media_import_entry.file_size)))
  )
