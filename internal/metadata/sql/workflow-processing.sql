SELECT EXISTS(SELECT 1 FROM media_import_job WHERE source_id=@{sourceId} AND state IN ('PARSING','QUEUED','RUNNING','VERIFYING')) AS processing
