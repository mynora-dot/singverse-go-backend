package party

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type fingerprintLibrary struct {
	*fakeLibrary
	version atomic.Int64
	loads   atomic.Int64
}

func (f *fingerprintLibrary) LibraryFingerprint(context.Context, store.DBTX) (string, error) {
	return fmt.Sprint(f.version.Load()), nil
}
func (f *fingerprintLibrary) LibrarySongs(ctx context.Context, tx store.DBTX) ([]map[string]any, error) {
	f.loads.Add(1)
	return f.fakeLibrary.LibrarySongs(ctx, tx)
}

func TestLibraryFingerprintPreservesInvalidation(t *testing.T) {
	f := newPartyFixture(t)
	lib := &fingerprintLibrary{fakeLibrary: f.lib}
	f.s.library = lib
	ctx := context.Background()
	first, err := f.s.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	built := first.built
	f.clock = f.clock.Add(31 * time.Second)
	again, err := f.s.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != again || lib.loads.Load() != 1 || !again.built.Equal(built) || !again.checked.Equal(f.clock) {
		t.Fatal("unchanged input rebuilt the index or changed its generation", lib.loads.Load())
	}
	lib.version.Add(1)
	f.lib.songs[0]["result"].(document)["title"] = "更新后的歌曲"
	f.clock = f.clock.Add(31 * time.Second)
	changed, err := f.s.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first || lib.loads.Load() != 2 || str(changed.byID["song-a"].song, "title") != "更新后的歌曲" {
		t.Fatal("changed library stayed cached")
	}
	if _, err = f.db.Exec("INSERT INTO artist(id,name,aliases_json,artist_type,biography,active,revision,creation_hash,name_key,created_at,updated_at) VALUES('managed','周杰伦','[]','MALE','',1,1,'key','key','now','now')"); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(31 * time.Second)
	profile, err := f.s.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lib.loads.Load() != 3 || str(profile.artists["managed"], "type") != "MALE" {
		t.Fatal("artist profile did not invalidate index")
	}
	f.s.Invalidate()
	if _, err = f.s.current(ctx); err != nil {
		t.Fatal(err)
	}
	if lib.loads.Load() != 4 {
		t.Fatal("explicit invalidation was ignored")
	}
}

func TestLibraryConcurrentReadAndInvalidation(t *testing.T) {
	f := newPartyFixture(t)
	f.s.library = &fingerprintLibrary{fakeLibrary: f.lib}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 12; j++ {
				f.s.Invalidate()
				index, err := f.s.current(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if len(index.entries) != 2 {
					t.Error("incomplete index published")
				}
			}
		})
	}
	wg.Wait()
	// Invalidate is serialized with publication, so the final invalidation
	// cannot be overwritten by a build which started earlier.
	f.s.Invalidate()
	if f.s.index != nil {
		t.Fatal("final invalidation was lost")
	}
}

type countedFileSize struct{ calls *atomic.Int64 }

func (v countedFileSize) MarshalJSON() ([]byte, error) { v.calls.Add(1); return []byte("1000"), nil }

func TestMobileLibraryProjectsOnlySelectedPage(t *testing.T) {
	f := newPartyFixture(t)
	var calls atomic.Int64
	f.lib.songs = nil
	for i := 0; i < 53; i++ {
		dimensions := document{}
		if i%2 == 0 {
			dimensions["GENRE"] = document{"tagIds": []string{"selected"}}
		}
		f.lib.songs = append(f.lib.songs, document{"id": fmt.Sprintf("%03d", i), "fileSize": countedFileSize{&calls}, "scannedAt": "same", "result": document{"title": "Song", "artists": []string{"Artist"}, "extension": "flac", "songCode": fmt.Sprintf("%03d", i)}, "classification": document{"dimensions": dimensions}})
	}
	if _, err := f.db.Exec("INSERT INTO classification_tag(id,dimension,name,aliases_json,active,revision) VALUES('selected','GENRE','Selected','[]',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.current(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.GET("/songs", func(c *gin.Context) { f.s.searchSongs(c, true) })
	for _, tc := range []struct {
		page, count int
		first       string
	}{{0, 20, "000"}, {1, 20, "020"}, {2, 13, "040"}, {3, 0, ""}, {1000000, 0, ""}} {
		calls.Store(0)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/songs?page=%d&size=20", tc.page), nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out document
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		items := array(out, "items")
		if number(out, "totalElements") != 53 || number(out, "page") != int64(tc.page) || len(items) != tc.count || calls.Load() != int64(tc.count) {
			t.Fatal(tc, out, "projections", calls.Load())
		}
		if tc.count > 0 {
			item := items[0].(map[string]any)
			if str(item, "id") != tc.first || str(item, "mediaType") != "AUDIO" || str(item, "format") != "FLAC" {
				t.Fatal(tc, item)
			}
		}
	}
	for _, tc := range []struct {
		query string
		total int64
		ids   []string
	}{
		{"tagIds=selected&page=1&size=3", 27, []string{"006", "008", "010"}},
		{"unclassified=true&page=1&size=3", 26, []string{"007", "009", "011"}},
		{"q=005&page=0&size=3", 1, []string{"005"}},
		{"artistId=" + artistID("Artist") + "&page=17&size=3", 53, []string{"051", "052"}},
	} {
		calls.Store(0)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/songs?"+tc.query, nil))
		var out document
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		items := array(out, "items")
		if w.Code != 200 || number(out, "totalElements") != tc.total || len(items) != len(tc.ids) || calls.Load() != int64(len(tc.ids)) {
			t.Fatal(tc, out, calls.Load())
		}
		for i, id := range tc.ids {
			if str(items[i].(map[string]any), "id") != id {
				t.Fatal(tc, out)
			}
		}
	}
}
