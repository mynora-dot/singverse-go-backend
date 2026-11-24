SELECT * FROM webdav_share_entry WHERE job_id=@{id} AND song_id>@{cursor} ORDER BY song_id LIMIT 50
