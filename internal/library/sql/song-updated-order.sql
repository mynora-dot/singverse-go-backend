 ORDER BY GREATEST(
 REPLACE(song.scanned_at,'Z',''),
 COALESCE(REPLACE(meta.applied_at,'Z',''),''),
 COALESCE(REPLACE(formal_song.updated_at,'Z',''),''),
 COALESCE(REPLACE(formal_variant.updated_at,'Z',''),''),
 COALESCE(REPLACE(formal_media.updated_at,'Z',''),''),
 COALESCE(REPLACE(shared.updated_at,'Z',''),''),
 COALESCE(REPLACE(remote.updated_at,'Z',''),''),
 COALESCE(REPLACE(download.created_at,'Z',''),''),
 COALESCE((SELECT MAX(REPLACE(history.created_at,'Z','')) FROM classification_history history WHERE history.song_id=song.id),'')
 ) DESC,song.id ASC
