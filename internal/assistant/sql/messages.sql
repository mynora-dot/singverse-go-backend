SELECT id,conversation_id,run_id,role,content,created_at FROM assistant_message WHERE conversation_id=? AND role<>'wire' ORDER BY created_at,id;
