-- migrate:up
-- Only replace untouched defaults; explicitly saved preferences keep their values.
UPDATE metadata_settings
SET threshold = 0.8, min_interval_seconds = 1, max_interval_seconds = 2
WHERE id = 1 AND auto_apply = 0 AND threshold = 0.95
  AND min_interval_seconds = 3 AND max_interval_seconds = 8
  AND updated_at = '2026-10-28T00:00:00Z';

-- migrate:down
UPDATE metadata_settings
SET threshold = 0.95, min_interval_seconds = 3, max_interval_seconds = 8
WHERE id = 1 AND auto_apply = 0 AND threshold = 0.8
  AND min_interval_seconds = 1 AND max_interval_seconds = 2
  AND updated_at = '2026-10-28T00:00:00Z';
