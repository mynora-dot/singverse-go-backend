UPDATE assistant_operation SET state='REJECTED',error='对话已停止',updated_at=? WHERE run_id=? AND state IN ('PENDING','APPROVED');
