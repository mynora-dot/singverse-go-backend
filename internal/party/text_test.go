package party

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func textFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newPartyFixture(t)
	root := t.TempDir()
	if _, err := f.db.Exec("INSERT INTO media_source(id,name,source_type,location,location_hash,username,created_at,updated_at) VALUES('source','下载','LOCAL',?,'text-source','','2026-11-20T00:00:00Z','2026-11-20T00:00:00Z')", root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO media_song(id,source_id,relative_path,path_hash,file_size,scanned_at,parser_profile_id,parser_revision,result_json,needs_review) VALUES('song-a','source','周杰伦-晴天.mp4','text-song',5,'2026-11-20T00:00:00Z','default',0,'{}',0)"); err != nil {
		t.Fatal(err)
	}
	return f, filepath.Join(root, "周杰伦-晴天.lrc")
}

func TestAdminTextTracksReadDownloadedLyrics(t *testing.T) {
	f, path := textFixture(t)
	endpoint := "/api/v1/media-songs/song-a/text-tracks"
	for _, test := range []struct {
		name, content, format string
	}{
		{"纯文本歌词", "第一句歌词\n第二句歌词", "text"},
		{"同步歌词", "[ti:测试歌曲]\n[00:01.20]第一句歌词\n[00:02.30]第二句歌词", "lrc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			response := f.request(t, "GET", endpoint, nil, "", true, false)
			body := want(t, response, 200)
			items := array(body, "items")
			if str(body, "songId") != "song-a" || len(items) != 1 || len(array(body, "warnings")) != 0 {
				t.Fatal(body)
			}
			item := items[0].(map[string]any)
			if str(item, "text") != test.content || str(item, "format") != test.format || response.header.Get("Cache-Control") != "no-store" {
				t.Fatal(item, response.header)
			}
		})
	}
	want(t, f.request(t, "GET", endpoint, nil, "", false, false), 401)
	device, _ := f.device(t)
	want(t, f.request(t, "GET", endpoint, nil, device, false, false), 401)
	want(t, f.request(t, "GET", "/api/v1/media-songs/unknown/text-tracks", nil, "", true, false), 404)
}

type textLibrary struct {
	*fakeLibrary
	lyrics map[string]any
}

func (f textLibrary) FormalLyrics(context.Context, store.DBTX, string) (map[string]any, error) {
	return f.lyrics, nil
}

func TestAdminTextTracksPreserveFormalLyricsAndClear(t *testing.T) {
	f, path := textFixture(t)
	if err := os.WriteFile(path, []byte("[00:01.00]文件中的歌词"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"人工整理的歌词", ""} {
		f.s.library = textLibrary{fakeLibrary: f.lib, lyrics: document{"text": content, "timing": "none"}}
		body := want(t, f.request(t, "GET", "/api/v1/media-songs/song-a/text-tracks", nil, "", true, false), 200)
		items := array(body, "items")
		if len(items) != 1 || str(items[0].(map[string]any), "id") != "metadata-lyrics" || str(items[0].(map[string]any), "text") != content {
			t.Fatal("formal lyrics or explicit clear was replaced by the sidecar", body)
		}
	}
}

func TestAdminTextTracksPreferTimedEmbeddedLyricsOverPlainSidecar(t *testing.T) {
	f, path := textFixture(t)
	if err := os.WriteFile(path, []byte("文件中的纯文本歌词"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE media_song SET embedded_tags_json=? WHERE id='song-a'", encode(document{"lyrics": document{"text": "[00:01.00]缓存中的同步歌词", "timing": "lrc"}})); err != nil {
		t.Fatal(err)
	}
	body := want(t, f.request(t, "GET", "/api/v1/media-songs/song-a/text-tracks", nil, "", true, false), 200)
	items := array(body, "items")
	if len(items) != 1 || str(items[0].(map[string]any), "format") != "lrc" || str(items[0].(map[string]any), "text") != "[00:01.00]缓存中的同步歌词" {
		t.Fatal(body)
	}
}

func TestAdminTextTracksRejectUnreadableOrEscapingLyrics(t *testing.T) {
	f, path := textFixture(t)
	endpoint := "/api/v1/media-songs/song-a/text-tracks"
	for _, content := range [][]byte{{0xff}, []byte(strings.Repeat("x", textLimit+1))} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		body := want(t, f.request(t, "GET", endpoint, nil, "", true, false), 200)
		if len(array(body, "items")) != 0 || len(array(body, "warnings")) != 1 {
			t.Fatal(body)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	body := want(t, f.request(t, "GET", endpoint, nil, "", true, false), 200)
	if len(array(body, "items")) != 0 || len(array(body, "warnings")) != 0 {
		t.Fatal(body)
	}
	for _, relative := range []string{"../outside.mp3", "/outside.mp3"} {
		f.lib.songs[0]["relativePath"] = relative
		want(t, f.request(t, "GET", endpoint, nil, "", true, false), 400)
	}
}
