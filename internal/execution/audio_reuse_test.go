package execution

import (
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/metastore"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func packageAudioDocument() metastandard.Document {
	return metastandard.Document{
		Fields: map[string]any{"audioRoles": metastandard.AudioRoles("stream:2", "stream:1"), "durationMs": 120250, "observation": map[string]any{"streams": audioStreams(2)}},
		States: map[string]metastandard.FieldState{"audioRoles": {Origin: metastandard.Origin{Kind: "SHARED"}, Decision: "AUTO", Status: "PRESENT"}},
	}
}

func packageAudioReceipt(song map[string]any, level string) map[string]any {
	return map[string]any{
		"location_source_id": song["source_id"], "location_path": song["relative_path"], "location_state": "PRESENT", "shared_verification": "PRESENT",
		"location_observation": encoded(map[string]any{"size": song["file_size"], "verificationBinding": hash(str(song, "location")), "verificationSourceBinding": hash(str(song, "location") + "\x00" + str(song, "username") + "\x00" + str(song, "password_encrypted")), "verificationLevel": level, "sizeVerified": level == "PATH_AND_SIZE"}),
	}
}

func TestSharedAudioReuseRequiresCurrentDirectoryReceipt(t *testing.T) {
	song := map[string]any{"source_id": uuid(), "relative_path": "歌手/歌曲.mp4", "location": "https://dav.example/root", "username": "account", "password_encrypted": "encrypted", "file_size": 1000}
	for _, tc := range []struct {
		name      string
		change    func(map[string]any, map[string]any, metastandard.Document, map[string]any)
		confirmed bool
	}{
		{"path and size", func(map[string]any, map[string]any, metastandard.Document, map[string]any) {}, true},
		{"path only", func(_ map[string]any, receipt map[string]any, _ metastandard.Document, _ map[string]any) {
			proof := decoded(receipt["location_observation"])
			proof["size"], proof["verificationLevel"] = nil, "PATH_ONLY"
			receipt["location_observation"] = encoded(proof)
		}, true},
		{"account changed", func(song map[string]any, _ map[string]any, _ metastandard.Document, _ map[string]any) {
			song["username"] = "new-account"
		}, false},
		{"root changed", func(song map[string]any, _ map[string]any, _ metastandard.Document, _ map[string]any) {
			song["location"] = "https://dav.example/other"
		}, false},
		{"password changed", func(song map[string]any, _ map[string]any, _ metastandard.Document, _ map[string]any) {
			song["password_encrypted"] = "new-encrypted"
		}, false},
		{"different path", func(_ map[string]any, receipt map[string]any, _ metastandard.Document, _ map[string]any) {
			receipt["location_path"] = "其他.mp4"
		}, false},
		{"missing path", func(_ map[string]any, receipt map[string]any, _ metastandard.Document, _ map[string]any) {
			receipt["location_state"] = "MISSING"
		}, false},
		{"pending shared", func(_ map[string]any, receipt map[string]any, _ metastandard.Document, _ map[string]any) {
			receipt["shared_verification"] = "PENDING"
		}, false},
		{"size changed", func(song map[string]any, _ map[string]any, _ metastandard.Document, _ map[string]any) {
			song["file_size"] = 1001
		}, false},
		{"incomplete pair", func(_ map[string]any, _ map[string]any, doc metastandard.Document, _ map[string]any) {
			doc.Fields["audioRoles"] = metastandard.AudioRoles("stream:2", "")
		}, false},
		{"duplicate streams", func(_ map[string]any, _ map[string]any, doc metastandard.Document, _ map[string]any) {
			doc.Fields["observation"] = map[string]any{"streams": []any{audioStreams(2)[0], audioStreams(2)[1], audioStreams(2)[1]}}
		}, false},
		{"receiver contradicts package", func(_ map[string]any, _ map[string]any, _ metastandard.Document, value map[string]any) {
			value["streams"], value["duration"] = audioStreams(1), 120.25
		}, false},
		{"manual clear", func(_ map[string]any, _ map[string]any, doc metastandard.Document, _ map[string]any) {
			doc.Fields["audioRoles"] = nil
			doc.States["audioRoles"] = metastandard.FieldState{Decision: "CLEAR", Status: "CLEARED"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := copyMap(song)
			receipt, doc, value := packageAudioReceipt(current, "PATH_AND_SIZE"), packageAudioDocument(), map[string]any{"state": "UNKNOWN"}
			tc.change(current, receipt, doc, value)
			projectAudioDocument(current, doc, receipt, value)
			if (str(value, "state") == "CONFIRMED") != tc.confirmed {
				t.Fatalf("unexpected adoption: %v", value)
			}
			if tc.confirmed && (str(value, "origin") != "SHARED" || boolv(value["receiverProbed"]) || str(value, "originalId") != "stream:2") {
				t.Fatalf("package changed provenance: %v", value)
			}
		})
	}
}

func TestVerifiedPackageAudioSkipsProbeAndSupportsManualDecision(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	song, err := f.s.song(ctx, f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	doc, receipt, collection := packageAudioDocument(), packageAudioReceipt(song, "PATH_AND_SIZE"), uuid()
	err = store.WithinTx(ctx, f.db, func(tx store.DBTX) error {
		if _, _, e := f.library.Formal().Observe(ctx, tx, f.song, metastore.Evidence{Origin: metastandard.Origin{Kind: "SHARED", CollectionID: collection}, Revision: "1", Fields: doc.Fields}); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO media_shared_song(song_id,source_id,collection_id,revision,snapshot_json,verification,expected_size,updated_at) VALUES(?,?,?,1,'{}','PRESENT',?,'now')", f.song, f.source, collection, song["file_size"]); e != nil {
			return e
		}
		return f.library.Formal().Location(ctx, tx, f.song, f.source, str(song, "relative_path"), "", "PRESENT", decoded(receipt["location_observation"]))
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "CONFIRMED" || str(status, "origin") != "SHARED" || boolv(status["receiverProbed"]) {
		t.Fatal(status, err)
	}
	if err = f.s.discoverSong(ctx, f.song, nil); err != nil {
		t.Fatal("reuse required a compute node", err)
	}
	var count int
	if err = f.db.Get(&count, "SELECT COUNT(*) FROM media_audio_job WHERE song_id=?", f.song); err != nil || count != 0 {
		t.Fatal("reuse manufactured a probe", count, err)
	}
	catalog, err := f.s.audioCatalog(ctx)
	if err != nil || len(catalog) != 1 || str(obj(catalog[0]["audio"]), "state") != "CONFIRMED" || str(catalog[0], "category") != "READY" {
		t.Fatal(catalog, err)
	}
	status, err = f.s.correctRoles(ctx, f.song, "stream:1", "stream:2", 0)
	if err != nil || str(status, "originalId") != "stream:1" || str(status, "origin") != "MANUAL" {
		t.Fatal("manual correction rejected verified declaration", status, err)
	}
	formal, err := f.library.Formal().Read(ctx, f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithinTx(ctx, f.db, func(tx store.DBTX) error {
		_, e := f.library.Formal().Update(ctx, tx, f.song, metastore.Update{ExpectedRevision: formal.Revision, RequestID: uuid(), Operations: []metastore.Operation{{Field: "audioRoles", Op: "CLEAR"}}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err = f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "UNKNOWN" || status["originalId"] != nil {
		t.Fatal("manual CLEAR was bypassed", status, err)
	}
	if err = f.s.discoverSong(ctx, f.song, nil); err != nil {
		t.Fatal("automatic detection bypassed CLEAR", err)
	}
}

func TestSingleAudioSongDoesNotInventStreamOrReadMedia(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	if _, err := f.db.Exec("UPDATE media_song SET relative_path='歌曲.FLAC',path_hash=? WHERE id=?", hash("歌曲.FLAC"), f.song); err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTx(ctx, f.db, func(tx store.DBTX) error { return f.library.SyncMetadata(ctx, tx, f.song) }); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "CONFIRMED" || str(obj(status["originalSelection"]), "kind") != "UNIQUE_AUDIO" || status["originalId"] != nil || status["streams"] != nil {
		t.Fatal(status, err)
	}
	if recognitionCategory(status) != "INAPPLICABLE" {
		t.Fatal("single audio scheduled for role recognition", status)
	}
	if err = f.s.discoverSong(ctx, f.song, nil); err != nil {
		t.Fatal("single audio required a probe", err)
	}
	var count int
	if err = f.db.Get(&count, "SELECT COUNT(*) FROM media_audio_job WHERE song_id=?", f.song); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	// A contradictory actual observation takes precedence over the collection's
	// single-audio convention, even when the extension is an audio extension.
	f.s.testProbe(t, f.song, audioStreams(2))
	status, err = f.s.AudioStatus(ctx, f.song)
	if err != nil || status["originalSelection"] != nil || str(status, "state") != "UNKNOWN" {
		t.Fatal("actual multi-stream input assumed unique", status, err)
	}
}

func TestReceiverProbeAndCorrectionExpireAfterAccountChange(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	f.s.testProbe(t, f.song, audioStreams(2))
	if _, err := f.s.correctRoles(ctx, f.song, "stream:1", "stream:2", 0); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.one(ctx, f.db, "audio-for-song", map[string]any{"song": f.song, "fingerprint": contentFingerprint(mustSong(t, f))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE media_source SET username='new-account' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "UNKNOWN" || status["streams"] != nil || status["originalId"] != nil {
		t.Fatal("old probe survived account change", status, err)
	}
	if _, err = f.s.correctRoles(ctx, f.song, "stream:2", "stream:1", 1); err == nil {
		t.Fatal("manual correction used old binding evidence")
	}
	if err = f.s.advanceAudio(ctx, job); err == nil {
		t.Fatal("stale node result accepted")
	} else {
		requireErrorStatus(t, err, 409)
	}
	f.s.testProbe(t, f.song, audioStreams(2))
	status, err = f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "CONFIRMED" || str(status, "originalId") != "stream:1" {
		t.Fatal("current proof did not preserve manual choice", status, err)
	}
}

func mustSong(t *testing.T, f *execFixture) map[string]any {
	t.Helper()
	song, err := f.s.song(t.Context(), f.db, f.song)
	if err != nil {
		t.Fatal(err)
	}
	return song
}

func TestSingleAudioExplicitSeparationNeedsNoInventedSource(t *testing.T) {
	f, ctx := executionFixture(t, "http://127.0.0.1:18081"), t.Context()
	if _, err := f.db.Exec("UPDATE media_song SET relative_path='歌曲.mp3',path_hash=? WHERE id=?", hash("歌曲.mp3"), f.song); err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTx(ctx, f.db, func(tx store.DBTX) error { return f.library.SyncMetadata(ctx, tx, f.song) }); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.SubmitAudio(ctx, f.song, "SEPARATE", "", nil)
	if err != nil || str(job, "state") != "QUEUED" {
		t.Fatal("explicit separation required an indexed probe", job, err)
	}
	stored, err := f.s.one(ctx, f.db, "audio-job", map[string]any{"id": job["id"]})
	if err != nil || storedRecipe(stored).source != "" {
		t.Fatal("unique source became a fabricated stream", stored, err)
	}
	var probes int
	if err = f.db.Get(&probes, "SELECT COUNT(*) FROM media_audio_job WHERE song_id=? AND kind='PROBE'", f.song); err != nil || probes != 0 {
		t.Fatal("separation eagerly created a probe", probes, err)
	}
}

func TestPlaybackObservationRejectsChangedBinding(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	before := playbackFingerprint(mustSong(t, f))
	if _, err := f.db.Exec("UPDATE media_source SET username='new-account' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	err := f.s.observeAudio(ctx, f.song, map[string]any{"duration": 120.25, "streams": audioStreams(2)}, before)
	requireErrorStatus(t, err, 409)
	var count int
	if err = f.db.Get(&count, "SELECT COUNT(*) FROM media_audio_job WHERE song_id=?", f.song); err != nil || count != 0 {
		t.Fatal("stale playback probe persisted", count, err)
	}
}

func TestLegacyAnalysisWithoutBindingCannotRestoreRoles(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	f.s.testProbe(t, f.song, audioStreams(2))
	if _, err := f.s.correctRoles(ctx, f.song, "stream:1", "stream:2", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE media_audio_job SET read_session_id='' WHERE song_id=?", f.song); err != nil {
		t.Fatal(err)
	}
	// Startup must not attach the current account to an observation made by an
	// unknown prior account. The local manual decision itself remains durable.
	if err := f.s.bootstrapFormalAudio(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "UNKNOWN" || status["streams"] != nil || status["originalId"] != nil {
		t.Fatal("unbound legacy result accepted", status, err)
	}
	formal, err := f.library.Formal().Read(ctx, f.db, f.song)
	if err != nil || formal.States["audioRoles"].Decision != "SET" {
		t.Fatal("manual choice was erased", formal, err)
	}
	if _, err = f.db.Exec("UPDATE media_source SET username='new-account' WHERE id=?", f.source); err != nil {
		t.Fatal(err)
	}
	status, err = f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "UNKNOWN" || status["streams"] != nil {
		t.Fatal("unbound history survived account edit", status, err)
	}
	f.s.testProbe(t, f.song, audioStreams(2))
	status, err = f.s.AudioStatus(ctx, f.song)
	if err != nil || str(status, "state") != "CONFIRMED" || str(status, "originalId") != "stream:1" {
		t.Fatal("fresh evidence did not recover manual mapping", status, err)
	}
}

func TestAudioCleanupViewKeepsBusinessOutcomeAndHidesPaths(t *testing.T) {
	f, ctx := executionFixture(t, ""), t.Context()
	f.s.testProbe(t, f.song, audioStreams(2))
	jobs, err := f.s.maps(ctx, f.db, "audio-for-song", map[string]any{"song": f.song, "fingerprint": contentFingerprint(mustSong(t, f))})
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	result := decoded(jobs[0]["result_json"])
	result["cleanupState"], result["cleanupError"], result["cleanupPaths"] = "PENDING", "临时文件清理失败，将自动重试", []any{"/private/node/input.tmp"}
	if _, err = f.db.Exec("UPDATE media_audio_job SET state='RUNNING' WHERE id=?", jobs[0]["id"]); err != nil {
		t.Fatal(err)
	}
	if err = f.s.updateAudio(ctx, str(jobs[0], "id"), "SUCCEEDED", result, ""); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.AudioStatus(ctx, f.song)
	if err != nil {
		t.Fatal(err)
	}
	view := obj(array(status["jobs"])[0])
	if str(view, "state") != "SUCCEEDED" || str(view, "cleanupState") != "PENDING" || str(view, "cleanupError") == "" || view["cleanupPaths"] != nil {
		t.Fatal("cleanup changed outcome or exposed node paths", view)
	}
}
