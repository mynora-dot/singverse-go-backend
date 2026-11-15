package catalog

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type fingerprintLibrary struct {
	*fakeLibrary
	version atomic.Int64
}

func (f *fingerprintLibrary) LibraryFingerprint(context.Context, store.DBTX) (string, error) {
	return fmt.Sprint(f.version.Load()), nil
}

func TestArtworkUnchangedLibrarySkipsDiscovery(t *testing.T) {
	s, _ := fixture(t)
	lib := &fingerprintLibrary{fakeLibrary: s.library.(*fakeLibrary)}
	s.library = lib
	ctx := context.Background()
	s.processArtwork(ctx)
	if lib.bulkCalls.Load() != 1 || len(s.artworkIndex) != 1 {
		t.Fatal("initial discovery missing", lib.bulkCalls.Load(), s.artworkIndex)
	}
	for i := 0; i < 3; i++ {
		s.artworkBuilt = time.Now().Add(-31 * time.Second)
		s.processArtwork(ctx)
	}
	if lib.bulkCalls.Load() != 1 {
		t.Fatal("unchanged library was projected again", lib.bulkCalls.Load())
	}
	lib.songs = append(lib.songs, map[string]any{"id": "new", "scannedAt": "latest", "result": map[string]any{"title": "New title", "artists": []string{"新歌手"}}})
	lib.version.Add(1)
	s.artworkBuilt = time.Now().Add(-31 * time.Second)
	s.processArtwork(ctx)
	if lib.bulkCalls.Load() != 2 || len(s.artworkIndex[artistID("新歌手")]) != 1 {
		t.Fatal("new artist was not discovered", lib.bulkCalls.Load(), s.artworkIndex)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM artist WHERE name='新歌手'").Scan(&count); err != nil || count != 1 {
		t.Fatal("new artist not persisted", count, err)
	}
}

func TestArtworkDiscoveryRollbackDoesNotPublishCache(t *testing.T) {
	s, _ := fixture(t)
	lib := &fingerprintLibrary{fakeLibrary: s.library.(*fakeLibrary)}
	s.library = lib
	original := s.q["a_pendingImage"]
	s.q["a_pendingImage"] = "SELECT * FROM missing_artwork_test_table"
	s.processArtwork(context.Background())
	if s.artworkIndex != nil || !s.artworkBuilt.IsZero() {
		t.Fatal("rolled back discovery was cached")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM artist").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	s.q["a_pendingImage"] = original
	s.processArtwork(context.Background())
	if lib.bulkCalls.Load() != 2 || len(s.artworkIndex) != 1 {
		t.Fatal("rolled back discovery was not retried", lib.bulkCalls.Load())
	}
}
