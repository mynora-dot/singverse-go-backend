package app

// Opt-in bridge for a disposable emulator verification, never a production database.
// SINGVERSE_PARTY_VERIFY_DIR contains self-generated MP4s and ffprobe.json.
import (
	"encoding/json"
	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/metastore"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/config"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPartyDeviceVerification(t *testing.T) {
	directory := os.Getenv("SINGVERSE_PARTY_VERIFY_DIR")
	if directory == "" {
		t.Skip("opt-in isolated TV verification")
	}
	root := t.TempDir()
	cfg, err := config.Parse(func(k string) string {
		switch k {
		case "SQLITE_PATH":
			return filepath.Join(root, "party.db")
		case "SINGVERSE_AUTH_KEY_FILE":
			return filepath.Join(root, "master.key")
		case "WEBDAV_METADATA_PATH", "METADATA_CACHE_PATH", "TRANSCODE_VERSION_PATH", "SINGVERSE_SONG_CACHE_PATH", "AUDIO_DERIVED_PATH":
			return filepath.Join(root, k)
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Application{db: testutil.Database(t)}
	services, err := a.initializeServices(cfg, a.db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Router, err = a.newRouter(services, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:5181")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: a.Router, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	jar, _ := cookiejar.New(nil)
	admin := &businessClient{t: t, origin: "http://127.0.0.1:5181", client: &http.Client{Jar: jar}}
	admin.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	source := admin.call("POST", "/api/v1/media-sources", map[string]any{"name": "隔离联动验收", "type": "LOCAL", "location": filepath.Join(directory, "media")}, "", 201)
	admin.call("POST", "/api/v1/media-sources/"+stringField(source, "id")+"/scan", map[string]any{}, "", 200)
	page := admin.call("GET", "/api/v1/media-sources/"+stringField(source, "id")+"/songs", nil, "", 200)
	raw, err := os.ReadFile(filepath.Join(directory, "ffprobe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err = json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	// Observations originate from the local ffprobe of these generated clips.
	normalized := metastandard.NormalizeObservation(probe)
	items := page["items"].([]any)
	for _, rawSong := range items {
		songID := stringField(object(rawSong), "id")
		initial := admin.call("GET", "/api/v1/media-songs/"+songID+"/audio-roles", nil, "", 200)
		probeID := requestID()
		result, _ := json.Marshal(map[string]any{"streams": probe["streams"], "duration": 90})
		if _, err = a.db.Exec(`INSERT INTO media_audio_job(id,song_id,kind,state,fingerprint,node_origin,result_json,error,created_at,updated_at) VALUES(?,?,'PROBE','SUCCEEDED',?,'',?,'','0','0')`, probeID, songID, initial["fingerprint"], string(result)); err != nil {
			t.Fatal(err)
		}
		bindPartyProbeRead(t, a, songID, probeID)
		if _, _, err = services.media.Formal().Observe(t.Context(), a.db, songID, metastore.Evidence{Origin: metastandard.Origin{Kind: "FILE"}, Fields: map[string]any{"observation": normalized, "durationMs": 90000}}); err != nil {
			t.Fatal(err)
		}
		admin.call("PUT", "/api/v1/media-songs/"+songID+"/audio-roles", map[string]any{"originalId": "stream:1", "backingId": "stream:2", "revision": 0}, "", 200)
	}
	services.devices.Invalidate()
	grant := admin.call("POST", "/api/v1/auth/tv-session", map[string]any{"name": "隔离验收电视"}, "", 200)
	session := admin.call("POST", "/api/v1/tv/sessions", map[string]any{}, stringField(grant, "token"), 200)
	invite := admin.call("POST", "/api/v1/tv/sessions/"+stringField(session, "id")+"/invitations", map[string]any{}, stringField(grant, "token"), 200)
	joined := admin.call("POST", "/api/v1/party/join", map[string]any{"invitation": invite["invitation"], "name": "验收亲友"}, "", 200)
	bridge, _ := json.Marshal(map[string]any{"origin": admin.origin, "deviceToken": grant["token"], "credential": joined, "sessionId": session["id"], "songs": items})
	if err = os.WriteFile(filepath.Join(directory, "bridge.json"), bridge, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Isolated party server ready on 127.0.0.1:5181; fixture credentials are in the private temporary directory")
	deadline := time.Now().Add(18 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(directory, "finished")); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("device verification timed out")
}
