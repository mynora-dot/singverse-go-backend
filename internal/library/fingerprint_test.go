package library

import (
	"context"
	"testing"
)

func TestLibraryFingerprintTracksProjectionInputs(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	read := func() string {
		t.Helper()
		v, e := s.LibraryFingerprint(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	previous := read()
	mutations := []string{
		"INSERT INTO media_source(id,name,source_type,location,location_hash,username,created_at,updated_at) VALUES('src','Name','LOCAL','/tmp/fingerprint','fingerprint','','now','now')",
		"INSERT INTO media_song(id,source_id,relative_path,path_hash,file_size,modified_at,scanned_at,parser_profile_id,parser_revision,result_json,needs_review) VALUES('song','src','song.mp4','path',100,'modified','scanned','default',0,'{}',0)",
		"UPDATE media_song SET result_json='{\"title\":\"changed\"}' WHERE id='song'",
		"UPDATE media_source SET name='Renamed' WHERE id='src'",
		"INSERT INTO song_metadata(song_id,source_id,fingerprint,track_json,fields_json,origins_json,applied_at) VALUES('song','src','fingerprint','{}','[]','{}','same-time')",
		"UPDATE song_metadata SET track_json='{\"title\":\"new\"}' WHERE song_id='song'",
		"INSERT INTO classification_song(song_id,revision,state_json) VALUES('song',1,'{}')",
		"UPDATE classification_song SET state_json='{\"different\":true}' WHERE song_id='song'",
		"INSERT INTO song_artist_override(source_id,path_key,artists_json,revision) VALUES('src','path','[]',1)",
		"UPDATE song_artist_override SET artists_json='[\"New artist\"]' WHERE source_id='src'",
		"INSERT INTO media_shared_song(song_id,source_id,collection_id,snapshot_json,updated_at) VALUES('song','src','collection','{}','now')",
		"UPDATE media_shared_song SET verification='PRESENT' WHERE song_id='song'",
		"INSERT INTO metadata_document(id,entity_type,revision,document_json,updated_at) VALUES('song','MEDIA','1','{}','same-time')",
		"UPDATE metadata_document SET document_json='{\"different\":true}' WHERE id='song'",
		"UPDATE metadata_document SET suppressed=1 WHERE id='song'",
		"UPDATE metadata_document SET suppressed=0 WHERE id='song'",
		"UPDATE media_song SET location_state='MISSING' WHERE id='song'",
		"UPDATE media_song SET location_state='PRESENT' WHERE id='song'",
		"DELETE FROM media_song WHERE id='song'",
	}
	for i, query := range mutations {
		if _, e := s.db.Exec(query); e != nil {
			t.Fatalf("mutation %d: %v", i, e)
		}
		next := read()
		// An empty source does not contribute to the visible library.
		if i > 0 && next == previous {
			t.Fatalf("mutation %d did not change fingerprint: %s", i, query)
		}
		if again := read(); next != again {
			t.Fatalf("fingerprint not deterministic after mutation %d", i)
		}
		previous = next
	}
}
