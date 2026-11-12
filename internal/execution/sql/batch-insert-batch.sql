INSERT INTO audio_batch(id,state,kind,backend,created_at,updated_at,selection_total,prepared_count,preparation_error) VALUES(@{id},'PREPARING',@{kind},@{backend},@{now},@{now},@{total},0,'')
