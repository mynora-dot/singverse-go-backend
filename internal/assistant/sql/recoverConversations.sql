UPDATE assistant_conversation SET active_run_id=NULL WHERE active_run_id IN (SELECT id FROM assistant_run WHERE status<>'WAITING');
