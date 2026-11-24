package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

func workflowPackage(t *testing.T, collection string, revision int, entries ...any) []byte {
	t.Helper()
	b, err := packManifest(map[string]any{"format": "singverse-media-index", "version": 3, "collectionId": collection, "collectionRevision": revision, "setId": uuid(), "entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func uploadWorkflow(t *testing.T, f *fixture, source string, data []byte) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs", bytes.NewReader(data)))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func importStep(t *testing.T, f *fixture, source, id, want string) map[string]any {
	t.Helper()
	if err := f.s.ProcessImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id, nil, 200)
	if want == "PREVIEW" && job["applyMode"] != "preview" {
		t.Fatalf("unexpected apply mode: %v", job["applyMode"])
	}
	if text(job["state"]) != want {
		t.Fatalf("wanted %s: %v", want, job)
	}
	return job
}
func applyWorkflow(t *testing.T, f *fixture, source string, data []byte, wantState string) map[string]any {
	t.Helper()
	job := uploadWorkflow(t, f, source, data)
	id := text(job["id"])
	importStep(t, f, source, id, "PREVIEW")
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/apply", nil, 200)
	if wantState == "SUCCEEDED" {
		importStep(t, f, source, id, "VERIFYING")
	}
	return importStep(t, f, source, id, wantState)
}
func TestWorkflowMergesExistingUpdatesAndProtectsLocalEdits(t *testing.T) {
	exerciseWorkflowMerge(t, setup(t))
}

func TestAutoImportResumesAfterUploadAndOnlyListsPackageDirectories(t *testing.T) {
	exerciseAutoImport(t, setup(t))
}

func TestMySQLAutoImportResumesAfterUploadAndOnlyListsPackageDirectories(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseAutoImport(t, setupWithDatabase(t, db, "mysql", cfg))
}

func exerciseAutoImport(t *testing.T, f *fixture) {
	t.Helper()
	var good, badDir atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" {
			t.Errorf("unexpected media request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
			return
		}
		files := []string{"one.mkv", "two.mkv"}
		switch r.URL.Path {
		case "/dav/good/":
			good.Add(1)
		case "/dav/bad/":
			badDir.Add(1)
			files = []string{"changed.mkv"}
		default:
			t.Errorf("unexpected directory: %s", r.URL.Path)
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path))
		for _, name := range files {
			fmt.Fprintf(w, `<d:response><d:href>%s%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>6</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, r.URL.Path, name)
		}
		fmt.Fprint(w, "</d:multistatus>")
	}))
	defer remote.Close()
	source := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "自动导入", "location": remote.URL + "/dav/"}, 201)
	sourceID := text(source["id"])
	base := "/api/v1/media-sources/" + sourceID + "/embedded-metadata/import-jobs"
	request(t, f.router, "POST", base+"?applyMode=typo", nil, 400)
	data := workflowPackage(t, uuid(), 1, entry("good/one.mkv", 6), entry("good/two.mkv", 6), entry("bad/missing.mkv", 6), entry("bad/changed.mkv", 7))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("POST", base+"?applyMode=auto", bytes.NewReader(data)))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	job := request(t, f.router, "GET", base+"/latest", nil, 200)
	if job["applyMode"] != "auto" || job["state"] != "PARSING" || job["preview"] != nil {
		t.Fatal(job)
	}
	id := text(job["id"])
	request(t, f.router, "POST", base+"?applyMode=auto", nil, 409)
	summaryURL := "/api/v1/media-sources/" + sourceID + "/embedded-metadata"
	if summary := request(t, f.router, "GET", summaryURL, nil, 200); summary["importProcessing"] != true {
		t.Fatal("source must refresh while the import is running", summary)
	}
	// A fresh service must advance the persisted intent without a browser calling /apply.
	restarted, err := New(f.db, f.s.dialect, f.cfg, fixtureCipher{}, f.lib, f.s.log)
	if err != nil {
		t.Fatal(err)
	}
	restarted.cancel()
	restarted.wg.Wait()
	defer restarted.Close()
	worker := *f
	worker.s = restarted
	importStep(t, &worker, sourceID, id, "QUEUED")
	importStep(t, &worker, sourceID, id, "VERIFYING")
	if good.Load()+badDir.Load() != 0 {
		t.Fatal("received a directory request before verification")
	}
	job = importStep(t, &worker, sourceID, id, "SUCCEEDED")
	if good.Load() != 1 || badDir.Load() != 1 || number(job["addedCount"]) != 4 || number(job["missingCount"]) != 1 || number(job["mismatchCount"]) != 1 {
		t.Fatal(job, good.Load(), badDir.Load())
	}
	if summary := request(t, f.router, "GET", summaryURL, nil, 200); summary["importProcessing"] != false {
		t.Fatal("finished import must stop source polling", summary)
	}
	quality, err := f.lib.SourceQuality(context.Background(), f.db, sourceID)
	if err != nil || quality["missingFiles"] != 1 || quality["sizeMismatches"] != 1 {
		t.Fatal(quality, err)
	}
	request(t, f.router, "POST", base+"/"+id+"/retry", nil, 200)
	importStep(t, &worker, sourceID, id, "SUCCEEDED")
	if good.Load() != 1 || badDir.Load() != 2 {
		t.Fatal("retry directory counts", good.Load(), badDir.Load())
	}
}
func TestMySQLWorkflowMergesExistingUpdatesAndProtectsLocalEdits(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseWorkflowMerge(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseWorkflowMerge(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	source := newDAV(t, f, "receiver")
	collection := uuid()
	var songID string
	if err := store.WithinTx(ctx, f.db, func(tx store.DBTX) error {
		var err error
		songID, _, err = f.lib.ImportSong(ctx, tx, source, "目录/歌曲 空格.mkv", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first := entry("目录/歌曲 空格.mkv", nil)
	first["metadata"].(map[string]any)["tags"] = map[string]any{"title": "共享歌名", "artists": []any{"共享歌手"}, "album": "首版专辑"}
	data := workflowPackage(t, collection, 1, first)
	job := applyWorkflow(t, f, source, data, "SUCCEEDED")
	if number(job["addedCount"]) != 0 || number(job["updatedCount"]) != 1 {
		t.Fatal(job)
	}
	song, err := f.s.song(ctx, f.db, songID)
	if err != nil || text(current(song)["title"]) != "共享歌名" {
		t.Fatal(song, err)
	}
	detail := request(t, f.router, "GET", "/api/v1/media-songs/"+songID+"/embedded-metadata", nil, 200)
	if detail["state"] != "IMPORTED" {
		t.Fatal(detail)
	}
	// A completed check with missing files still permits a metadata update.
	review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+songID+"/review", nil, 200)
	request(t, f.router, "POST", "/api/v1/metadata/songs/"+songID+"/review/apply", ReviewApply{RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), Fields: []string{"title"}, Edits: map[string]any{"title": "我的订正"}}, 200)
	second := entry("目录/歌曲 空格.mkv", nil)
	second["metadata"].(map[string]any)["tags"] = map[string]any{"title": "共享新歌名", "artists": []any{"共享新歌手"}, "album": ""}
	job = applyWorkflow(t, f, source, workflowPackage(t, collection, 2, second), "SUCCEEDED")
	if number(job["preservedCount"]) != 1 {
		t.Fatal(job)
	}
	song, err = f.s.song(ctx, f.db, songID)
	if err != nil {
		t.Fatal(err)
	}
	values := current(song)
	if text(values["title"]) != "我的订正" || text(array(values["artists"])[0]) != "共享新歌手" || text(values["album"]) != "首版专辑" {
		t.Fatal(values)
	}
	diff := request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+text(job["id"])+"/differences", nil, 200)
	if len(array(diff["items"])) != 1 || number(diff["nextCursor"]) != 1 {
		t.Fatal(diff)
	}
	job = applyWorkflow(t, f, source, workflowPackage(t, collection, 2, second), "SUCCEEDED")
	if number(job["addedCount"]) != 0 {
		t.Fatal("duplicate song", job)
	}
	blank := entry("目录/歌曲 空格.mkv", nil)
	blank["metadata"].(map[string]any)["tags"] = map[string]any{"title": " \t ", "artists": []any{" ", ""}}
	blank["metadata"].(map[string]any)["technical"] = map[string]any{"video": map[string]any{"codec": "hevc"}}
	job = applyWorkflow(t, f, source, workflowPackage(t, collection, 3, blank), "SUCCEEDED")
	song, err = f.s.song(ctx, f.db, songID)
	if err != nil || text(array(current(song)["artists"])[0]) != "共享新歌手" || current(song)["durationMs"] != nil || number(path(song, "metadata", "media", "durationMs")) != 123500 || path(song, "metadata", "media", "observation", "duration") != nil {
		t.Fatal("blank tags or atomic media observation violates ownership", song, err)
	}
	old := uploadWorkflow(t, f, source, data)
	importStep(t, f, source, text(old["id"]), "FAILED")
	songs := request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/songs", nil, 200)
	if number(songs["totalElements"]) != 1 {
		t.Fatal(songs)
	}
}

func TestWorkflowOwnRootVerificationPlaybackAndRebind(t *testing.T) {
	exerciseWorkflowBinding(t, setup(t))
}
func TestMySQLWorkflowOwnRootVerificationPlaybackAndRebind(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseWorkflowBinding(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseWorkflowBinding(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	var propfinds, gets atomic.Int64
	var status atomic.Int64
	status.Store(207)
	root := "/dav/我的 曲库/"
	relative := "华语/歌 手/歌 曲.mkv"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			if r.Header.Get("Depth") != "1" {
				t.Error("expected Depth: 1 for directory verification")
			}
			propfinds.Add(1)
			w.WriteHeader(int(status.Load()))
			if status.Load() != 207 {
				return
			}
			if r.URL.Path == root {
				fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.EscapedPath())
				return
			}
			href := (&url.URL{Path: root + relative}).EscapedPath()
			fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>6</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.EscapedPath(), href)
			return
		}
		gets.Add(1)
		if r.URL.Path != root+relative {
			t.Error("wrong receiver path", r.URL.Path)
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Content-Length", "6")
		fmt.Fprint(w, "media!")
	}))
	defer remote.Close()
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"name": "自己的曲库", "type": "WEBDAV", "location": remote.URL + root}, 201)
	source := text(src["id"])
	embedded := "/api/v1/media-sources/" + source + "/embedded-metadata"
	request(t, f.router, "PUT", embedded, map[string]any{"enabled": true, "accessGroup": "existing-source"}, 200)
	f.s.SetRemoteExtractor(func(context.Context, map[string]any) (map[string]any, error) {
		t.Error("package import scheduled a remote media read")
		return nil, fmt.Errorf("unexpected content extraction")
	})
	item := entry("旧目录/"+relative, 6)
	job := uploadWorkflow(t, f, source, workflowPackage(t, uuid(), 1, item))
	id := text(job["id"])
	importStep(t, f, source, id, "PREVIEW")
	job = request(t, f.router, "PUT", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/mapping", map[string]any{"removePrefix": "旧目录", "addPrefix": ""}, 200)
	sample := array(job["samples"])[0].(map[string]any)
	parsed, err := url.Parse(text(sample["mediaURL"]))
	if err != nil || parsed.Path != root+relative || parsed.Host != strings.TrimPrefix(remote.URL, "http://") {
		t.Fatal(sample, err)
	}
	if propfinds.Load() != 0 || gets.Load() != 0 {
		t.Fatal("preview accessed media")
	}
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/apply", nil, 200)
	job = importStep(t, f, source, id, "VERIFYING")
	if number(job["verifiedCount"]) != 0 || job["inputState"] != "RELEASED" {
		t.Fatal("directory verification should start after metadata is saved", job)
	}
	if err := f.s.ProcessFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RunRemoteNext(ctx); err != nil {
		t.Fatal(err)
	}
	if propfinds.Load() != 0 || gets.Load() != 0 {
		t.Fatal("offline import accessed the receiver's media", propfinds.Load(), gets.Load())
	}
	config := request(t, f.router, "GET", embedded, nil, 200)
	if config["enabled"] != true || config["accessGroup"] != "existing-source" {
		t.Fatal("import changed the existing source configuration", config)
	}
	var songID string
	if err = f.db.Get(&songID, "SELECT id FROM media_song WHERE source_id=?", source); err != nil {
		t.Fatal(err)
	}
	song, _ := f.s.song(ctx, f.db, songID)
	if text(song["mediaVerification"]) != "PENDING" {
		t.Fatal(song)
	}
	status.Store(401)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if number(job["unreachableCount"]) != 1 || number(job["missingCount"]) != 0 || propfinds.Load() != 1 {
		t.Fatal(job)
	}
	if gets.Load() != 0 {
		t.Fatal("verification downloaded media")
	}
	status.Store(207)
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/retry", nil, 200)
	job = importStep(t, f, source, id, "SUCCEEDED")
	if number(job["presentCount"]) != 1 {
		t.Fatal(job)
	}
	if err = f.s.ProcessFiles(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.cfg.MetadataPath, "library", source, songID, "media.strm"))
	if err != nil {
		t.Fatal(err)
	}
	playURL, _ := url.Parse(strings.TrimSpace(string(b)))
	if playURL.Path != root+relative || playURL.Host != parsed.Host {
		t.Fatal("wrong local STRM", playURL)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/media-songs/"+songID+"/video", nil))
	if w.Code != 200 || w.Body.String() != "media!" || gets.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	diagnostic := request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/diagnostics", nil, 200)
	if diagnostic["directory"] != "PRESENT" || diagnostic["media"] != "RANGE_UNSUPPORTED" {
		t.Fatal("directory success must not imply range support", diagnostic)
	}
	newRoot := remote.URL + "/dav/重新绑定/"
	job = request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/rebind", map[string]any{"location": newRoot, "username": "", "password": ""}, 202)
	song, err = f.s.song(ctx, f.db, songID)
	if err != nil || text(song["mediaVerification"]) != "PENDING" || text(current(song)["title"]) == "" {
		t.Fatal(song, err)
	}
	if text(job["state"]) != "VERIFYING" || number(job["totalCount"]) != 1 {
		t.Fatal(job)
	}
	summary := request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata", nil, 200)
	quality := summary["quality"].(map[string]any)
	if number(quality["total"]) != 1 || number(quality["identityComplete"]) != 1 || number(quality["pendingVerification"]) != 1 {
		t.Fatal(quality)
	}
}

func TestWorkflowResumesFullPackageChunks(t *testing.T) {
	f := setup(t)
	source := newDAV(t, f, "checkpoints")
	collection := uuid()
	ctx := context.Background()
	entries := []any{}
	for i := 0; i < 201; i++ {
		entries = append(entries, entry(fmt.Sprintf("a-%03d.mkv", i), nil))
	}
	job := uploadWorkflow(t, f, source, workflowPackage(t, collection, 3, entries...))
	id := text(job["id"])
	importStep(t, f, source, id, "PREVIEW")
	// Fail the second chunk: first 100 outcomes and songs must survive atomically.
	_, err := f.db.Exec("CREATE TRIGGER fail_import BEFORE INSERT ON media_shared_song WHEN (SELECT COUNT(*) FROM media_shared_song)>=100 BEGIN SELECT RAISE(ABORT,'forced failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/apply", nil, 200)
	job = importStep(t, f, source, id, "FAILED")
	if number(job["processedCount"]) != 100 || job["inputState"] != "RETAINED" {
		t.Fatal(job)
	}
	if _, err = os.Stat(importArchive(t, f, id)); err != nil {
		t.Fatal("partially applied import lost its resumable input", err)
	}
	if _, err = f.db.Exec("DROP TRIGGER fail_import"); err != nil {
		t.Fatal(err)
	}
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/import-jobs/"+id+"/retry", nil, 200)
	// A new service instance resumes durable rows after the browser is gone.
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = New(f.db, "sqlite", f.cfg, fixtureCipher{}, f.lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.s.cancel()
	f.s.wg.Wait()
	f.s.ctx, f.s.cancel = context.WithCancel(ctx)
	if err = f.s.ProcessImports(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := f.s.importJob(ctx, source, id)
	if err != nil || number(stored["processedCount"]) != 201 || number(stored["addedCount"]) != 201 || text(stored["state"]) != "VERIFYING" || stored["inputState"] != "RELEASED" {
		t.Fatal(stored, err)
	}
	verified := importStep(t, f, source, id, "SUCCEEDED")
	if number(verified["verifiedCount"]) != 201 {
		t.Fatal(verified)
	}
	var songs int
	if err = f.db.Get(&songs, "SELECT COUNT(*) FROM media_song WHERE source_id=?", source); err != nil || songs != 201 {
		t.Fatal(songs, err)
	}
}

func TestShareImportsRequireFullPackages(t *testing.T) {
	f := setup(t)
	source := newDAV(t, f, "partial")
	collection := uuid()
	baseline := applyWorkflow(t, f, source, workflowPackage(t, collection, 1, entry("a.mkv", nil)), "SUCCEEDED")
	part, err := packManifest(map[string]any{"format": "singverse-media-index", "version": 3, "collectionId": collection, "collectionRevision": 2, "setId": uuid(), "part": 1, "entries": []any{entry("b.mkv", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	legacyDelta, err := packManifest(map[string]any{"format": "singverse-media-index", "version": 3, "collectionId": collection, "collectionRevision": 2, "setId": uuid(), "baseId": baseline["snapshotId"], "entries": []any{entry("b.mkv", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	index := map[string]any{
		"format": "singverse-share-set", "version": 5, "metadataSchema": "1.0", "id": uuid(), "collectionId": collection,
		"collectionRevision": "2", "baseId": baseline["snapshotId"], "total": 0, "records": []string{}, "deletions": []string{}, "files": map[string]any{},
	}
	digest, err := metastandard.DocumentDigest([]byte(encode(index)))
	if err != nil {
		t.Fatal(err)
	}
	index["contentDigest"] = digest
	delta := makeZip(t, []struct {
		name string
		data []byte
	}{{"index.json", []byte(encode(index))}})
	for _, data := range [][]byte{part, legacyDelta, delta} {
		job := uploadWorkflow(t, f, source, data)
		job = importStep(t, f, source, text(job["id"]), "FAILED")
		if job["error"] != fullShareImportMessage || number(job["processedCount"]) != 0 {
			t.Fatal(job)
		}
		if _, err = f.s.ImportStream(t.Context(), source, bytes.NewReader(data)); code(err) != 400 {
			t.Fatal("synchronous import accepted an incomplete package", err)
		}
	}
	var revision int
	if err = f.db.Get(&revision, "SELECT revision FROM media_shared_baseline WHERE source_id=? AND collection_id=?", source, collection); err != nil || revision != 1 {
		t.Fatal("rejected package changed the shared baseline", revision, err)
	}
}

func TestWorkflowPrefixMappingKeepsOtherDirectoriesAndRejectsCollisions(t *testing.T) {
	exerciseWorkflowPrefixMapping(t, setup(t))
}
func TestMySQLWorkflowPrefixMappingKeepsOtherDirectoriesAndRejectsCollisions(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseWorkflowPrefixMapping(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseWorkflowPrefixMapping(t *testing.T, f *fixture) {
	t.Helper()
	source := newDAV(t, f, "mixed-folders")
	job := uploadWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("旧华语/歌曲.mkv", nil), entry("粤语/歌曲.mkv", nil)))
	id := text(job["id"])
	importStep(t, f, source, id, "PREVIEW")
	endpoint := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + id
	job = request(t, f.router, "PUT", endpoint+"/mapping", map[string]any{"removePrefix": "旧华语", "addPrefix": "华语"}, 200)
	samples := array(job["samples"])
	if samples[0].(map[string]any)["relativePath"] != "华语/歌曲.mkv" || samples[1].(map[string]any)["relativePath"] != "粤语/歌曲.mkv" {
		t.Fatal("mapping changed an unrelated folder", job)
	}
	request(t, f.router, "PUT", endpoint+"/mapping", map[string]any{"removePrefix": "旧华语", "addPrefix": "粤语"}, 400)
	request(t, f.router, "PUT", endpoint+"/mapping", map[string]any{"removePrefix": "不存在", "addPrefix": "华语"}, 400)
	job = request(t, f.router, "GET", endpoint, nil, 200)
	if job["addPrefix"] != "华语" || array(job["samples"])[0].(map[string]any)["relativePath"] != "华语/歌曲.mkv" {
		t.Fatal("failed mapping did not roll back", job)
	}
}

func TestArchivedMediaRetainsSavedInformationAndLocalCorrections(t *testing.T) {
	exerciseArchivedInformation(t, setup(t))
}
func TestMySQLArchivedMediaRetainsSavedInformationAndLocalCorrections(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseArchivedInformation(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseArchivedInformation(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	source := newDAV(t, f, "archived-information")
	applyWorkflow(t, f, source, workflowPackage(t, uuid(), 1, entry("共享歌曲.mkv", nil)), "SUCCEEDED")
	var sharedID string
	if err := f.db.Get(&sharedID, "SELECT id FROM media_song WHERE source_id=?", source); err != nil {
		t.Fatal(err)
	}
	var localID, temporaryID string
	if err := store.WithinTx(ctx, f.db, func(tx store.DBTX) error {
		var err error
		localID, _, err = f.lib.ImportSong(ctx, tx, source, "本机整理.mkv", nil)
		if err != nil {
			return err
		}
		temporaryID, _, err = f.lib.ImportSong(ctx, tx, source, "临时索引.mkv", nil)
		if err != nil {
			return err
		}
		return f.s.prepare(ctx, tx, source, true)
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sharedID, localID} {
		review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+id+"/review", nil, 200)
		request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/review/apply", ReviewApply{RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), Fields: []string{"title"}, Edits: map[string]any{"title": "保留我的订正"}}, 200)
	}
	old := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec("UPDATE webdav_resource SET archived_at=? WHERE source_id=?", old, source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM media_song WHERE source_id=?", source); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sharedID, localID} {
		var track string
		var resources int
		if err := f.db.Get(&track, "SELECT track_json FROM song_metadata WHERE song_id=?", id); err != nil || !strings.Contains(track, "保留我的订正") {
			t.Fatal("archival removed a local correction", id, track, err)
		}
		if err := f.db.Get(&resources, "SELECT COUNT(*) FROM webdav_resource WHERE song_id=?", id); err != nil || resources != 1 {
			t.Fatal("archival lost saved media identity", id, resources, err)
		}
	}
	var resources int
	if err := f.db.Get(&resources, "SELECT COUNT(*) FROM webdav_resource WHERE song_id=?", temporaryID); err != nil || resources != 0 {
		t.Fatal("temporary resource retention changed", resources, err)
	}
}

func TestSourceSaveCompatibilityAndDurableAutomaticIngest(t *testing.T) {
	f := setup(t)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "歌手-歌曲.mp3"), []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"name": "旧客户端", "type": "LOCAL", "location": folder}, 201)
	if legacy["initialTaskId"] != nil {
		t.Fatal(legacy)
	}
	request(t, f.router, "GET", "/api/v1/media-sources/"+text(legacy["id"])+"/ingest-tasks/latest", nil, 204)
	request(t, f.router, "DELETE", "/api/v1/media-sources/"+text(legacy["id"]), nil, 204)
	source := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"name": "保存并扫描", "type": "LOCAL", "location": folder, "startScan": true}, 201)
	if text(source["initialTaskId"]) == "" {
		t.Fatal(source)
	}
	f.lib.RunIngest(context.Background())
	job := request(t, f.router, "GET", "/api/v1/media-sources/"+text(source["id"])+"/ingest-tasks/latest", nil, 200)
	if job["state"] != "SUCCEEDED" {
		t.Fatal(job)
	}
	songs := request(t, f.router, "GET", "/api/v1/media-sources/"+text(source["id"])+"/songs", nil, 200)
	if number(songs["totalElements"]) != 1 {
		t.Fatal(songs)
	}
	request(t, f.router, "PUT", "/api/v1/media-sources/"+text(source["id"]), map[string]any{"name": "普通编辑", "type": "LOCAL", "location": folder}, 200)
	var count int
	if err := f.db.Get(&count, "SELECT COUNT(*) FROM media_ingest_request WHERE source_id=?", source["id"]); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" || r.URL.Path != "/dav/华语/" {
			t.Errorf("unexpected import request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer remote.Close()
	dav := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"name": "导入专用", "type": "WEBDAV", "location": remote.URL + "/dav/", "startScan": false, "autoExtractEmbedded": false, "scanEnabled": false}, 201)
	davID := text(dav["id"])
	if dav["initialTaskId"] != nil || dav["nextScanAt"] != nil || dav["scanEnabled"] != false {
		t.Fatal("package creation unexpectedly scheduled a scan", dav)
	}
	summary := request(t, f.router, "GET", "/api/v1/media-sources/"+davID+"/embedded-metadata", nil, 200)
	if summary["enabled"] != false {
		t.Fatal(summary)
	}
	var extractions atomic.Int32
	f.s.SetRemoteExtractor(func(context.Context, map[string]any) (map[string]any, error) {
		extractions.Add(1)
		return nil, fmt.Errorf("imported metadata must not trigger content extraction")
	})
	imported := applyWorkflow(t, f, davID, workflowPackage(t, uuid(), 1, entry("华语/歌 曲.mkv", nil)), "SUCCEEDED")
	if number(imported["addedCount"]) != 1 || number(imported["missingCount"]) != 1 {
		t.Fatal(imported)
	}
	f.lib.RunIngest(context.Background())
	if err := f.s.RunRemoteNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	request(t, f.router, "GET", "/api/v1/media-sources/"+davID+"/ingest-tasks/latest", nil, 204)
	request(t, f.router, "GET", "/api/v1/media-sources/"+davID+"/scan-tasks/latest", nil, 204)
	if extractions.Load() != 0 {
		t.Fatal("package import redundantly read the remote media", extractions.Load())
	}
	songs = request(t, f.router, "GET", "/api/v1/media-sources/"+davID+"/songs", nil, 200)
	if number(songs["totalElements"]) != 1 {
		t.Fatal("package did not populate the library without scanning", songs)
	}
}

func TestSharedFieldsKeepIndependentSeriesOwnershipAndBlankValues(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source := newDAV(t, f, "field-owners")
	a, b := uuid(), uuid()
	build := func(tags map[string]any) map[string]any {
		v := entry("song.mkv", nil)
		v["metadata"].(map[string]any)["tags"] = tags
		return v
	}
	legacy := shareBundle(t, []any{build(map[string]any{"title": "旧包歌名", "artists": []any{"旧包歌手"}})}, nil)
	if _, err := f.s.ImportPart(ctx, source, legacy); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		collection string
		revision   int
		tags       map[string]any
	}{
		{a, 1, map[string]any{"title": "系列A歌名", "album": "专辑A"}},
		{b, 1, map[string]any{"releaseDate": "1999"}},
		{a, 2, map[string]any{"album": "专辑A修订", "releaseDate": "2000", "title": "   "}},
		{b, 2, map[string]any{"releaseDate": "2001", "album": "抢占A专辑"}},
	} {
		if _, err := f.s.ImportPart(ctx, source, workflowPackage(t, step.collection, step.revision, build(step.tags))); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	if err := f.db.Get(&id, "SELECT id FROM media_song WHERE source_id=?", source); err != nil {
		t.Fatal(err)
	}
	song, err := f.s.song(ctx, f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	v := current(song)
	if text(v["title"]) != "旧包歌名" || text(v["album"]) != "专辑A修订" || text(v["releaseDate"]) != "2001" {
		t.Fatal(v)
	}
	meta := metaOf(song)
	if meta == nil || meta.Origins["album"].ExternalID == nil || *meta.Origins["album"].ExternalID != a || meta.Origins["releaseDate"].ExternalID == nil || *meta.Origins["releaseDate"].ExternalID != b {
		t.Fatal(meta)
	}
	snapshot, err := f.s.sharedSnapshot(ctx, f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.cfg.MetadataPath, text(snapshot["lyricsRef"])+".lyrics.json")); err != nil {
		t.Fatal("lyrics were not persisted", err)
	}
}

func TestHistoricalVerificationLeavesChangedSizesPending(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var source string
	collection := uuid()
	duringListing := workflowPackage(t, collection, 3, entry("current/race.mkv", 20))
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "PROPFIND" || r.URL.Path != "/dav/current/" {
			t.Errorf("unexpected request for superseded media %s %s", r.Method, r.URL.Path)
		}
		if _, err := f.s.ImportPart(ctx, source, duringListing); err != nil {
			t.Error(err)
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path))
		for _, name := range []string{"race.mkv", "keep.mkv"} {
			fmt.Fprintf(w, `<d:response><d:href>/dav/current/%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>10</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, name)
		}
		fmt.Fprint(w, "</d:multistatus>")
	}))
	defer remote.Close()
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "old receipts", "location": remote.URL + "/dav/"}, 201)
	source = text(src["id"])
	old := applyWorkflow(t, f, source, workflowPackage(t, collection, 1, entry("old/size.mkv", 10), entry("current/race.mkv", 10), entry("current/keep.mkv", 10)), "VERIFYING")
	// Jobs that completed offline before automatic verification keep their
	// explicit retry entry point, even after a newer snapshot changes a file.
	if _, err := f.db.Exec("UPDATE media_import_job SET state='SUCCEEDED',phase='IMPORTED' WHERE id=?", old["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportPart(ctx, source, workflowPackage(t, collection, 2, entry("old/size.mkv", 20))); err != nil {
		t.Fatal(err)
	}
	importStep(t, f, source, text(old["id"]), "SUCCEEDED")
	base := "/api/v1/media-sources/" + source + "/embedded-metadata/import-jobs/" + text(old["id"])
	request(t, f.router, "POST", base+"/retry", nil, 200)
	result := importStep(t, f, source, text(old["id"]), "SUCCEEDED")
	if requests.Load() != 1 || number(result["verifiedCount"]) != 3 || number(result["presentCount"]) != 1 {
		t.Fatal(result, requests.Load())
	}
	diff := request(t, f.router, "GET", base+"/differences", nil, 200)
	if len(array(diff["items"])) != 2 {
		t.Fatal(diff)
	}
	for _, raw := range array(diff["items"]) {
		item := raw.(map[string]any)
		if item["verification"] != "SUPERSEDED" || text(item["verificationError"]) == "" {
			t.Fatal(item)
		}
		var verification string
		var expected int64
		if err := f.db.QueryRow("SELECT verification,expected_size FROM media_shared_song WHERE song_id=?", item["songId"]).Scan(&verification, &expected); err != nil || verification != "PENDING" || expected != 20 {
			t.Fatal("old receipt updated the current target", verification, expected, err)
		}
		locations, err := f.lib.Formal().Locations(ctx, f.db, text(item["songId"]))
		if err != nil || len(locations) != 1 || locations[0].State != "PENDING" {
			t.Fatal(locations, err)
		}
	}
}

func TestV4RemapDropsOldVerification(t *testing.T) {
	sender := setup(t)
	source, media := sender.local(t, "song.mkv")
	export := exportJob(t, sender, source, nil)
	w := httptest.NewRecorder()
	sender.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+text(export["id"])+"/download", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	f := setup(t)
	var calls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		size := 5
		if r.URL.Path == "/dav/moved/" {
			size = 99
		}
		w.WriteHeader(207)
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:">%s<d:response><d:href>%ssong.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>%d</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, verificationSelfXML(r.URL.Path), r.URL.Path, size)
	}))
	defer remote.Close()
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": "remap", "location": remote.URL + "/dav/"}, 201)
	target := text(src["id"])
	old := applyWorkflow(t, f, target, w.Body.Bytes(), "SUCCEEDED")
	base := "/api/v1/media-sources/" + target + "/embedded-metadata/import-jobs/"
	if number(old["presentCount"]) != 1 {
		t.Fatal(old)
	}

	job := uploadWorkflow(t, f, target, w.Body.Bytes())
	id := text(job["id"])
	importStep(t, f, target, id, "PREVIEW")
	request(t, f.router, "PUT", base+id+"/mapping", map[string]any{"addPrefix": "moved"}, 200)
	request(t, f.router, "POST", base+id+"/apply", nil, 200)
	importStep(t, f, target, id, "VERIFYING")
	moved := importStep(t, f, target, id, "SUCCEEDED")
	if number(moved["mismatchCount"]) != 1 {
		t.Fatal(moved)
	}
	request(t, f.router, "POST", base+text(old["id"])+"/retry", nil, 200)
	old = importStep(t, f, target, text(old["id"]), "SUCCEEDED")
	if calls.Load() != 2 || number(old["presentCount"]) != 0 || array(old["samples"])[0].(map[string]any)["verification"] != "SUPERSEDED" {
		t.Fatal(old, calls.Load())
	}
	ctx := context.Background()
	song, err := f.s.song(ctx, f.db, media[0])
	if err != nil || song["mediaVerification"] != "MISMATCH" || song["relativePath"] != "moved/song.mkv" {
		t.Fatal(song, err)
	}
	loc, err := f.lib.Formal().Locations(ctx, f.db, media[0])
	if err != nil || len(loc) != 1 || loc[0].RelativePath != "moved/song.mkv" || loc[0].State != "MISMATCH" {
		t.Fatal(loc, err)
	}
}
