SELECT id FROM media_import_job WHERE input_state='PENDING' AND phase='VERIFYING' AND (input_cleanup_after IS NULL OR input_cleanup_after<=@{now}) ORDER BY input_cleanup_after,id LIMIT 8
