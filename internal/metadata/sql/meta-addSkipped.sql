UPDATE metadata_batch SET skipped_existing = skipped_existing + 1, updated_at = @{now} WHERE id = @{id}
