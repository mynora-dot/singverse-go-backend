SELECT song.*, source.source_type, source.location, source.username, source.password_encrypted,
       remote.song_id AS remote_resource_id, remote.identity_size, remote.identity_modified,
       remote.etag AS content_etag, remote.binding AS remote_binding, remote.generation AS remote_generation, remote.last_scan_id,
       shared.verification AS shared_verification
FROM media_song song
JOIN media_source source ON source.id = song.source_id
LEFT JOIN webdav_resource remote ON remote.song_id = song.id AND remote.archived_at IS NULL
LEFT JOIN media_shared_song shared ON shared.song_id = song.id
WHERE song.id = @{id}
