SELECT song.id
FROM media_song song
JOIN media_source source ON source.id = song.source_id
LEFT JOIN media_shared_song shared ON shared.song_id = song.id
WHERE song.id > @{cursor}
  AND (source.source_type <> 'WEBDAV' OR shared.song_id IS NULL OR
       (shared.verification = 'PRESENT' AND EXISTS (
           SELECT 1 FROM webdav_resource remote WHERE remote.song_id = song.id
           AND remote.archived_at IS NULL AND remote.last_scan_id IS NOT NULL
       )))
ORDER BY song.id LIMIT 50
