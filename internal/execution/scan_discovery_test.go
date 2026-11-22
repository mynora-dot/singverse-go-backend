package execution

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/background"
	"github.com/mynora-dot/singverse-go-backend/internal/library"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestScanDiscoveryContinuesWhenNodeRecovers(t *testing.T) {
	var online atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/internal/v1/status":
			if !online.Load() {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, `{"capabilities":["media-probe"]}`)
		case r.URL.Path == "/internal/v1/audio-roles/tasks":
			io.WriteString(w, `{"state":"ACCEPTED"}`)
		default:
			json.NewEncoder(w).Encode(map[string]any{"state": "SUCCEEDED", "result": map[string]any{"duration": 120.25, "streams": audioStreams(1)}})
		}
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	bg, err := background.New(f.db, "sqlite", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bg.Close() })
	f.s.SetProjector(bg.Project)
	f.s.SetAudioScopes(bg)
	ctx := t.Context()
	batch, err := bg.StartLocal(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.WithinTx(ctx, f.db, func(tx store.DBTX) error { return bg.RegisterScanScope(ctx, tx, batch, f.source) }); err != nil {
		t.Fatal(err)
	}
	if err = bg.FinishLocal(ctx, batch, &library.Scan{TotalSongs: 1}, nil); err != nil {
		t.Fatal(err)
	}
	f.s.discoverAudio(ctx)
	var reason string
	if err = f.db.QueryRowContext(ctx, `SELECT reason FROM background_scan_scope WHERE batch_id=?`, batch).Scan(&reason); err != nil || reason != "等待计算节点恢复后继续发现" {
		t.Fatal("offline node lost the durable wait", reason, err)
	}
	online.Store(true)
	// Both cursors may need to wrap after an unavailable-node pass.
	for range 3 {
		f.s.discoverAudio(ctx)
	}
	var jobs int
	if err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_audio_job WHERE song_id=?`, f.song).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("node recovery did not admit exactly one probe", jobs, err)
	}
	f.s.tickAudio(ctx)
	f.s.tickAudio(ctx)
	for range 3 {
		f.s.discoverAudio(ctx)
	}
	var status, counts string
	if err = f.db.QueryRowContext(ctx, `SELECT batch_status,batch_counts_json FROM background_task WHERE id=?`, batch).Scan(&status, &counts); err != nil || status != "COMPLETED" || integer(decoded(counts)["completed"]) != 1 {
		t.Fatal("scan did not settle after automatic audio processing", status, counts, err)
	}
}

func TestImportedWebDAVAudioDiscoveryRequiresLibraryScan(t *testing.T) {
	var calls atomic.Int64
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"capabilities":["media-probe","audio-role-fast"]}`)
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	ctx := t.Context()
	if _, err := f.db.Exec("UPDATE media_source SET source_type='WEBDAV' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO media_shared_song(song_id,source_id,collection_id,snapshot_json,updated_at) VALUES(?,?,?,'{}','now')", f.song, f.source, uuid()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO webdav_resource(song_id,source_id,binding,relative_path,path_hash,task_id,last_seen_at,state) VALUES(?,?,?,'歌曲.mp4',?,?,'now','IMPORTED')", f.song, f.source, hash("binding"), hash("歌曲.mp4"), uuid()); err != nil {
		t.Fatal(err)
	}
	bg, err := background.New(f.db, "sqlite", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bg.Close() })
	f.s.SetProjector(bg.Project)
	f.s.SetAudioScopes(bg)
	var batches []string
	song := mustSong(t, f)
	if err = store.WithinTx(ctx, f.db, func(tx store.DBTX) error {
		var e error
		batches, e = bg.AutomaticOrigin(ctx, tx, uuid(), f.song, contentFingerprint(song))
		return e
	}); err != nil || len(batches) != 1 {
		t.Fatal(err)
	}
	for _, state := range []string{"PENDING", "MISSING", "UNREACHABLE", "MISMATCH", "PRESENT"} {
		if _, err = f.db.Exec("UPDATE media_shared_song SET verification=? WHERE song_id=?", state, f.song); err != nil {
			t.Fatal(err)
		}
		candidates, err := f.s.maps(ctx, f.db, "audio-candidates", map[string]any{"cursor": ""})
		if err != nil || len(candidates) != 0 {
			t.Fatalf("global discovery included %s import: %v %v", state, candidates, err)
		}
		f.s.discoverAudio(ctx)
		if err = f.s.discoverSong(ctx, f.song, map[string]any{"capabilities": []any{"media-probe"}}); err != nil {
			t.Fatal(err)
		}
	}
	var jobs int
	if err = f.db.Get(&jobs, "SELECT COUNT(*) FROM media_audio_job"); err != nil || jobs != 0 || calls.Load() != 0 {
		t.Fatal("package directory verification triggered automatic processing", jobs, calls.Load(), err)
	}
	var state string
	if err = f.db.Get(&state, "SELECT state FROM background_scan_scope WHERE batch_id=?", batches[0]); err != nil || state != "SKIPPED" {
		t.Fatal("discovery scope not resolved", state, err)
	}
	// Recheck in the submission transaction as a caller might have selected
	// this song for discovery before a package import changed its binding.
	if _, err := f.s.submitAudio(ctx, f.song, "PROBE", "", nil, nil, true); err == nil {
		t.Fatal("automatic submission admitted a package-only path")
	} else {
		requireErrorStatus(t, err, 409)
	}
	if _, err = f.db.Exec("UPDATE webdav_resource SET last_scan_id=? WHERE song_id=?", uuid(), f.song); err != nil {
		t.Fatal(err)
	}
	f.s.discoverAudio(ctx)
	if err = f.db.Get(&jobs, "SELECT COUNT(*) FROM media_audio_job WHERE kind='PROBE'"); err != nil || jobs != 1 {
		t.Fatal("independent WebDAV scan did not resume normal discovery", jobs, err)
	}
}

func TestImportedWebDAVAllowsExplicitAudioProbe(t *testing.T) {
	f := executionFixture(t, "http://127.0.0.1:18081")
	if _, err := f.db.Exec("UPDATE media_source SET source_type='WEBDAV' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO media_shared_song(song_id,source_id,collection_id,snapshot_json,updated_at) VALUES(?,?,?,'{}','now')", f.song, f.source, uuid()); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.SubmitAudio(t.Context(), f.song, "PROBE", "", nil)
	if err != nil || str(job, "state") != "QUEUED" {
		t.Fatal("explicit probe was blocked", job, err)
	}
}
