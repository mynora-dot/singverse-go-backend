UPDATE assistant_operation SET state=?,decision_id=?,decision=?,updated_at=? WHERE id=? AND run_id=? AND state='PENDING' AND revision=?;
