SELECT m.wire FROM assistant_message m JOIN assistant_run r ON m.run_id=r.id WHERE m.conversation_id=? AND m.wire IS NOT NULL AND r.status='COMPLETED' ORDER BY m.created_at,m.id;
