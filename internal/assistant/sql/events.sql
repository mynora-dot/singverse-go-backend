SELECT run_id,sequence,kind,data,created_at FROM assistant_event WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT 200;
