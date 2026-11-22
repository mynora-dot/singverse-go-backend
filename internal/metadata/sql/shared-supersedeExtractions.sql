UPDATE webdav_metadata_task SET state='SUPERSEDED'
WHERE song_id=@{songId} AND state IN ('QUEUED','RUNNING')
  AND generation<(SELECT generation FROM webdav_resource WHERE song_id=@{songId})
