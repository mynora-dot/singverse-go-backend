package execution

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func savedVersion(t *testing.T, f *execFixture, kind, state, recipe string, stamp int64) map[string]any {
	t.Helper()
	song, err := f.s.song(t.Context(), f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"id": uuid(), "song_id": f.song, "kind": kind, "state": state, "fingerprint": contentFingerprint(song), "node_origin": "", "backend": "", "recipe_json": recipe, "created_at": stamp, "updated_at": stamp}
	if _, err = f.s.exec(t.Context(), f.db, "audio-insert-job", job); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(f.cfg.AudioDerivedPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.s.audioPath(job), []byte("derived-"+str(job, "id")), 0600); err != nil {
		t.Fatal(err)
	}
	return job
}
func deleteVersionHTTP(t *testing.T, f *execFixture, song, id string, want int) {
	t.Helper()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/media-songs/"+song+"/accompaniment/"+id, nil))
	if w.Code != want {
		t.Fatalf("DELETE %s: %d %s, want %d", id, w.Code, w.Body.String(), want)
	}
}
func TestDeleteAudioPreservesCurrentSourceAndPendingCleanup(t *testing.T) {
	f := executionFixture(t, "")
	old := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"separationProfile":"AI_CLEAN","outputFormat":"aac"}`, 10)
	active := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"separationProfile":"DSP_FAST","outputFormat":"aac"}`, 20)
	preview := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"separationProfile":"DSP_FAST","preview":true,"outputFormat":"aac"}`, 30)
	flac := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"separationProfile":"LEGACY_DEMUCS","outputFormat":"flac"}`, 40)
	if _, err := f.db.Exec("UPDATE media_audio_job SET result_json=? WHERE id=?", `{"cleanupState":"PENDING","cleanupError":"retry"}`, str(old, "id")); err != nil {
		t.Fatal(err)
	}
	// No explicit selection: the implicit current AAC version is also protected.
	deleteVersionHTTP(t, f, f.song, str(active, "id"), 409)
	deleteVersionHTTP(t, f, uuid(), str(old, "id"), 404)
	deleteVersionHTTP(t, f, f.song, "invalid", 404)
	deleteVersionHTTP(t, f, f.song, str(old, "id"), 204)
	deleteVersionHTTP(t, f, f.song, str(old, "id"), 204)
	if _, err := os.Stat(f.s.audioPath(old)); !os.IsNotExist(err) {
		t.Fatalf("derived file remains: %v", err)
	}
	status, err := f.s.audioStatus(t.Context(), f.song)
	if err != nil {
		t.Fatal(err)
	}
	if str(obj(status["backingResource"]), "jobId") != str(active, "id") || len(array(status["versions"])) != 3 {
		t.Fatal(status)
	}
	retained, err := f.s.requireAudio(t.Context(), f.db, f.song, str(old, "id"))
	if err != nil || str(retained, "state") != "SUCCEEDED" || str(decoded(retained["result_json"]), "cleanupState") != "PENDING" {
		t.Fatal("cleanup/audit record lost", retained, err)
	}
	if _, err = f.s.activateAudio(t.Context(), f.song, str(old, "id")); err == nil {
		t.Fatal("deleted version activated")
	}
	for _, job := range []map[string]any{active, preview, flac} {
		if !regular(f.s.audioPath(job)) {
			t.Fatal("other version removed", job)
		}
	}
	source, err := f.s.song(t.Context(), f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(filepath.Join(str(source, "location"), str(source, "relative_path")))
	if err != nil || !bytes.Equal(media, f.content) {
		t.Fatal("source changed", err)
	}
	deleteVersionHTTP(t, f, f.song, str(preview, "id"), 204)
	deleteVersionHTTP(t, f, f.song, str(flac, "id"), 204)
	// Remaining current output is still downloadable through the real route.
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/media-songs/"+f.song+"/accompaniment/"+str(active, "id"), nil))
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestDeleteAudioRequiresCompletedDerivedOutputAndProtectsSwitchedSelection(t *testing.T) {
	f := executionFixture(t, "")
	first := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"outputFormat":"aac"}`, 1)
	second := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"outputFormat":"aac"}`, 2)
	probe := savedVersion(t, f, "PROBE", "SUCCEEDED", `{}`, 3)
	running := savedVersion(t, f, "SEPARATE", "RUNNING", `{}`, 4)
	deleteVersionHTTP(t, f, f.song, str(probe, "id"), 422)
	deleteVersionHTTP(t, f, f.song, str(running, "id"), 409)
	if _, err := f.s.activateAudio(t.Context(), f.song, str(first, "id")); err != nil {
		t.Fatal(err)
	}
	deleteVersionHTTP(t, f, f.song, str(first, "id"), 409)
	deleteVersionHTTP(t, f, f.song, str(second, "id"), 204)
	if !regular(f.s.audioPath(first)) || !regular(f.s.audioPath(probe)) || !regular(f.s.audioPath(running)) {
		t.Fatal("protected file removed")
	}
}
func TestAudioVersionsRemainVisibleBeyondRecentProcessingHistory(t *testing.T) {
	f := executionFixture(t, "")
	oldest := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"outputFormat":"aac"}`, 1)
	for i := int64(2); i <= 25; i++ {
		savedVersion(t, f, "PROBE", "SUCCEEDED", `{}`, i)
	}
	status, err := f.s.audioStatus(t.Context(), f.song)
	if err != nil {
		t.Fatal(err)
	}
	if len(array(status["versions"])) != 1 || str(obj(status["backingResource"]), "jobId") != str(oldest, "id") {
		t.Fatal("older output hidden", status)
	}
	deleteVersionHTTP(t, f, f.song, str(oldest, "id"), 409)
}

func TestDeleteAndActivateAudioSerialize(t *testing.T) {
	f := executionFixture(t, "")
	candidate := savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"outputFormat":"aac"}`, 1)
	savedVersion(t, f, "SEPARATE", "SUCCEEDED", `{"outputFormat":"aac"}`, 2)
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; _, err := f.s.activateAudio(t.Context(), f.song, str(candidate, "id")); done <- err }()
	go func() { <-start; done <- f.s.deleteAudio(t.Context(), f.song, str(candidate, "id")) }()
	close(start)
	first, second := <-done, <-done
	if first == nil && second == nil {
		t.Fatal("both deletion and activation succeeded")
	}
	if first != nil && second != nil {
		t.Fatal("both operations failed", first, second)
	}
	status, err := f.s.audioStatus(t.Context(), f.song)
	if err != nil {
		t.Fatal(err)
	}
	id := str(obj(status["backingResource"]), "jobId")
	row, err := f.s.requireAudio(t.Context(), f.db, f.song, id)
	if err != nil || !regular(f.s.audioPath(row)) {
		t.Fatal("selected version has no file", id, err)
	}
}
