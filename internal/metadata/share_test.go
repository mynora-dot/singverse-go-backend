package metadata

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newDAV(t *testing.T, f *fixture, name string) string {
	t.Helper()
	dav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" {
			t.Errorf("unexpected WebDAV request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:">`+verificationSelfXML(r.URL.Path)+`</d:multistatus>`)
	}))
	t.Cleanup(dav.Close)
	src := request(t, f.router, "POST", "/api/v1/media-sources", map[string]any{"type": "WEBDAV", "name": name, "location": dav.URL + "/dav/", "username": "private-user", "password": "private-secret"}, 201)
	return text(src["id"])
}
func shareBundle(t *testing.T, entries []any, assets map[string]any) []byte {
	t.Helper()
	b, e := packManifest(map[string]any{"format": "singverse-media-index", "version": 2, "entries": entries, "assets": assets})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func entry(name string, size any) map[string]any {
	item := map[string]any{"path": name, "metadata": map[string]any{"tags": map[string]any{"title": name, "artists": []any{"Shared Artist"}}, "technical": map[string]any{"duration": 123.5}, "fields": map[string]any{"title": "PRESENT", "artists": "PRESENT"}, "lyrics": map[string]any{"timing": "none", "text": "line one\nline two"}}}
	if size != nil {
		item["size"] = size
	}
	return item
}
func exportJob(t *testing.T, f *fixture, source string, base any) map[string]any {
	t.Helper()
	job := request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs", base, 202)
	if e := f.s.ProcessFiles(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := f.s.ProcessShare(context.Background()); e != nil {
		t.Fatal(e)
	}
	job = request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+text(job["id"]), nil, 200)
	if job["state"] != "SUCCEEDED" {
		t.Fatal(job)
	}
	return job
}

func TestShareRoundTripDurableFilesDeltaAndPrivacy(t *testing.T) {
	exerciseShareRoundTrip(t, setup(t))
}
func TestMySQLShareRoundTripDurableFilesDeltaAndPrivacy(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseShareRoundTrip(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseShareRoundTrip(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	source, ids := f.local(t, "A-First.mkv", "B-Second.mp3")
	if e := f.s.ProcessFiles(ctx); e != nil {
		t.Fatal(e)
	}
	for _, id := range ids {
		folder := filepath.Join(f.cfg.MetadataPath, "library", source, id)
		for _, name := range []string{"metadata.json", "manifest.json", "media.strm", "media.nfo"} {
			b, e := os.ReadFile(filepath.Join(folder, name))
			if e != nil || len(b) == 0 {
				t.Fatalf("missing derivative %s %v", name, e)
			}
		}
	}
	job := exportJob(t, f, source, nil)
	if number(job["package_version"]) != shareArchiveVersion || number(job["processed_count"]) != 2 {
		t.Fatal(job)
	}
	jobID := text(job["id"])
	wBase := httptest.NewRecorder()
	f.router.ServeHTTP(wBase, httptest.NewRequest("GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+jobID+"/download", nil))
	if wBase.Code != 200 {
		t.Fatal(wBase.Code, wBase.Body.String())
	}
	bundle := wBase.Body.Bytes()
	z, e := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if e != nil || len(z.File) != 2 {
		t.Fatalf("expected an index and one record page: %v", e)
	}
	for _, file := range z.File {
		if file.Name != "index.json" && file.Name != "records/000001.json" {
			t.Fatal("unexpected archive entry", file.Name)
		}
		data, e := readZipFile(file, shareLimit)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(data, []byte("private-user")) || bytes.Contains(data, []byte("private-secret")) || bytes.Contains(data, []byte(f.cfg.MetadataPath)) {
			t.Fatalf("private data leaked in %s", file.Name)
		}
	}
	target := newDAV(t, f, "target")
	res, e := f.s.ImportStream(ctx, target, bytes.NewReader(bundle))
	if e != nil || res["imported"] != 2 {
		t.Fatalf("%v %v", res, e)
	}
	again, e := f.s.ImportStream(ctx, target, bytes.NewReader(bundle))
	if e != nil || again["imported"] != 2 || again["processed"] != 2 {
		t.Fatalf("receipt: %v %v", again, e)
	}
	songs := request(t, f.router, "GET", "/api/v1/media-sources/"+target+"/songs", nil, 200)
	if number(songs["totalElements"]) != 2 {
		t.Fatal(songs)
	}
	song := songs["items"].([]any)[0].(map[string]any)
	a := metaOf(song)
	if a == nil || a.Origins["artists"].Kind != "SHARED" {
		t.Fatalf("shared projection %#v", a)
	}
	// A metadata edit is exported in the delta; removal is advisory only.
	review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+ids[0]+"/review", nil, 200)
	request(t, f.router, "POST", "/api/v1/metadata/songs/"+ids[0]+"/review/apply", ReviewApply{RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), Fields: []string{"title"}, Edits: map[string]any{"title": "Confirmed Updated"}}, 200)
	if _, e = f.db.Exec("DELETE FROM media_song WHERE id=?", ids[1]); e != nil {
		t.Fatal(e)
	}
	delta := exportJob(t, f, source, map[string]any{"baseId": jobID})
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+text(delta["id"])+"/download", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	outer := w.Body.Bytes()
	z, e = zip.NewReader(bytes.NewReader(outer), int64(len(outer)))
	if e != nil {
		t.Fatal(e)
	}
	advisory, e := z.Open("deletions/000001.json")
	if e != nil {
		t.Fatal(e)
	}
	deleted, e := io.ReadAll(advisory)
	advisory.Close()
	if e != nil || !bytes.Contains(deleted, []byte(`"advisoryOnly":true`)) {
		t.Fatalf("%s %v", deleted, e)
	}
	result, e := f.s.ImportStream(ctx, target, bytes.NewReader(outer))
	if e != nil || result["imported"] != 1 {
		t.Fatalf("set import %v %v", result, e)
	}
	songs = request(t, f.router, "GET", "/api/v1/media-sources/"+target+"/songs", nil, 200)
	if number(songs["totalElements"]) != 2 {
		t.Fatal("advisory removed existing song", songs)
	}
	var changed bytes.Buffer
	writer := zip.NewWriter(&changed)
	for _, file := range z.File {
		if file.Name != "records/000001.json" {
			if e = writer.Copy(file); e != nil {
				t.Fatal(e)
			}
			continue
		}
		data, e := readZipFile(file, shareLimit)
		if e != nil {
			t.Fatal(e)
		}
		altered := bytes.Replace(data, []byte("Confirmed Updated"), []byte("Modified Metadata"), 1)
		if bytes.Equal(data, altered) || len(data) != len(altered) {
			t.Fatal("test must change a record without changing its length")
		}
		entry, e := writer.Create(file.Name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = entry.Write(altered); e != nil {
			t.Fatal(e)
		}
	}
	if e = writer.Close(); e != nil {
		t.Fatal(e)
	}
	alteredArchive := filepath.Join(t.TempDir(), "altered.zip")
	if e = os.WriteFile(alteredArchive, changed.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	if e = ValidateShareFile(alteredArchive); e == nil || !strings.Contains(e.Error(), "SHA-256") {
		t.Fatal("changed record was not rejected by its checksum", e)
	}
}

func TestShareImportReceiptChunkRollbackResumeAndAudioClaim(t *testing.T) {
	exerciseShareImportChunks(t, setup(t))
}
func TestMySQLShareImportReceiptChunkRollbackResumeAndAudioClaim(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseShareImportChunks(t, setupWithDatabase(t, db, "mysql", cfg))
}
func exerciseShareImportChunks(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	source := newDAV(t, f, "chunks")
	entries := []any{}
	for i := 0; i < 205; i++ {
		item := entry(fmt.Sprintf("A-Song%03d.mkv", i), int64(9007199254740993))
		item["metadata"].(map[string]any)["audioRoles"] = map[string]any{"original": map[string]any{"streamIndex": 1}}
		entries = append(entries, item)
	}
	b := shareBundle(t, entries, nil)
	claims := 0
	fail := true
	f.s.SetAudioClaim(func(ctx context.Context, tx store.DBTX, id string, claim map[string]any) error {
		claims++
		if fail && claims == 101 {
			return fmt.Errorf("forced claim failure")
		}
		_, e := f.lib.SongWith(ctx, tx, id)
		return e
	})
	_, e := f.s.ImportPart(ctx, source, b)
	if e == nil {
		t.Fatal("expected second chunk failure")
	}
	t.Logf("interrupted import: %v claims=%d", e, claims)
	var count int
	if e = f.db.QueryRow("SELECT COUNT(*) FROM media_song WHERE source_id=?", source).Scan(&count); e != nil || count != 100 {
		t.Fatalf("chunk rollback count=%d %v", count, e)
	}
	fail = false
	result, e := f.s.ImportPart(ctx, source, b)
	if e != nil || result["imported"] != 205 || result["processed"] != 205 {
		t.Fatalf("resume %v %v", result, e)
	}
	var size int64
	if e = f.db.QueryRow("SELECT file_size FROM media_song WHERE source_id=? LIMIT 1", source).Scan(&size); e != nil || size != 9007199254740993 {
		t.Fatalf("int64 lost %d %v", size, e)
	}
	if e = f.s.ProcessFiles(ctx); e != nil {
		t.Fatal(e)
	}
	var pending int
	if e = f.db.QueryRow("SELECT COUNT(*) FROM media_metadata_file WHERE synced_revision<revision").Scan(&pending); e != nil || pending != 0 {
		t.Fatal(pending, e)
	}
}

func makeZip(t *testing.T, files []struct {
	name string
	data []byte
}) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, file := range files {
		out, e := w.Create(file.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = out.Write(file.data); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestShareValidationAndCoverHash(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	source := newDAV(t, f, "validation")
	png := testPNG(t)
	encoded := base64.StdEncoding.EncodeToString(png)
	key := hash(encoded)
	cover := entry("A-Cover.mkv", int64(7))
	cover["metadata"].(map[string]any)["coverRef"] = key
	b := shareBundle(t, []any{cover}, map[string]any{key: encoded})
	if _, e := f.s.ImportPart(ctx, source, b); e != nil {
		t.Fatal(e)
	}
	stored, e := f.s.SharedCover(key)
	if e != nil || !bytes.Equal(stored, png) {
		t.Fatal("cover hash changed", e)
	}
	cases := []map[string]any{entry("../escape.mkv", nil), entry("A.mkv", 1.5), entry("A.unsupported", nil), entry("A.mkv", -1)}
	for _, badEntry := range cases {
		var data []byte
		manifest := map[string]any{"format": "singverse-media-index", "version": 2, "entries": []any{badEntry}}
		if validPortablePath(text(badEntry["path"])) {
			data = shareBundle(t, []any{badEntry}, nil)
		} else {
			data = makeZip(t, []struct {
				name string
				data []byte
			}{{"manifest.json", []byte(encode(manifest))}})
		}
		if _, e = f.s.ImportPart(ctx, source, data); code(e) != 400 {
			t.Fatalf("bad entry %v: %v", badEntry, e)
		}
	}
	duplicate := shareBundle(t, []any{entry("A-Same.mkv", nil)}, nil)
	doc, e := readManifest(duplicate)
	if e != nil {
		t.Fatal(e)
	}
	doc["entries"] = []any{entry("A-Same.mkv", nil), entry("A-Same.mkv", nil)}
	dup := makeZip(t, []struct {
		name string
		data []byte
	}{{"manifest.json", []byte(encode(doc))}})
	if _, e = f.s.ImportPart(ctx, source, dup); code(e) != 400 {
		t.Fatal(e)
	}
	traversal := makeZip(t, []struct {
		name string
		data []byte
	}{{"../manifest.json", []byte(`{}`)}})
	if _, e = readManifest(traversal); code(e) != 400 {
		t.Fatal(e)
	}
	underreported := append([]byte{}, duplicate...)
	at := bytes.LastIndex(underreported, []byte{'P', 'K', 5, 6})
	binary.LittleEndian.PutUint16(underreported[at+8:at+10], 0)
	binary.LittleEndian.PutUint16(underreported[at+10:at+12], 0)
	if _, e = checkedZip(bytes.NewReader(underreported), int64(len(underreported)), 30001); code(e) != 400 {
		t.Fatal("underreported directory accepted", e)
	}
	part := shareBundle(t, []any{entry("A-New.mkv", nil)}, nil)
	index := []byte(encode(map[string]any{"format": "singverse-share-set", "version": 2, "parts": []string{"part-000001.zip"}, "deletions": []string{}}))
	for _, files := range [][]struct {
		name string
		data []byte
	}{{{"index.json", index}, {"unexpected.txt", []byte("x")}}, {{"index.json", index}}, {{"index.json", index}, {"part-000001.zip", part}, {"part-000001.zip", part}}} {
		outer := makeZip(t, files)
		if _, e = f.s.ImportStream(ctx, source, bytes.NewReader(outer)); code(e) != 400 {
			t.Fatal("invalid set accepted", e)
		}
	}
	var count int
	_ = f.db.QueryRow("SELECT COUNT(*) FROM media_song WHERE source_id=?", source).Scan(&count)
	if count != 1 {
		t.Fatal("invalid set partially imported", count)
	}
}

func TestShareArchiveRetriesFromFrozenSnapshotAndRejectsManagedSymlink(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	names := []string{}
	for i := 0; i < 80; i++ {
		names = append(names, fmt.Sprintf("A-Item%02d.mkv", i))
	}
	source, ids := f.local(t, names...)
	if e := f.s.ProcessFiles(ctx); e != nil {
		t.Fatal(e)
	}
	snapshot := map[string]any{"tags": map[string]any{"title": "Long lyrics", "artists": []string{"A"}}, "lyrics": map[string]any{"timing": "none", "text": strings.Repeat("line\n", 40000)}}
	for _, id := range ids {
		if _, e := f.db.Exec("UPDATE media_song SET embedded_tags_json=? WHERE id=?", encode(snapshot), id); e != nil {
			t.Fatal(e)
		}
	}
	if e := store.WithinTx(ctx, f.db, func(tx store.DBTX) error { return f.lib.SyncSourceMetadata(ctx, tx, source) }); e != nil {
		t.Fatal(e)
	}
	job := request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs", nil, 202)
	id := text(job["id"])
	if _, e := f.db.Exec("CREATE TRIGGER reject_finished_export BEFORE UPDATE OF state ON webdav_share_job WHEN NEW.state='RUNNING' AND OLD.processed_count>0 AND NEW.processed_count>OLD.processed_count BEGIN SELECT RAISE(ABORT,'forced export failure'); END"); e != nil {
		t.Fatal(e)
	}
	if e := f.s.ProcessShare(ctx); e == nil {
		t.Fatal("expected export checkpoint failure")
	}
	job = request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+id, nil, 200)
	if job["state"] != "FAILED" || number(job["part_count"]) != 0 || number(job["processed_count"]) <= 0 || number(job["processed_count"]) >= 80 {
		t.Fatal("missing saved checkpoint", job)
	}
	folder, e := f.s.shareFolder(id)
	if e != nil {
		t.Fatal(e)
	}
	files, e := os.ReadDir(folder)
	if e != nil || len(files) != 0 {
		t.Fatal("failed job published an archive or left a temporary file", files, e)
	}
	downloadURL := "/api/v1/media-sources/" + source + "/embedded-metadata/share-jobs/" + id + "/download"
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", downloadURL, nil))
	if w.Code != http.StatusConflict {
		t.Fatal("download available before commit", w.Code)
	}
	if _, e = f.db.Exec("DROP TRIGGER reject_finished_export"); e != nil {
		t.Fatal(e)
	}
	request(t, f.router, "POST", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+id+"/resume", nil, 200)
	if e = f.s.ProcessShare(ctx); e != nil {
		t.Fatal(e)
	}
	job = request(t, f.router, "GET", "/api/v1/media-sources/"+source+"/embedded-metadata/share-jobs/"+id, nil, 200)
	if job["state"] != "SUCCEEDED" || number(job["part_count"]) != 0 || number(job["processed_count"]) != 80 {
		t.Fatal(job)
	}
	files, e = os.ReadDir(folder)
	if e != nil || len(files) != 1 || files[0].Name() != shareArchiveName {
		t.Fatal("expected one persisted ZIP", files, e)
	}
	pages, count := 0, 0
	e = visitValidatedShareFile(filepath.Join(folder, shareArchiveName), func(doc map[string]any) error {
		pages++
		count += len(array(doc["entries"]))
		return nil
	}, nil)
	if e != nil || pages < 2 || count != 80 {
		t.Fatal("record pages lost after retry", pages, count, e)
	}
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Join(f.cfg.MetadataPath, "library", "linked")); e != nil {
		t.Fatal(e)
	}
	if _, e = managedDirectory(f.cfg.MetadataPath, "library", "linked", "child"); code(e) != 400 {
		t.Fatal("managed symlink accepted", e)
	}
	if _, e = os.Stat(filepath.Join(outside, "child")); !os.IsNotExist(e) {
		t.Fatal("symlink wrote outside root", e)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
