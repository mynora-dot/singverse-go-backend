package catalog

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestSharedFavoritesAtomicIdempotentAndPersistent(t *testing.T) {
	s, r := fixture(t)
	change := map[string]any{"songIds": []string{"song-a", "song-b"}, "selected": true}
	first := request(t, r, "PUT", "/api/v1/favorites", change, 200)
	again := request(t, r, "PUT", "/api/v1/favorites", change, 200)
	if encode(first) != encode(again) || num(first["revision"]) != 1 {
		t.Fatal(first, again)
	}
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a", "missing"}, "selected": false}, 200)
	before := request(t, r, "GET", "/api/v1/favorites/ids", nil, 200)
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a", "missing"}, "selected": true}, 409)
	after := request(t, r, "GET", "/api/v1/favorites/ids", nil, 200)
	if encode(before) != encode(after) {
		t.Fatal("partial batch committed", before, after)
	}
	// Close the original connection and reconstruct the service from disk.
	database, err := store.Map(context.Background(), s.db, "PRAGMA database_list")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlx.Open("sqlite", str(database["file"]))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened := &Service{db: db, q: s.q, library: s.library}
	page, err := reopened.listFavorites(context.Background(), "稻香", "AVAILABLE", "TITLE", 0, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	result := page.(map[string]any)
	if result["totalElements"] != 1 || result["totalFavorites"] != 1 || str(result["items"].([]map[string]any)[0]["id"]) != "song-b" {
		t.Fatal(result)
	}
	if len(s.library.(*fakeLibrary).songs) != 2 {
		t.Fatal("cancel removed original songs")
	}
}

func TestFavoritesMissingSnapshotAndFilteringBeforePagination(t *testing.T) {
	s, r := fixture(t)
	lib := s.library.(*fakeLibrary)
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a", "song-b"}, "selected": true}, 200)
	lib.songs = lib.songs[1:]
	result, err := s.listFavorites(context.Background(), "晴天", "UNAVAILABLE", "RECENT", 0, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	p := result.(map[string]any)
	row := p["items"].([]map[string]any)[0]
	if p["totalElements"] != 1 || row["available"] != false || row["title"] != "晴天" || str(row["coverPath"]) != "" {
		t.Fatal(p)
	}
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a"}, "selected": true}, 200)
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a"}, "selected": false}, 200)
	for _, body := range []any{map[string]any{"songIds": []string{"song-b"}}, map[string]any{"songIds": []string{"song-b", "song-b"}, "selected": true}, map[string]any{"songIds": []string{}, "selected": false}} {
		request(t, r, "PUT", "/api/v1/favorites", body, 400)
	}
	for _, query := range []string{"size=101", "availability=bad", "sort=bad", "page=-1"} {
		request(t, r, "GET", "/api/v1/favorites?"+query, nil, 400)
	}
}

func TestFavoritesWriteFailureRollsBackEntireBatch(t *testing.T) {
	s, r := fixture(t)
	// Fail the second insert after the first has been written in the transaction.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_favorite BEFORE INSERT ON nas_favorite WHEN NEW.song_id='song-b' BEGIN SELECT RAISE(ABORT, 'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	request(t, r, "PUT", "/api/v1/favorites", map[string]any{"songIds": []string{"song-a", "song-b"}, "selected": true}, 500)
	err := store.WithinTx(context.Background(), s.db, func(tx store.DBTX) error {
		v, err := s.favoriteIDs(context.Background(), tx)
		if err == nil && (len(v["songIds"].([]string)) != 0 || num(v["revision"]) != 0) {
			t.Fatal(v)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
