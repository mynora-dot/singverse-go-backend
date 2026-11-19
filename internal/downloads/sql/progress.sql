UPDATE lx_download_item SET status='DOWNLOADING',received_bytes=@{received},total_bytes=@{total},message='',updated_at=@{now} WHERE id=@{id} AND status IN ('RESOLVING','DOWNLOADING')
