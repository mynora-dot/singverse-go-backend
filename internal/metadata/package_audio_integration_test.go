package metadata

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/compute"
	"github.com/mynora-dot/singverse-go-backend/internal/execution"
	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/metastore"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestV4AudioTitlesFromExportToVerifiedWebDAVRoles(t *testing.T) {
	ctx := t.Context()
	sender := setup(t)
	source, ids := sender.local(t, "song.mkv")
	media := ids[0]
	streams := []any{
		map[string]any{"index": 0, "codec_type": "video"},
		map[string]any{"index": 1, "codec_type": "audio", "title": "伴奏"},
		map[string]any{"index": 2, "codec_type": "audio", "title": "原唱"},
	}
	err := store.WithinTx(ctx, sender.db, func(tx store.DBTX) error {
		_, _, err := sender.lib.Formal().Observe(ctx, tx, media, metastore.Evidence{
			Origin: metastandard.Origin{Kind: "FILE"},
			Fields: map[string]any{"durationMs": 120000, "observation": map[string]any{"streams": streams}},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	export := exportJob(t, sender, source, nil)
	download := httptest.NewRecorder()
	sender.router.ServeHTTP(download, httptest.NewRequest("GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+text(export["id"])+"/download", nil))
	if download.Code != http.StatusOK {
		t.Fatal(download.Code)
	}

	receiver := setup(t)
	cfg := receiver.cfg
	cfg.AudioDerivedPath, cfg.TranscodePath, cfg.SongCachePath = t.TempDir(), t.TempDir(), t.TempDir()
	node, err := compute.New(receiver.db, "sqlite", cfg, fixtureCipher{}, receiver.s.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(node.Close)
	audio, err := execution.New(receiver.db, "sqlite", cfg, fixtureCipher{}, receiver.lib, node, receiver.s.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(audio.Close)
	audio.Register(receiver.router.Group("/api/v1"))

	var listings, other atomic.Int32
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" || r.URL.Path != "/dav/" {
			other.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		listings.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML("/dav/")+`<d:response><d:href>/dav/song.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>5</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	t.Cleanup(dav.Close)
	target := request(t, receiver.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "receiver", "location": dav.URL + "/dav/"}, 201)
	targetID := text(target["id"])
	job := applyWorkflow(t, receiver, targetID, download.Body.Bytes(), "VERIFYING")
	rolesURL := "/api/v1/media-songs/" + media + "/audio-roles"
	status := request(t, receiver.router, "GET", rolesURL, nil, 200)
	hint, _ := status["packageRoleHint"].(map[string]any)
	if status["state"] != "UNKNOWN" || status["originalId"] != nil || status["backingId"] != nil ||
		hint["basis"] != "STREAM_TITLES" || hint["verification"] != "PENDING" || hint["originalId"] != "stream:2" || hint["backingId"] != "stream:1" ||
		listings.Load() != 0 || other.Load() != 0 {
		t.Fatalf("offline import: status=%v directory requests=%d other requests=%d", status, listings.Load(), other.Load())
	}
	verified := importStep(t, receiver, targetID, text(job["id"]), "SUCCEEDED")
	if number(verified["presentCount"]) != 1 || number(verified["pathOnlyCount"]) != 0 {
		t.Fatal(verified)
	}
	status = request(t, receiver.router, "GET", rolesURL, nil, 200)
	if status["state"] != "CONFIRMED" || status["originalId"] != "stream:2" || status["backingId"] != "stream:1" || status["receiverProbed"] != false ||
		status["reuseBasis"] != "PATH_AND_SIZE" || listings.Load() != 1 || other.Load() != 0 {
		t.Fatalf("directory verification: status=%v directory requests=%d other requests=%d", status, listings.Load(), other.Load())
	}
	var jobs int
	if err := receiver.db.Get(&jobs, "SELECT COUNT(*) FROM media_audio_job WHERE song_id=?", media); err != nil || jobs != 0 {
		t.Fatalf("audio jobs=%d error=%v", jobs, err)
	}
}
