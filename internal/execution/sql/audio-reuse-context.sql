SELECT loc.source_id AS location_source_id, loc.relative_path AS location_path, loc.state AS location_state, loc.observation_json AS location_observation, shared.verification AS shared_verification
FROM metadata_location loc LEFT JOIN media_shared_song shared ON shared.song_id=loc.media_id
WHERE loc.media_id=@{song}
