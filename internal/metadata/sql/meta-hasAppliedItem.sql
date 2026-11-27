SELECT 1 AS applied FROM metadata_item
    WHERE song_id = @{songId} AND fingerprint = @{fingerprint} AND status IN ('AUTO_APPLIED', 'MANUAL_APPLIED') LIMIT 1
