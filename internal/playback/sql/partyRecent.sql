WITH played AS (
  SELECT * FROM playback_history
  WHERE client_type <> 'ADMIN' AND played_ms > 0
    AND started_at >= @{from} AND started_at <= @{until}
    AND (@{songId}='' OR song_id=@{songId})
    AND (@{pattern}='' OR LOWER(title) LIKE @{pattern} ESCAPE '!' OR LOWER(artists_json) LIKE @{pattern} ESCAPE '!')
), recent AS (
  SELECT h.* FROM played h WHERE NOT EXISTS (
    SELECT 1 FROM played newer WHERE newer.song_id=h.song_id
      AND (newer.started_at>h.started_at OR (newer.started_at=h.started_at AND newer.id>h.id))
  )
)
SELECT * FROM recent ORDER BY started_at DESC,id DESC LIMIT @{size} OFFSET @{offset}
