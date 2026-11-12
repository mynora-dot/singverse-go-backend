UPDATE media_import_job SET input_state='PENDING',input_cleanup_error=NULL,input_cleanup_after=NULL WHERE id=@{id} AND phase='VERIFYING' AND input_state='RETAINED'
