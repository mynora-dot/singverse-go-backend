INSERT INTO metadata_batch (id, mode, status, providers_json, auto_apply, auto_apply_explicit, threshold, skipped_existing, created_at, updated_at)
    VALUES (@{id}, @{mode}, @{status}, @{providers}, @{autoApply}, @{autoApplyExplicit}, @{threshold}, @{skipped}, @{now}, @{now})
