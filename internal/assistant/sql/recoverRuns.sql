UPDATE assistant_run SET status='INTERRUPTED',error='服务已重启。未完成的对话已停止，请查看执行回执后重新提问。',updated_at=? WHERE status='RUNNING';
