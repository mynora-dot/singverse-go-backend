package metadata

import (
	"context"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestLegacyImportRetainsStreamTitleEvidence(t *testing.T) {
	for _, version := range []string{"v2", "v3"} {
		t.Run(version, func(t *testing.T) {
			f := setup(t)
			source := newDAV(t, f, "audio-"+version)
			item := entry("song.mp4", 100)
			snap := item["metadata"].(map[string]any)
			streams := []any{
				map[string]any{"codec_type": "video", "index": 0},
				map[string]any{"codec_type": "audio", "index": 4, "tags": map[string]any{"title": "原唱"}},
				map[string]any{"codec_type": "audio", "index": 6, "tags": map[string]any{"title": "伴奏"}},
			}
			snap["technical"] = map[string]any{"duration": "120.25", "streams": streams}
			// The old audio controller exported a separate optional status object.
			snap["audioRoles"] = map[string]any{"state": "UNKNOWN", "duration": 120.25, "streams": streams[1:]}
			var received map[string]any
			var media string
			f.s.SetAudioClaim(func(_ context.Context, _ store.DBTX, id string, claim map[string]any) error {
				media, received = id, claim
				return nil
			})
			switch version {
			case "v2":
				_, err := f.s.ImportPart(t.Context(), source, shareBundle(t, []any{item}, nil))
				if err != nil {
					t.Fatal(err)
				}
			case "v3":
				job := applyWorkflow(t, f, source, workflowPackage(t, uuid(), 1, item), "VERIFYING")
				if job["phase"] != "VERIFYING" || number(job["verifiedCount"]) != 0 {
					t.Fatal(job)
				}
			}
			doc, err := f.lib.Formal().Read(t.Context(), f.db, media)
			if err != nil {
				t.Fatal(err)
			}
			o, b, basis := metastandard.PackageAudioRoles(doc.Fields["audioRoles"], doc.Fields["observation"])
			if o != "stream:4" || b != "stream:6" || basis != "STREAM_TITLES" || number(doc.Fields["durationMs"]) != 120250 || doc.States["observation"].Origin.Kind != "SHARED" {
				t.Fatalf("technical metadata: %v %s %s %s", doc, o, b, basis)
			}
			lo, lb, legacyBasis := metastandard.LegacyPackageAudioRoles(received)
			if lo != o || lb != b || legacyBasis != basis || decimal(received["duration"]) != 120.25 {
				t.Fatalf("legacy status: %v", received)
			}
		})
	}
}

func TestLegacyAudioClaimUpdatesOnlyFromOwningCollection(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	source := newDAV(t, f, "audio-updates")
	primary, contributor := uuid(), uuid()
	item := entry("song.mp4", 100)
	snapshot := item["metadata"].(map[string]any)
	data := func(collection string, rev int, original, backing string) []byte {
		if original != "" {
			snapshot["audioRoles"] = map[string]any{"state": "CONFIRMED", "originalId": original, "backingId": backing}
		}
		return workflowPackage(t, collection, rev, item)
	}
	var accepted []string
	var id string
	f.s.SetAudioClaim(func(_ context.Context, _ store.DBTX, song string, claim map[string]any) error {
		id = song
		accepted = append(accepted, text(claim["originalId"]))
		return nil
	})
	applyWorkflow(t, f, source, data(primary, 1, "", ""), "SUCCEEDED")
	applyWorkflow(t, f, source, data(contributor, 1, "stream:2", "stream:4"), "SUCCEEDED")
	if _, err := f.s.ImportPart(ctx, source, data(contributor, 2, "stream:4", "stream:2")); err != nil {
		t.Fatal(err)
	}
	applyWorkflow(t, f, source, data(primary, 7, "stream:5", "stream:2"), "SUCCEEDED")
	if len(accepted) != 2 || accepted[0] != "stream:2" || accepted[1] != "stream:4" {
		t.Fatal(accepted)
	}
	shared, err := f.s.sharedSnapshot(ctx, f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	owner := shared["sharedFields"].(map[string]any)["audioRoles"].(map[string]any)
	if owner["collectionId"] != contributor || number(owner["revision"]) != 2 {
		t.Fatal(owner)
	}
	portable, err := f.s.Snapshot(ctx, f.db, id)
	if err != nil || portable["audioRoles"] != nil {
		t.Fatalf("portable snapshot includes pending claim: %v, %v", portable["audioRoles"], err)
	}
}

func TestLegacyHistoricalAudioClaimNeedsMatchingReplay(t *testing.T) {
	f := setup(t)
	source := newDAV(t, f, "audio-history")
	collection := uuid()
	item := entry("song.mp4", 100)
	applyWorkflow(t, f, source, workflowPackage(t, collection, 1, item), "SUCCEEDED")
	var media string
	if err := f.db.QueryRow("SELECT id FROM media_song WHERE source_id=?", source).Scan(&media); err != nil {
		t.Fatal(err)
	}
	old := map[string]any{"state": "CONFIRMED", "originalId": "stream:2", "backingId": "stream:4"}
	newer := map[string]any{"state": "CONFIRMED", "originalId": "stream:4", "backingId": "stream:2"}
	if _, err := f.db.Exec("INSERT INTO media_audio_import(song_id, claim_json) VALUES(?, ?)", media, encode(map[string]any{"fingerprint": "old", "claim": old})); err != nil {
		t.Fatal(err)
	}
	var accepted []string
	f.s.SetAudioClaim(func(_ context.Context, _ store.DBTX, _ string, claim map[string]any) error {
		accepted = append(accepted, text(claim["originalId"]))
		return nil
	})
	apply := func(series string, revision int, claim map[string]any) {
		item["metadata"].(map[string]any)["audioRoles"] = claim
		applyWorkflow(t, f, source, workflowPackage(t, series, revision, item), "SUCCEEDED")
	}
	apply(collection, 2, newer)
	apply(uuid(), 3, old)
	apply(collection, 3, old)
	apply(collection, 4, newer)
	if len(accepted) != 2 || accepted[0] != "stream:2" || accepted[1] != "stream:4" {
		t.Fatal(accepted)
	}
}
