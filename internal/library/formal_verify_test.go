package library

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/metastore"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestVerifyLocationsSharesDirectoryListingsWithinBatch(t *testing.T) {
	s, router := newTestService(t)
	// Keep this batch deterministic instead of racing the periodic verifier.
	s.cancel()
	s.wg.Wait()
	ctx := context.Background()
	var mu sync.Mutex
	hits := map[string]int{}
	removed := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		if r.Method != "PROPFIND" {
			t.Errorf("unexpected media request: %s", r.Method)
		}
		if strings.Contains(r.URL.Path, "/missing/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/denied/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, r.URL.Path)
		if !removed {
			fmt.Fprintf(w, `<d:response><d:href>%sone.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>5</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, r.URL.Path)
			fmt.Fprintf(w, `<d:response><d:href>%stwo.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>12</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, r.URL.Path)
		}
		fmt.Fprint(w, `</d:multistatus>`)
	}))
	defer remote.Close()
	sources := map[string]string{}
	for _, root := range []string{"a", "b"} {
		source := requestJSON(t, router, "POST", "/api/v1/media-sources", map[string]any{"name": root, "type": "WEBDAV", "location": remote.URL + "/" + root + "/"}, 201)
		sources[root] = source["id"].(string)
	}
	type pending struct {
		root, relative, state string
		size                  int64
		id                    string
	}
	items := []pending{
		{root: "a", relative: "album/one.mkv", size: 5, state: "PRESENT"},
		{root: "a", relative: "album/two.mkv", size: 10, state: "MISMATCH"},
		{root: "a", relative: "album/absent.mkv", size: 5, state: "MISSING"},
		{root: "a", relative: "other/one.mkv", size: 5, state: "PRESENT"},
		{root: "a", relative: "missing/one.mkv", size: 5, state: "MISSING"},
		{root: "a", relative: "missing/two.mkv", size: 5, state: "MISSING"},
		{root: "a", relative: "denied/one.mkv", size: 5, state: "UNREACHABLE"},
		{root: "a", relative: "denied/two.mkv", size: 5, state: "UNREACHABLE"},
		{root: "b", relative: "album/one.mkv", size: 10, state: "MISMATCH"},
	}
	if err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		for i := range items {
			item := &items[i]
			item.id = metastore.NewID()
			parsed, err := Parse(path.Base(item.relative), []Rule{})
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, s.q["song-insert"], item.id, sources[item.root], item.relative, hash(item.relative), item.size, nil, now(), "default", 0, nil, encode(parsed.Result), true, nil); err != nil {
				return err
			}
			if err = s.SyncMetadata(ctx, tx, item.id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE media_song SET location_state='PENDING'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.verifyLocations(ctx)
	for _, item := range items {
		var state string
		if err := s.db.QueryRowContext(ctx, "SELECT location_state FROM media_song WHERE id=?", item.id).Scan(&state); err != nil || state != item.state {
			t.Fatalf("%s/%s: state=%s want=%s err=%v", item.root, item.relative, state, item.state, err)
		}
		locations, err := s.formal.Locations(ctx, s.db, item.id)
		if err != nil || len(locations) != 1 || locations[0].State != item.state {
			t.Fatalf("formal location: %v, %v", locations, err)
		}
	}
	mu.Lock()
	for _, directory := range []string{"/a/album/", "/a/other/", "/a/missing/", "/a/denied/", "/b/album/"} {
		if hits[directory] != 1 {
			t.Errorf("%s: %d directory requests, want 1", directory, hits[directory])
		}
	}
	removed = true
	mu.Unlock()
	// A new batch must observe changes instead of retaining a directory cache.
	if _, err := s.db.ExecContext(ctx, "UPDATE media_song SET location_state='PENDING' WHERE id=?", items[0].id); err != nil {
		t.Fatal(err)
	}
	s.verifyLocations(ctx)
	var state string
	if err := s.db.QueryRowContext(ctx, "SELECT location_state FROM media_song WHERE id=?", items[0].id).Scan(&state); err != nil || state != "MISSING" {
		t.Fatalf("new batch reused stale listing: %s, %v", state, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/a/album/"] != 2 {
		t.Fatalf("new batch did not reread directory: %v", hits)
	}
}
