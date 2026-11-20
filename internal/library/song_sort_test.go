package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestLatestSongUpdatesBeforePaginationAndSearch(t *testing.T) {
	s, router := newTestService(t)
	dir := t.TempDir()
	for i := range 6 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("测试歌手-排序歌曲%d.mkv", i)), []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sourceID := localSource(t, router, dir)["id"].(string)
	requestJSON(t, router, "POST", "/api/v1/media-sources/"+sourceID+"/scan", nil, 200)
	all := requestJSON(t, router, "GET", "/api/v1/media-songs?size=100", nil, 200)["items"].([]any)
	ids := []string{}
	for _, raw := range all {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	sort.Strings(ids)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE media_song SET scanned_at='2020-01-01T00:00:00Z'")
	exec("UPDATE metadata_document SET updated_at='2020-01-01T00:00:00Z'")
	exec("UPDATE media_song SET scanned_at='2020-01-02T00:00:00Z' WHERE id=?", ids[0])
	song, err := s.readSong(context.Background(), s.db, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO song_metadata(song_id,source_id,fingerprint,track_json,fields_json,origins_json,applied_at) VALUES(?,?,?,'{}','[]','{}',?)", ids[1], sourceID, song.contentFingerprint, "2020-01-02T00:00:00.1Z")
	songID, _, err := s.FormalIDs(context.Background(), s.db, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE metadata_document SET updated_at='2020-01-02T00:00:00.2Z' WHERE id=?", songID)
	exec("INSERT INTO classification_history(id,song_id,origin,before_json,after_json,after_revision,created_at) VALUES('history',?,'MANUAL','{}','{}',1,?)", ids[3], "2020-01-02T00:00:00.3Z")
	_, variantID, err := s.FormalIDs(context.Background(), s.db, ids[4])
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE metadata_document SET updated_at='2020-01-02T00:00:00.3Z' WHERE id=?", variantID)
	want := []string{ids[3], ids[4], ids[2], ids[1], ids[0], ids[5]}
	for _, filters := range []string{"", "&search=排序&sourceId=" + sourceID + "&unclassified=true"} {
		for p := range 3 {
			result := requestJSON(t, router, "GET", fmt.Sprintf("/api/v1/media-songs?sort=UPDATED_DESC&size=2&page=%d%s", p, filters), nil, 200)
			items := result["items"].([]any)
			if result["totalElements"] != float64(6) || len(items) != 2 {
				t.Fatal(result)
			}
			for i, item := range items {
				if item.(map[string]any)["id"] != want[p*2+i] {
					t.Fatalf("page %d filters %q: %#v, want %v", p, filters, result, want)
				}
			}
		}
	}
	for _, query := range []string{"", "?sort=SCANNED_DESC"} {
		result := requestJSON(t, router, "GET", "/api/v1/media-songs"+query, nil, 200)
		if result["items"].([]any)[0].(map[string]any)["id"] != ids[0] {
			t.Fatal("scan order changed", result)
		}
	}
	requestJSON(t, router, "GET", "/api/v1/media-songs?sort=invalid", nil, 400)
}
