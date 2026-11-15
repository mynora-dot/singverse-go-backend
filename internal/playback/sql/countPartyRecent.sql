SELECT COUNT(DISTINCT song_id) FROM playback_history
WHERE client_type <> 'ADMIN' AND played_ms > 0
  AND started_at >= @{from} AND started_at <= @{until}
  AND (@{songId}='' OR song_id=@{songId})
  AND (@{pattern}='' OR LOWER(title) LIKE @{pattern} ESCAPE '!' OR LOWER(artists_json) LIKE @{pattern} ESCAPE '!')
