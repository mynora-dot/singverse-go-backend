UPDATE assistant_operation SET state='UNCERTAIN',error='执行过程中服务中断，请核对业务页面；系统不会自动重放。',updated_at=? WHERE state='EXECUTING';
