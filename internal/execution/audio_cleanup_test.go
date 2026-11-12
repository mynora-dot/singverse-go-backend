package execution

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAudioTerminationsRegisterCleanupInSameUpdate(t *testing.T) {
	f, _ := analysisFixture(t)
	for _, state := range []string{"FAILED", "CANCELLED"} {
		t.Run(state, func(t *testing.T) {
			job := analysisJob(t, f, "FAST", false)
			if err := f.s.updateAudio(t.Context(), str(job, "id"), state, map[string]any{}, "stopped"); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": job["id"]})
			if err != nil || str(row, "state") != state || str(row, "cleanup_state") != "CHECK" || decoded(row["result_json"])["cleanupState"] != nil {
				t.Fatal("running termination lost its independent cleanup watch", row, err)
			}
		})
	}
	queued := analysisJob(t, f, "PROBE", false)
	if _, err := f.db.Exec("UPDATE media_audio_job SET state='QUEUED' WHERE id=?", queued["id"]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.updateAudio(t.Context(), str(queued, "id"), "CANCELLED", map[string]any{}, "stopped"); err != nil {
		t.Fatal(err)
	}
	row, _ := f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": queued["id"]})
	if str(row, "cleanup_state") != "" {
		t.Fatal("never submitted work created a cleanup watch", row)
	}
}

func TestAudioCleanupPollingKeepsTerminalResultAndDoesNotRunInference(t *testing.T) {
	var requests atomic.Int64
	var offline atomic.Bool
	var legacy atomic.Bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("cleanup attempted media work or lost authorization", r.Method)
			w.WriteHeader(400)
			return
		}
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		result := map[string]any{"state": "UNKNOWN", "original": "wrong"}
		if !legacy.Load() {
			result["cleanupState"], result["cleanupError"] = "RELEASED", ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "state": "SUCCEEDED", "result": result})
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	job := analysisJob(t, f, "FAST", false)
	id := str(job, "id")
	if _, err := f.db.Exec("UPDATE media_audio_job SET node_origin=? WHERE id=?", node.URL, id); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"state": "CONFIDENT", "original": "stream:1", "modelVersion": "kept", "cleanupState": "PENDING", "cleanupError": "busy"}
	if err := f.s.updateAudio(t.Context(), id, "SUCCEEDED", result, ""); err != nil {
		t.Fatal(err)
	}
	ready := func() {
		if _, err := f.db.Exec("UPDATE media_audio_job SET cleanup_next_at=0 WHERE id=?", id); err != nil {
			t.Fatal(err)
		}
	}
	ready()
	offline.Store(true)
	f.s.pollAudioCleanup(t.Context())
	row, _ := f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": id})
	if str(row, "state") != "SUCCEEDED" || str(row, "cleanup_state") != "PENDING" || encoded(decoded(row["result_json"])) != encoded(result) || integer(row["cleanup_next_at"]) <= now() {
		t.Fatal("node failure altered a completed job or lost retry backoff", row)
	}
	ready()
	offline.Store(false)
	f.s.pollAudioCleanup(t.Context())
	row, _ = f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": id})
	data := decoded(row["result_json"])
	if str(row, "state") != "SUCCEEDED" || str(row, "cleanup_state") != "RELEASED" || str(data, "cleanupState") != "RELEASED" || str(data, "state") != "CONFIDENT" || str(data, "original") != "stream:1" || str(data, "modelVersion") != "kept" {
		t.Fatal("cleanup replaced recognition result", row)
	}
	before := requests.Load()
	f.s.pollAudioCleanup(t.Context())
	if requests.Load() != before {
		t.Fatal("released cleanup was polled again")
	}
	var roles int
	if err := f.db.Get(&roles, "SELECT COUNT(*) FROM media_audio_role WHERE song_id=?", f.song); err != nil || roles != 0 {
		t.Fatal("cleanup applied an audio-role result", roles, err)
	}
	// Cancelling a running job watches cleanup without guessing an external state.
	if _, err := f.db.Exec("UPDATE media_audio_job SET state='CANCELLED',cleanup_state='',result_json='{}' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.watchCancelledAudioCleanup(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	row, _ = f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": id})
	if str(row, "cleanup_state") != "CHECK" || decoded(row["result_json"])["cleanupState"] != nil {
		t.Fatal("unobserved cancel cleanup was reported as known", row)
	}
	ready()
	legacy.Store(true)
	f.s.pollAudioCleanup(t.Context())
	row, _ = f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": id})
	if str(row, "state") != "CANCELLED" || str(row, "cleanup_state") != "" || decoded(row["result_json"])["cleanupState"] != nil {
		t.Fatal("old node cleanup was guessed or polled indefinitely", row)
	}
}
