UPDATE media_import_job SET complete_snapshot=1,deletion_count=0,collection_id=@{collectionId},collection_revision=@{revision},snapshot_id=@{snapshotId},base_id='' WHERE id=@{id}
