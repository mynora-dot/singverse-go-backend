SELECT COUNT(*) AS total FROM media_import_entry WHERE job_id=@{id} AND verification='PRESENT' AND verification_level='PATH_ONLY'
