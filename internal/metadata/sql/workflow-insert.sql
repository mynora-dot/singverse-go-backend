INSERT INTO media_import_job(id,source_id,binding,apply_mode,state,created_at,updated_at) VALUES(@{id},@{sourceId},@{binding},@{applyMode},'PARSING',@{now},@{now})
