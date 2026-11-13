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
