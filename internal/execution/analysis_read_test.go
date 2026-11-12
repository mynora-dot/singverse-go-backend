package execution

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func analysisFixture(t *testing.T) (*execFixture, []byte) {
	t.Helper()
	f := executionFixture(t, "")
	content := bytes.Repeat([]byte("0123456789abcdef"), int(originalBlockBytes/16)*3+4)
	if _, err := f.db.Exec("UPDATE media_source SET source_type='WEBDAV',location='http://127.0.0.1/media/' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE media_song SET file_size=? WHERE id=?", len(content), f.song); err != nil {
		t.Fatal(err)
	}
	return f, content
}

func analysisJob(t *testing.T, f *execFixture, kind string, automatic bool) map[string]any {
	t.Helper()
	song, err := f.s.song(t.Context(), f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	job := map[string]any{"id": uuid(), "song_id": f.song, "kind": kind, "state": "RUNNING", "fingerprint": contentFingerprint(song), "node_origin": "http://127.0.0.1:18080", "backend": "", "recipe_json": "{}", "created_at": now(), "updated_at": now()}
	err = store.WithinTx(t.Context(), f.db, func(tx store.DBTX) error {
		if _, e := f.s.exec(t.Context(), tx, "audio-insert-job", job); e != nil {
			return e
		}
		return f.s.bindAnalysisRead(t.Context(), tx, job, automatic)
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func analysisRequest(t *testing.T, f *execFixture, session, value string, fetch func(http.ResponseWriter, *http.Request) error, guard func() error) (*httptest.ResponseRecorder, error) {
	t.Helper()
	r := httptest.NewRequest("GET", "/input", nil)
	r.Header.Set("Range", value)
	w := httptest.NewRecorder()
	handled, err := f.s.ServeCachedOriginal(analysisContext(t.Context(), session), w, r, f.song, fetch, guard)
	if !handled {
		t.Fatal("analysis read escaped the bounded path")
	}
	return w, err
}

func TestAnalysisReadsNeverPopulateOriginalAndReuseOnlyExistingBlocks(t *testing.T) {
	f, content := analysisFixture(t)
	job := analysisJob(t, f, "PROBE", false)
	session := str(job, "read_session_id")
	ranges := []string{}
	fetch := func(w http.ResponseWriter, r *http.Request) error {
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "song.mp4", time.Time{}, bytes.NewReader(content))
		return nil
	}
	w, err := analysisRequest(t, f, session, "bytes=0-31", fetch, nil)
	if err != nil || w.Code != 206 || !bytes.Equal(w.Body.Bytes(), content[:32]) {
		t.Fatal(w.Code, err)
	}
	entries, _ := f.s.maps(t.Context(), f.db, "cache-entries", nil)
	if len(entries) != 0 || len(ranges) != 1 || ranges[0] != "bytes=0-31" {
		t.Fatal("analysis created a playback entry or expanded the read", entries, ranges)
	}
	// Normal playback still creates and retains its full original block.
	r := httptest.NewRequest("GET", "/play", nil)
	r.Header.Set("Range", "bytes=0-31")
	if handled, e := f.s.ServeCachedOriginal(t.Context(), httptest.NewRecorder(), r, f.song, fetch, nil); e != nil || !handled {
		t.Fatal(handled, e)
	}
	entries, _ = f.s.maps(t.Context(), f.db, "cache-entries", nil)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	before := encoded(entries[0])
	folder, _ := f.s.cacheFolder(str(entries[0], "id"))
	ranges = nil
	value := fmt.Sprintf("bytes=%d-%d", originalBlockBytes-16, originalBlockBytes+31)
	w, err = analysisRequest(t, f, session, value, fetch, nil)
	if err != nil || !bytes.Equal(w.Body.Bytes(), content[originalBlockBytes-16:originalBlockBytes+32]) {
		t.Fatal(err, w.Body.Len())
	}
	if len(ranges) != 1 || ranges[0] != fmt.Sprintf("bytes=%d-%d", originalBlockBytes, originalBlockBytes+31) {
		t.Fatal("existing block was downloaded again or missing block expanded", ranges)
	}
	entries, _ = f.s.maps(t.Context(), f.db, "cache-entries", nil)
	if len(entries) != 1 || encoded(entries[0]) != before {
		t.Fatal("analysis altered playback cache retention or accounting", entries)
	}
	if _, err = os.Stat(filepath.Join(folder, "original_1.block")); !os.IsNotExist(err) {
		t.Fatal("analysis persisted a missing playback block", err)
	}
	w, err = analysisRequest(t, f, session, "bytes=0-31", fetch, func() error { return fail(401, "revoked") })
	requireErrorStatus(t, err, 401)
	if w.Body.Len() != 0 {
		t.Fatal("revoked analysis returned an existing block")
	}
	state, _ := f.s.one(t.Context(), f.db, "analysis-read-find", map[string]any{"id": session})
	if integer(state["used_bytes"]) != 64 || integer(state["request_count"]) != 2 {
		t.Fatal("cache hits charged remote budget", state)
	}
}

func TestAnalysisReadBudgetIsDurableAndAutomaticFastSharesProbe(t *testing.T) {
	f, content := analysisFixture(t)
	probe := analysisJob(t, f, "PROBE", false)
	session := str(probe, "read_session_id")
	if _, err := f.db.Exec("UPDATE media_analysis_read SET max_bytes=16,max_requests=1 WHERE id=?", session); err != nil {
		t.Fatal(err)
	}
	fetch := func(w http.ResponseWriter, r *http.Request) error {
		http.ServeContent(w, r, "song.mp4", time.Time{}, bytes.NewReader(content))
		return nil
	}
	if _, err := analysisRequest(t, f, session, "bytes=0-7", fetch, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE media_audio_job SET state='SUCCEEDED' WHERE id=?", probe["id"]); err != nil {
		t.Fatal(err)
	}
	fast := analysisJob(t, f, "FAST", true)
	if str(fast, "read_session_id") != session {
		t.Fatal("automatic FAST reset the PROBE budget", fast)
	}
	// Re-loading a job/session uses the durable counters, independent of callback state.
	loaded, err := f.s.one(t.Context(), f.db, "audio-job", map[string]any{"id": fast["id"]})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := f.s.analysisSession(context.Background(), loaded)
	if err != nil || reloaded != session {
		t.Fatal(reloaded, err)
	}
	w, err := analysisRequest(t, f, reloaded, "bytes=8-15", fetch, nil)
	requireErrorStatus(t, err, 413)
	if w.Body.Len() != 0 {
		t.Fatal("budget failure leaked partial output")
	}
	manual := analysisJob(t, f, "FAST", false)
	if str(manual, "read_session_id") == session {
		t.Fatal("explicit retry did not receive a new budget")
	}
}

func TestAnalysisReadRejectsUnboundedFallbackAndBindingChanges(t *testing.T) {
	f, _ := analysisFixture(t)
	job := analysisJob(t, f, "PROBE", false)
	session := str(job, "read_session_id")
	for _, value := range []string{"", "bytes=0-", "bytes=-10", "bytes=0-1,4-5", "bytes=0-1048576", "bytes=9223372036854775808-9223372036854775809"} {
		w, err := analysisRequest(t, f, session, value, func(http.ResponseWriter, *http.Request) error { t.Fatal("invalid range fetched media"); return nil }, nil)
		requireErrorStatus(t, err, 400)
		if w.Body.Len() != 0 {
			t.Fatal(value)
		}
	}
	w, err := analysisRequest(t, f, session, "bytes=0-7", func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(200)
		_, e := w.Write([]byte("12345678"))
		return e
	}, nil)
	requireErrorStatus(t, err, 422)
	if w.Body.Len() != 0 {
		t.Fatal("non-range source was streamed")
	}
	if _, err = f.db.Exec("UPDATE media_source SET username='another-account' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	requireErrorStatus(t, f.s.validateAnalysisRead(t.Context(), f.db, job), 409)
	_, err = analysisRequest(t, f, session, "bytes=0-7", func(http.ResponseWriter, *http.Request) error { t.Fatal("old binding read media"); return nil }, nil)
	requireErrorStatus(t, err, 409)
}

func TestAnalysisPinsRemoteVersionAndRejectsDavGenerationChange(t *testing.T) {
	f, content := analysisFixture(t)
	if _, err := f.db.Exec("INSERT INTO webdav_resource(song_id,source_id,binding,relative_path,path_hash,task_id,generation,last_seen_at,file_size) VALUES(?,?,?,'歌曲.mp4',?,?,1,?,?)", f.song, f.source, hash("binding"), hash("歌曲.mp4"), uuid(), time.Now().UTC().Format(time.RFC3339Nano), len(content)); err != nil {
		t.Fatal(err)
	}
	job := analysisJob(t, f, "PROBE", false)
	etag := `"version-1"`
	fetch := func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, "song.mp4", time.Time{}, bytes.NewReader(content))
		return nil
	}
	for _, value := range []string{"bytes=0-7", "bytes=8-15"} {
		if _, err := analysisRequest(t, f, str(job, "read_session_id"), value, fetch, nil); err != nil {
			t.Fatal("identical pinned version rejected", err)
		}
	}
	etag = `"version-2"`
	w, err := analysisRequest(t, f, str(job, "read_session_id"), "bytes=16-23", fetch, nil)
	requireErrorStatus(t, err, 409)
	if w.Body.Len() != 0 {
		t.Fatal("mixed media versions returned")
	}
	if _, err = f.db.Exec("UPDATE webdav_resource SET generation=generation+1 WHERE song_id=?", f.song); err != nil {
		t.Fatal(err)
	}
	requireErrorStatus(t, f.s.validateAnalysisRead(t.Context(), f.db, job), 409)
}

func TestLegacyRunningAnalysisCannotInventBindingSnapshot(t *testing.T) {
	f, _ := analysisFixture(t)
	job := analysisJob(t, f, "PROBE", false)
	if _, err := f.db.Exec("UPDATE media_audio_job SET read_session_id='' WHERE id=?", job["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM media_analysis_read WHERE id=?", job["id"]); err != nil {
		t.Fatal(err)
	}
	job["read_session_id"] = ""
	_, err := f.s.analysisSession(t.Context(), job)
	requireErrorStatus(t, err, 409)
	if _, err = f.db.Exec("UPDATE media_audio_job SET state='QUEUED' WHERE id=?", job["id"]); err != nil {
		t.Fatal(err)
	}
	job["state"] = "QUEUED"
	session, err := f.s.analysisSession(t.Context(), job)
	if err != nil || session != str(job, "id") {
		t.Fatal(session, err)
	}
}
