SELECT * FROM webdav_share_job WHERE source_id=@{sourceId} AND (base_id IS NULL OR base_id='') ORDER BY created_at DESC,id DESC LIMIT 1
