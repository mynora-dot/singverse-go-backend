package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/config"
)

func TestCacheRejectsPrematureEndListIncludingPreviouslySavedEntries(t *testing.T) {
	for _, tc := range []struct {
		name, extinf string
		offset       float64
		valid        bool
	}{
		{"complete", "239.24", 0, true},
		{"codec rounding", "238.9", 0, true},
		{"premature end", "17.08", 0, false},
		{"seeked remainder", "14.24", 225, true},
		{"truncated seek", "17.08", 30, false},
		{"not a duration", "NaN", 0, false},
		{"infinite duration", "+Inf", 0, false},
		{"negative duration", "-1", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			id := uuid()
			folder := filepath.Join(root, id)
			if err := os.Mkdir(folder, 0700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				cacheOwnerFile:       cacheOwner,
				"master.m3u8":        "#EXTM3U\nstream_0.m3u8\n",
				"stream_0.m3u8":      "#EXTM3U\n#EXTINF:" + tc.extinf + ",\nstream_0_000001.ts\n#EXT-X-ENDLIST\n",
				"stream_0_000001.ts": "media",
			}
			artifacts := map[string]any{}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(folder, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if name != cacheOwnerFile {
					artifacts[name] = map[string]any{"size": len(data)}
				}
			}
			execution := map[string]any{"media": map[string]any{"duration": 239.24}, "offset": tc.offset}
			if got := validatePlaylists(folder, artifacts, cacheDuration(execution)) == nil; got != tc.valid {
				t.Fatalf("capture accepted=%v, want %v", got, tc.valid)
			}
			s := &Service{cfg: config.Config{SongCachePath: root}}
			row := map[string]any{"id": id, "state": "READY", "target_json": encoded(map[string]any{"mode": "TRANSCODE"}), "execution_json": encoded(execution), "artifacts_json": encoded(artifacts)}
			if got := s.validCache(row); got != tc.valid {
				t.Fatalf("saved cache accepted=%v, want %v", got, tc.valid)
			}
			if _, err := os.Stat(filepath.Join(folder, "stream_0_000001.ts")); err != nil {
				t.Fatal("validation must preserve stored artifacts", err)
			}
		})
	}
}

func TestInvalidCacheCanBeReplacedWithoutDeletingStoredArtifacts(t *testing.T) {
	f := executionFixture(t, "")
	id, key := uuid(), hash("incomplete-cache")
	folder := filepath.Join(f.s.cfg.SongCachePath, id)
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{cacheOwnerFile: cacheOwner, "master.m3u8": "#EXTM3U\nstream_0.m3u8\n",
		"stream_0.m3u8": "#EXTM3U\n#EXTINF:17.08,\nstream_0_000001.ts\n#EXT-X-ENDLIST\n", "stream_0_000001.ts": "preserve"}
	artifacts := map[string]any{}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if name != cacheOwnerFile {
			artifacts[name] = map[string]any{"size": len(data)}
		}
	}
	row := map[string]any{"id": id, "cache_key": key, "song_id": f.song,
		"execution_json": encoded(map[string]any{"media": map[string]any{"duration": 239.24}}),
		"target_json":    encoded(map[string]any{"mode": "TRANSCODE"}), "artifacts_json": encoded(artifacts),
		"media_type": "VIDEO", "bytes": int64(7), "created_at": now()}
	if _, err := f.s.exec(t.Context(), f.db, "cache-insert", row); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.acquireCache(t.Context(), key, uuid()); err != nil || got != nil {
		t.Fatal("incomplete cache reused", got, err)
	}
	prior, err := f.s.one(t.Context(), f.db, "cache-find", map[string]any{"id": id})
	if err != nil || str(prior, "cache_key") == key {
		t.Fatal("invalid key was not detached", err)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil || string(data) != want {
			t.Fatal("stored cache was modified", name, err)
		}
	}
	row["id"] = uuid()
	if _, err := f.s.exec(t.Context(), f.db, "cache-insert", row); err != nil {
		t.Fatal("replacement lookup key is still blocked", err)
	}
}
