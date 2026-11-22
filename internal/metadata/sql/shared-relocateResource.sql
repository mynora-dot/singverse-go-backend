UPDATE webdav_resource SET relative_path=@{path},path_hash=@{hash},generation=generation+1,task_id=@{taskId},
  last_scan_id=NULL,missing_since=NULL,missing_scans=0,file_size=@{size},modified_at=NULL,etag=NULL,
  state=CASE WHEN state IN ('PENDING','RUNNING') THEN 'IMPORTED' ELSE state END
WHERE song_id=@{songId} AND source_id=@{sourceId} AND path_hash<>@{hash} AND archived_at IS NULL
