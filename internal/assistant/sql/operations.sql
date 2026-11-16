SELECT o.* FROM assistant_operation o JOIN assistant_run r ON r.id=o.run_id WHERE r.conversation_id=? ORDER BY o.created_at,o.id;
