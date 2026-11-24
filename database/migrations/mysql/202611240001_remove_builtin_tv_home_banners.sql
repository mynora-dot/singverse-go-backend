-- migrate:up
DELETE FROM tv_home_banner
WHERE id IN (
  'builtin-01-blue-stage',
  'builtin-02-acoustic-evening',
  'builtin-03-moonlight-piano',
  'builtin-04-vinyl-memories',
  'builtin-05-neon-rhythm',
  'builtin-06-sunset-melody',
  'builtin-07-jazz-night',
  'builtin-08-starlit-echo'
);

-- migrate:down
-- The retired bundled image bytes are unavailable for restoration.
SELECT 1;
