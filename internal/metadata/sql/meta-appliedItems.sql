SELECT DISTINCT song_id, fingerprint FROM metadata_item WHERE status IN ('AUTO_APPLIED', 'MANUAL_APPLIED')
