SELECT * FROM audio_batch WHERE (@{kind}='' OR kind=@{kind} OR (@{kind}='RECOGNIZE' AND kind IN ('FAST','ANALYZE'))) ORDER BY created_at DESC,id LIMIT @{limit} OFFSET @{offset}
