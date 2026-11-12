SELECT COUNT(*) FROM audio_batch WHERE (@{kind}='' OR kind=@{kind} OR (@{kind}='RECOGNIZE' AND kind IN ('FAST','ANALYZE')))
