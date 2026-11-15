package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPartyAudioRolesPreserveBearerAndPlaybackBoundaries(t *testing.T) {
	a := application(t)
	admin := newBusinessClient(t, a)
	admin.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "歌手 - 音轨验收.mp4"), []byte("isolated source"), 0600); err != nil {
		t.Fatal(err)
	}
	source := admin.call("POST", "/api/v1/media-sources", map[string]any{"name": "音轨验收", "type": "LOCAL", "location": directory}, "", 201)
	admin.call("POST", "/api/v1/media-sources/"+stringField(source, "id")+"/scan", map[string]any{}, "", 200)
	songs := admin.call("GET", "/api/v1/media-sources/"+stringField(source, "id")+"/songs", nil, "", 200)
	items, _ := songs["items"].([]any)
	if len(items) != 1 {
		t.Fatal(songs)
	}
	songID := stringField(object(items[0]), "id")
	grant := admin.call("POST", "/api/v1/auth/tv-session", map[string]any{"name": "音轨验收电视"}, "", 200)
	deviceToken := stringField(grant, "token")
	session := admin.call("POST", "/api/v1/tv/sessions", map[string]any{}, deviceToken, 200)
	invite := admin.call("POST", "/api/v1/tv/sessions/"+stringField(session, "id")+"/invitations", map[string]any{}, deviceToken, 200)
	joined := admin.call("POST", "/api/v1/party/join", map[string]any{"invitation": invite["invitation"], "name": "音轨订正亲友"}, "", 200)
	guestToken := stringField(joined, "token")
	// H5 sends credentials: omit. Its explicit participant Bearer must work
	// independently of the temporary account Cookie used to log in the television.
	guest := &businessClient{t: t, origin: admin.origin, client: &http.Client{}}
	partyPath := "/api/v1/party/audio-roles/songs/" + songID
	tvPath := "/api/v1/tv/audio-roles/songs/" + songID
	initial := guest.call("GET", partyPath, nil, guestToken, 200)
	if initial["state"] != "UNKNOWN" || initial["revision"] != float64(0) {
		t.Fatal(initial)
	}
	// The compute observation is an isolated fixture. Role reads, revision checks,
	// correction and persistence below all run through the real application HTTP.
	probeID := requestID()
	if _, err = a.db.Exec(`INSERT INTO media_audio_job(id,song_id,kind,state,fingerprint,node_origin,result_json,error,created_at,updated_at) VALUES(?,?,'PROBE','SUCCEEDED',?,'','{"streams":[{"id":"stream:0","kind":"audio"},{"id":"stream:1","kind":"audio"},{"id":"stream:2","kind":"video"}],"duration":180}','','0','0')`, probeID, songID, initial["fingerprint"]); err != nil {
		t.Fatal(err)
	}
	bindPartyProbeRead(t, a, songID, probeID)
	observed := guest.call("GET", partyPath, nil, guestToken, 200)
	if streams, _ := observed["streams"].([]any); len(streams) != 2 {
		t.Fatal(observed)
	}
	guest.call("PUT", partyPath, map[string]any{"originalId": "stream:0", "backingId": "stream:2", "revision": 0}, guestToken, 409)
	correction := map[string]any{"originalId": "stream:0", "backingId": "stream:1", "revision": 0}
	saved := guest.call("PUT", partyPath, correction, guestToken, 200)
	if saved["revision"] != float64(1) || saved["origin"] != "MANUAL" || saved["originalId"] != "stream:0" || saved["backingId"] != "stream:1" {
		t.Fatal(saved)
	}
	guest.call("PUT", partyPath, correction, guestToken, 409)
	// Java's TV alias also authenticates either participant or device Bearers.
	alias := guest.call("GET", tvPath, nil, guestToken, 200)
	if alias["revision"] != float64(1) || alias["backingId"] != "stream:1" {
		t.Fatal(alias)
	}
	reversed := map[string]any{"originalId": "stream:1", "backingId": "stream:0", "revision": 1}
	guest.call("PUT", tvPath, reversed, guestToken, 200)
	for _, token := range []string{guestToken, deviceToken} {
		current := guest.call("GET", partyPath, nil, token, 200)
		if current["revision"] != float64(2) || current["originalId"] != "stream:1" || current["backingId"] != "stream:0" {
			t.Fatal(current)
		}
	}
	var original, backing string
	var revision int64
	if err = a.db.QueryRow("SELECT original_id,backing_id,revision FROM media_audio_role WHERE song_id=?", songID).Scan(&original, &backing, &revision); err != nil || original != "stream:1" || backing != "stream:0" || revision != 2 {
		t.Fatal("HTTP correction was not persisted", original, backing, revision, err)
	}
	if _, err = a.db.Exec(`UPDATE media_audio_job SET result_json='{"streams":[{"id":"stream:0","kind":"audio"},{"id":"stream:1","kind":"audio"},{"id":"stream:2","kind":"video"},{"id":"stream:3","kind":"audio","title":"粤语原唱"}],"duration":180}' WHERE id=?`, probeID); err != nil {
		t.Fatal(err)
	}
	multiple := guest.call("GET", partyPath, nil, guestToken, 200)
	if streams, _ := multiple["streams"].([]any); len(streams) != 3 {
		t.Fatal("multi-track projection included a video stream", multiple)
	}
	guest.call("PUT", partyPath, map[string]any{"originalId": "stream:3", "backingId": "stream:0", "revision": 2}, guestToken, 200)
	// Shared role authorization does not grant the participant television media
	// access, offline versions, or another actor's transcode lease.
	transcodeID := requestID()
	if _, err = a.db.Exec(`INSERT INTO media_transcode(id,execution_id,song_id,owner_id,purpose,state,fingerprint,node_origin,input_json,target_json,result_json,error,renewed_at,created_at,updated_at) VALUES(?,?,?,?,'REALTIME','FAILED','fixture','','{}','{}','{}','',0,0,0)`, transcodeID, transcodeID, songID, "tv:"+stringField(grant, "deviceId")); err != nil {
		t.Fatal(err)
	}
	guest.call("GET", "/api/v1/tv/transcoding/"+transcodeID, nil, guestToken, 404)
	guest.call("POST", "/api/v1/tv/transcoding/"+transcodeID+"/cancel", map[string]any{}, guestToken, 404)
	guest.call("GET", "/api/v1/tv/transcoding/"+transcodeID+"/artifacts/master.m3u8", nil, guestToken, 404)
	for _, method := range []string{"GET", "HEAD"} {
		guest.call(method, "/api/v1/tv/transcoding/versions/"+transcodeID+"/"+songID, nil, guestToken, 403)
		// Accompaniment has the shared Bearer boundary, then validates its job.
		guest.call(method, tvPath+"/accompaniment/"+probeID, nil, guestToken, 404)
	}
	guest.call("GET", "/api/v1/tv/media/"+songID, nil, guestToken, 403)
	guest.call("POST", "/api/v1/tv/playback-resources", map[string]any{"songId": songID}, guestToken, 403)
	var state string
	if err = a.db.QueryRow("SELECT state FROM media_transcode WHERE id=?", transcodeID).Scan(&state); err != nil || state != "FAILED" {
		t.Fatal("foreign transcode was changed", state, err)
	}
	for _, path := range []string{partyPath, tvPath} {
		admin.call("GET", path, nil, "", 401)
	}
	admin.call("DELETE", "/api/v1/admin/tv-devices/"+stringField(grant, "deviceId"), nil, "", 200)
	for _, path := range []string{partyPath, tvPath} {
		for _, token := range []string{guestToken, deviceToken} {
			guest.call("GET", path, nil, token, 401)
			guest.call("PUT", path, reversed, token, 401)
		}
	}
}

// Current analysis fixtures carry the same source-binding receipt as production probes.
func bindPartyProbeRead(t *testing.T, a *Application, songID, probeID string) {
	t.Helper()
	var err error
	var songRow = map[string]any{}
	rows, err := a.db.Queryx(`SELECT song.*,source.source_type,source.location,source.username,source.password_encrypted FROM media_song song JOIN media_source source ON source.id=song.source_id WHERE song.id=?`, songID)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("fixture song missing")
	}
	if err = rows.MapScan(songRow); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	bindingDigest := func(values ...any) string {
		var b strings.Builder
		for _, v := range values {
			if v == nil {
				b.WriteString("-1:")
			} else {
				text := fmt.Sprint(v)
				fmt.Fprintf(&b, "%d:%s", len(utf16.Encode([]rune(text))), text)
			}
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
	}
	content := bindingDigest("content:v2", songRow["id"], songRow["file_size"], songRow["modified_at"], nil, nil)
	configuration := bindingDigest("source:v2", songRow["source_id"], songRow["source_type"], songRow["location"], songRow["username"], songRow["password_encrypted"])
	readFingerprint := bindingDigest("analysis-read:v1", bindingDigest("playback:v2", content, songRow["relative_path"], configuration), nil, nil)
	if _, err = a.db.Exec(`INSERT INTO media_analysis_read(id,fingerprint,max_bytes,max_requests,max_elapsed_ms,used_bytes,request_count,elapsed_ms,etag,modified,updated_at) VALUES(?,?,1,1,1,0,0,0,'','',0)`, probeID, readFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE media_audio_job SET read_session_id=? WHERE id=?`, probeID, probeID); err != nil {
		t.Fatal(err)
	}
}
