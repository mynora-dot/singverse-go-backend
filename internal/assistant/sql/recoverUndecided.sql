UPDATE assistant_operation SET state='REJECTED',error='服务重启，尚未开始执行；请重新提问并确认',updated_at=? WHERE state IN ('PENDING','APPROVED') AND run_id IN (SELECT id FROM assistant_run WHERE status='RUNNING');
