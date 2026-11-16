UPDATE assistant_run SET status='STOPPED',error=?,updated_at=? WHERE id=? AND status IN ('RUNNING','WAITING');
