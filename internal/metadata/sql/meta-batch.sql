SELECT id, mode, status, providers_json, auto_apply, auto_apply_explicit, threshold, skipped_existing, created_at, updated_at FROM metadata_batch WHERE id = @{id}
