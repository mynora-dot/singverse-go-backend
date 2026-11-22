package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func localImportSource(t *testing.T, f *fixture) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "song.mkv"), []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	source := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "LOCAL", "name": "import recovery", "location": root}, 201)
	return text(source["id"])
}

func importArchive(t *testing.T, f *fixture, id string) string {
	t.Helper()
	folder, err := f.s.importFolder(id)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(folder, "bundle.zip")
}

func TestImportZIPReleasePreservesDurableRetryInput(t *testing.T) {
	f := setup(t)
	source := localImportSource(t, f)
	job := uploadWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("song.mkv", 5)))
	id := text(job["id"])
	base := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + id
	archive := importArchive(t, f, id)
	job = importStep(t, f, source, id, "PREVIEW")
	if job["inputState"] != "RETAINED" {
		t.Fatal(job)
	}
	// A cancelled preview can be resumed and still has its original input.
	request(t, f.router, "POST", base+"/cancel", nil, 200)
	importStep(t, f, source, id, "CANCELLED")
	if _, err := os.Stat(archive); err != nil {
		t.Fatal("preview input was released", err)
	}
	request(t, f.router, "POST", base+"/retry", nil, 200)
	importStep(t, f, source, id, "PREVIEW")
	request(t, f.router, "POST", base+"/apply", nil, 200)
	job = importStep(t, f, source, id, "VERIFYING")
	if job["inputState"] != "RELEASED" {
		t.Fatal(job)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("applied archive remains", err)
	}
	for _, table := range []string{"media_import_entry", "media_shared_song", "media_shared_baseline"} {
		var count int
		if err := f.db.Get(&count, "SELECT COUNT(*) FROM "+table); err != nil || count != 1 {
			t.Fatal("durable import data was cleared", table, count, err)
		}
	}
	// Directory verification and its explicit retry never require the ZIP.
	request(t, f.router, "POST", base+"/cancel", nil, 200)
	request(t, f.router, "POST", base+"/retry", nil, 200)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if number(job["presentCount"]) != 1 || number(job["pathOnlyCount"]) != 0 || array(job["samples"])[0].(map[string]any)["verificationLevel"] != "PATH_AND_SIZE" {
		t.Fatal(job)
	}
	// Recover a crash after unlink but before the cleanup receipt commits.
	if _, err := f.db.Exec("UPDATE media_import_job SET input_state='PENDING',input_cleanup_after=NULL WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.cleanupImportInputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	job = request(t, f.router, "GET", base, nil, 200)
	if job["inputState"] != "RELEASED" || number(job["addedCount"]) != 1 {
		t.Fatal(job)
	}
}

func TestImportZIPCleanupFailureRetriesWithoutReapplying(t *testing.T) {
	f := setup(t)
	source := localImportSource(t, f)
	job := uploadWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("song.mkv", 5)))
	id := text(job["id"])
	base := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + id
	importStep(t, f, source, id, "PREVIEW")
	archive := importArchive(t, f, id)
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	// A nonempty directory provides a deterministic unlink failure on every OS.
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "locked"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	request(t, f.router, "POST", base+"/apply", nil, 200)
	job = importStep(t, f, source, id, "VERIFYING")
	if job["inputState"] != "PENDING" || text(job["inputCleanupError"]) == "" || text(job["inputCleanupAfter"]) == "" || job["error"] != nil {
		t.Fatal("cleanup failure changed the business outcome", job)
	}
	deferred := text(job["inputCleanupAfter"])
	job = importStep(t, f, source, id, "SUCCEEDED")
	if text(job["inputCleanupAfter"]) != deferred || number(job["addedCount"]) != 1 || number(job["presentCount"]) != 1 {
		t.Fatal("cleanup was retried before its deadline or replayed import", job)
	}
	if err := os.RemoveAll(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE media_import_job SET input_cleanup_after=? WHERE id=?", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.cleanupImportInputs(context.Background()); err != nil {
		t.Fatal(err)
	}
	job = request(t, f.router, "GET", base, nil, 200)
	if job["inputState"] != "RELEASED" || job["inputCleanupError"] != nil || job["inputCleanupAfter"] != nil || number(job["processedCount"]) != 1 {
		t.Fatal(job)
	}
}

func TestVerificationGroupsInterleavedParentPathsOnce(t *testing.T) {
	entries := []map[string]any{}
	for _, name := range []string{"a.mkv", "a/one.mkv", "a/sub/one.mkv", "a/two.mkv", "b.mkv"} {
		entries = append(entries, map[string]any{"relative_path": name})
	}
	groups := groupVerificationEntries(entries)
	if len(groups) != 3 || groups[0].directory != "" || len(groups[0].entries) != 2 || groups[1].directory != "a" || len(groups[1].entries) != 2 || groups[2].directory != "a/sub" {
		t.Fatal(groups)
	}
}

func TestVerificationDirectoryFailureAndUnknownSizeAreDurable(t *testing.T) {
	f := setup(t)
	var callsA, callsB, gets atomic.Int64
	var failB atomic.Bool
	failB.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			gets.Add(1)
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path == "/dav/a/" {
			callsA.Add(1)
			w.WriteHeader(207)
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path)+`<d:response><d:href>/dav/a/one.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>6</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response><d:response><d:href>/dav/a/two.mkv</d:href><d:propstat><d:prop><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
			return
		}
		callsB.Add(1)
		if failB.Load() {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path)+`<d:response><d:href>/dav/b/three.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>6</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer remote.Close()
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "verification boundaries", "location": remote.URL + "/dav/"}, 201)
	source := text(src["id"])
	job := applyWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("a/one.mkv", 6), entry("a/two.mkv", nil), entry("b/three.mkv", 6)), "SUCCEEDED")
	id := text(job["id"])
	base := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + id
	if number(job["presentCount"]) != 2 || number(job["unreachableCount"]) != 1 || number(job["pathOnlyCount"]) != 1 || callsA.Load() != 1 || callsB.Load() != 1 || gets.Load() != 0 {
		t.Fatal(job, callsA.Load(), callsB.Load(), gets.Load())
	}
	importStep(t, f, source, id, "SUCCEEDED")
	if callsA.Load() != 1 || callsB.Load() != 1 {
		t.Fatal("failed directory was automatically retried")
	}
	failB.Store(false)
	request(t, f.router, "POST", base+"/retry", nil, 200)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if number(job["presentCount"]) != 3 || callsA.Load() != 1 || callsB.Load() != 2 {
		t.Fatal("explicit retry rechecked successful entries", job, callsA.Load(), callsB.Load())
	}
	var songID string
	if err := f.db.Get(&songID, "SELECT song_id FROM media_import_entry WHERE job_id=? AND relative_path='a/two.mkv'", id); err != nil {
		t.Fatal(err)
	}
	locations, err := f.lib.Formal().Locations(context.Background(), f.db, songID)
	if err != nil || len(locations) != 1 {
		t.Fatal(locations, err)
	}
	observation := map[string]any{}
	if err = decode(locations[0].Observation, &observation); err != nil || observation["verificationLevel"] != "PATH_ONLY" || observation["sizeVerified"] != false || text(observation["verificationSourceBinding"]) == "" {
		t.Fatal(observation, err)
	}
	if job["verificationSourceBinding"] != nil || job["binding"] != nil {
		t.Fatal("opaque binding leaked into API", job)
	}
	// The URL stayed identical, but another account must reverify old PRESENTs.
	if _, err = f.db.Exec("UPDATE media_source SET username='changed-account' WHERE id=?", source); err != nil {
		t.Fatal(err)
	}
	request(t, f.router, "POST", base+"/retry", nil, 200)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if callsA.Load() != 2 || callsB.Load() != 3 || number(job["presentCount"]) != 3 {
		t.Fatal("changed credentials reused stale observations", job, callsA.Load(), callsB.Load())
	}
}

func TestVerificationRejectsCredentialChangeDuringListing(t *testing.T) {
	f := setup(t)
	var source string
	var changed atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !changed.Swap(true) {
			if _, err := f.db.Exec("UPDATE media_source SET username='another-account' WHERE id=?", source); err != nil {
				t.Error(err)
			}
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path)+`<d:response><d:href>/dav/song.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>5</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer remote.Close()
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "credential race", "location": remote.URL + "/dav/"}, 201)
	source = text(src["id"])
	job := applyWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("song.mkv", 5)), "VERIFYING")
	id := text(job["id"])
	base := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + id
	job = importStep(t, f, source, id, "FAILED")
	if number(job["verifiedCount"]) != 0 || number(job["presentCount"]) != 0 {
		t.Fatal("stale account observation was committed", job)
	}
	request(t, f.router, "POST", base+"/retry", nil, 200)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if number(job["presentCount"]) != 1 || job["inputState"] != "RELEASED" {
		t.Fatal(job)
	}
}

func verificationSelfXML(directory string) string {
	return `<d:response><d:href>` + directory + `</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
}
