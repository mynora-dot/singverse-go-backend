package catalog

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestLatestArtistUpdatesBeforePaginationAndFilters(t *testing.T) {
	s, router := fixture(t)
	lib := s.library.(*fakeLibrary)
	lib.songs = nil
	for _, name := range []string{"歌手A", "歌手B", "歌手C", "歌手D", "歌手E", "歌手F"} {
		lib.songs = append(lib.songs, map[string]any{"id": name, "result": map[string]any{"title": "曲目", "artists": []string{name}}})
	}
	request(t, router, "GET", "/api/v1/artists", nil, 200)
	ids := []string{}
	for i, name := range []string{"歌手A", "歌手B", "歌手C", "歌手D", "歌手E", "歌手F"} {
		id := artistID(name)
		ids = append(ids, id)
		at := "2020-01-02T00:00:00Z"
		if i > 0 && i < 4 {
			at = fmt.Sprintf("2020-01-02T00:00:00.%dZ", i)
		} else if i == 4 {
			at = "2020-01-02T00:00:00.3Z"
		} else if i == 5 {
			at = "2020-01-01T00:00:00Z"
		}
		if _, err := s.db.Exec("UPDATE artist SET updated_at=?,artist_type='MALE' WHERE id=?", at, id); err != nil {
			t.Fatal(err)
		}
	}
	tied := []string{ids[3], ids[4]}
	sort.Strings(tied)
	want := append(tied, ids[2], ids[1], ids[0], ids[5])
	for _, filters := range []string{"", "&q=歌手&type=MALE&artwork=MISSING"} {
		for p := range 3 {
			result := request(t, router, "GET", fmt.Sprintf("/api/v1/artists?sort=UPDATED_DESC&size=2&page=%d%s", p, filters), nil, 200)
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
	first := func(query string) string {
		t.Helper()
		return request(t, router, "GET", "/api/v1/artists?"+query, nil, 200)["items"].([]any)[0].(map[string]any)["id"].(string)
	}
	if first("") != ids[0] || first("sort=NAME_ASC") != ids[0] {
		t.Fatal("default name order changed")
	}
	request(t, router, "PUT", "/api/v1/artists/"+ids[5]+"/category", map[string]any{"type": "FEMALE", "revision": 1}, 200)
	if first("sort=UPDATED_DESC") != ids[5] {
		t.Fatal("category update not reflected in sort")
	}
	ctx := context.Background()
	if _, err := s.artistImage(ctx, ids[0], "portrait", "", []byte("image"), false); err != nil {
		t.Fatal(err)
	}
	if first("sort=UPDATED_DESC") != ids[0] {
		t.Fatal("manual artwork update not reflected in sort")
	}
	if _, err := s.tx(ctx, func(tx store.DBTX) (any, error) {
		return nil, s.storeArtwork(ctx, tx, ids[1], "BACKGROUND", []byte("automatic image"), ArtworkChoice{Provider: "THEAUDIODB"}, "", "AUTO")
	}); err != nil {
		t.Fatal(err)
	}
	if first("sort=UPDATED_DESC") != ids[1] {
		t.Fatal("automatic artwork update not reflected in sort")
	}
	request(t, router, "GET", "/api/v1/artists?sort=invalid", nil, 400)
}
