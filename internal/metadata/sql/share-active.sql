SELECT id FROM webdav_share_job WHERE (@{sourceId} IS NULL OR source_id=@{sourceId}) AND (base_id IS NULL OR base_id='') AND state IN ('WAITING','QUEUED','RUNNING')
