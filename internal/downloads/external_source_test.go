package downloads

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/downloads/lxruntime"
)

func TestMain(m *testing.M) {
	for _, argument := range os.Args[1:] {
		if argument == "--lx-worker" {
			if lxruntime.Worker(context.Background(), os.Stdin, os.Stdout) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// Opt-in only: never copy a private source/key into fixtures or print signed URLs.
// SINGVERSE_LX_LIVE_SCRIPT points to the user's external file; all writes use t.TempDir.
func TestExternalSourceLiveQishuiDownload(t *testing.T) {
	path := os.Getenv("SINGVERSE_LX_LIVE_SCRIPT")
	if path == "" {
		t.Skip("set SINGVERSE_LX_LIVE_SCRIPT for an explicitly authorized live check")
	}
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read external source")
	}
	s, root, _ := fixture(t)
	runtime := lxruntime.New("")
	s.runtime = runtime
	t.Cleanup(func() { s.Close() })
	s.catalog = newCatalog(s.network)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	source, err := s.Import(ctx, SourceInput{Script: string(script), Environment: "mobile"})
	if err != nil {
		t.Fatalf("source import: %v", err)
	}
	id := str(source, "id")
	inspected, err := s.Inspect(ctx, id, true)
	if err != nil {
		t.Fatalf("source initialization: %v", err)
	}
	cap, ok := inspected["capabilities"].(map[string][]string)
	if !ok || len(cap["qs"]) == 0 {
		t.Fatal("source did not declare Qishui")
	}
	t.Logf("source initialization passed; platforms=%d; Qishui qualities=%v", len(cap), cap["qs"])
	query := os.Getenv("SINGVERSE_LX_LIVE_QUERY")
	if query == "" {
		query = "天行九歌 霍尊"
	}
	found, err := s.catalog.Search(ctx, "qs", query, 1)
	if err != nil || len(found.Tracks) == 0 {
		t.Fatalf("Qishui catalogue: candidates=%d error=%v", len(found.Tracks), err)
	}
	track := found.Tracks[0]
	t.Logf("Qishui catalogue passed; candidates=%d; selected ID=%s", len(found.Tracks), track.RemoteID)
	if err = s.remember(ctx, []Track{track}); err != nil {
		t.Fatal("cannot remember catalogue selection")
	}
	batch := createOne(t, s, track)
	itemID := str(itemFor(t, s, batch), "id")
	if err = s.download(ctx, claim(t, s, itemID)); err != nil {
		t.Fatalf("source resolve/download: %v", err)
	}
	s.finalizeBatch(itemID)
	row, err := s.row(ctx, s.db, "item", map[string]any{"id": itemID})
	if err != nil || str(row, "status") != "SUCCESS" {
		t.Fatal("download did not reach successful library publication")
	}
	file := filepath.Join(root, str(row, "relative_path"))
	stat, err := os.Stat(file)
	if err != nil || stat.Size() == 0 {
		t.Fatal("published audio is missing")
	}
	var count int
	if err = s.db.Get(&count, "SELECT COUNT(*) FROM media_song WHERE id=?", str(row, "song_id")); err != nil || count != 1 {
		t.Fatal("downloaded song was not registered in the isolated library")
	}
	_, lyricErr := os.Stat(file[:len(file)-len(filepath.Ext(file))] + ".lrc")
	t.Logf("audio validation and library publication passed; quality=%s format=%s bytes=%d lyricSidecar=%t", str(row, "resolved_quality"), filepath.Ext(file), stat.Size(), lyricErr == nil)
}

func TestExternalQishuiPublicPlaylistLive(t *testing.T) {
	if os.Getenv("SINGVERSE_LX_LIVE_SCRIPT") == "" {
		t.Skip("explicit live check only")
	}
	network := NewNetwork()
	defer network.Close()
	c := newCatalog(network).(*publicCatalog)
	q, err := c.qishui()
	if err != nil {
		t.Fatal(err)
	}
	data, err := q.client.SearchPage(t.Context(), "华语", "playlist", "0")
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, group := range asArray(data["result_groups"]) {
		for _, raw := range asArray(asObject(group)["data"]) {
			id = catalogText(at(raw, "entity", "playlist", "base_info"), "id")
			if id != "" {
				break
			}
		}
		if id != "" {
			break
		}
	}
	if id == "" {
		t.Fatal("no public playlist candidate")
	}
	collection, err := c.Collection(t.Context(), CatalogReference{"qs", "playlist", id}, 1)
	if err != nil || len(collection.Tracks) == 0 {
		t.Fatalf("public playlist ID=%s tracks=%d error=%v", id, len(collection.Tracks), err)
	}
	second, err := c.Collection(t.Context(), CatalogReference{"qs", "playlist", id}, 2)
	if err != nil || len(second.Tracks) == 0 {
		t.Fatalf("public playlist second page: %v", err)
	}
	seen := map[string]bool{}
	for _, track := range collection.Tracks {
		seen[track.ID] = true
	}
	for _, track := range second.Tracks {
		if seen[track.ID] {
			t.Fatal("public playlist repeated first-page tracks")
		}
	}
	t.Logf("public playlist read passed; tracks=%d total=%d more=%t", len(collection.Tracks), collection.Total, collection.HasMore)
}

func TestExternalQishuiMetadataLive(t *testing.T) {
	if os.Getenv("SINGVERSE_LX_LIVE_SCRIPT") == "" {
		t.Skip("explicit live check only")
	}
	network := NewNetwork()
	defer network.Close()
	c := newCatalog(network)
	found, err := c.Search(t.Context(), "qs", "传奇 郭沁", 1)
	if err != nil || len(found.Tracks) == 0 {
		t.Fatalf("metadata search: %v", err)
	}
	track, err := c.Detail(t.Context(), found.Tracks[0])
	if err != nil || track.ID != found.Tracks[0].ID {
		t.Fatalf("Qishui detail: %v", err)
	}
	lyric, err := c.Lyrics(t.Context(), track)
	if err != nil {
		t.Fatalf("Qishui lyrics: %v", err)
	}
	t.Logf("public song detail passed; identity preserved; lyricAvailable=%t", len(lyric) > 0)
}
