package downloads

import (
	"context"
	"testing"
)

func TestDownloadConfirmationIsReadOnlyAndPublishesOnce(t *testing.T) {
	s, root, track := fixture(t)
	_ = root
	ctx := context.Background()
	serveAudio(s, makeWAV())
	in := BatchInput{TrackIDs: []string{track.ID}, Quality: "128k", Name: "助手验收"}
	preview, e := s.DownloadPreview(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"lx_download_batch", "lx_download_item", "media_song"} {
		var n int
		if e = s.db.Get(&n, "SELECT COUNT(*) FROM "+table); e != nil || n != 0 {
			t.Fatal("preview wrote business data", table, n, e)
		}
	}
	id := uuid()
	created, e := s.CreateConfirmed(ctx, in, str(preview, "revision"), id)
	if e != nil {
		t.Fatal(e)
	}
	if str(created["batch"].(map[string]any), "id") != id {
		t.Fatal(created)
	}
	if _, e = s.CreateConfirmed(ctx, in, str(preview, "revision"), id); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.db.Get(&n, "SELECT COUNT(*) FROM lx_download_batch"); e != nil || n != 1 {
		t.Fatal("duplicate business batch", n, e)
	}
	item := itemFor(t, s, id)
	item = claim(t, s, str(item, "id"))
	if e = s.download(ctx, item); e != nil {
		t.Fatal(e)
	}
	s.finalizeBatch(str(item, "id"))
	result, e := s.Items(ctx, id, 0, 50)
	if e != nil {
		t.Fatal(e)
	}
	row := result["items"].([]map[string]any)[0]
	if row["status"] != "SUCCESS" || row["songId"] == nil {
		t.Fatal("download was not imported", row)
	}
	if _, e = s.CreateConfirmed(ctx, in, str(preview, "revision"), id); e != nil {
		t.Fatal(e)
	}
	if e = s.db.Get(&n, "SELECT COUNT(*) FROM media_song"); e != nil || n != 1 {
		t.Fatal("replayed completed publication", n, e)
	}
}

func TestDownloadChangedTargetsRequireNewConfirmation(t *testing.T) {
	s, _, track := fixture(t)
	ctx := context.Background()
	in := BatchInput{TrackIDs: []string{track.ID}}
	preview, e := s.DownloadPreview(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	track.Title = "已经改名"
	if e = s.remember(ctx, []Track{track}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateConfirmed(ctx, in, str(preview, "revision"), uuid()); e == nil {
		t.Fatal("stale approval accepted")
	}
	var n int
	if e = s.db.Get(&n, "SELECT COUNT(*) FROM lx_download_batch"); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestDownloadOpaqueSelectionChangesInvalidateConfirmation(t *testing.T) {
	s, _, track := fixture(t)
	ctx := context.Background()
	in := BatchInput{TrackIDs: []string{track.ID}}
	preview, e := s.DownloadPreview(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	track.MusicInfo["songmid"] = "replacement-resource"
	if e = s.remember(ctx, []Track{track}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateConfirmed(ctx, in, str(preview, "revision"), uuid()); e == nil {
		t.Fatal("opaque resource changed without a new confirmation")
	}
}

func TestRetryConfirmationFreezesFailedItemsAndCreatesOnce(t *testing.T) {
	s, _, track := fixture(t)
	ctx := context.Background()
	original := createOne(t, s, track)
	item := itemFor(t, s, original)
	if e := s.finish(ctx, s.db, str(item, "id"), "FAILED", "", "", "", "", "test failure"); e != nil {
		t.Fatal(e)
	}
	before, e := s.cached(ctx, s.db, track.ID)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := s.RetryPreview(ctx, original)
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.cached(ctx, s.db, track.ID)
	if e != nil || encode(before) != encode(after) {
		t.Fatal("retry preview changed the selection cache", e)
	}
	var n int
	if e = s.db.Get(&n, "SELECT COUNT(*) FROM lx_download_batch"); e != nil || n != 1 {
		t.Fatal("retry preview created a batch", n, e)
	}
	// Removing a failed target must invalidate the entire approved batch.
	if _, e = s.db.Exec("UPDATE lx_download_item SET status='SUCCESS' WHERE id=?", item["id"]); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RetryConfirmed(ctx, original, str(preview, "revision"), uuid()); e == nil {
		t.Fatal("changed retry target accepted")
	}
	if _, e = s.db.Exec("UPDATE lx_download_item SET status='FAILED' WHERE id=?", item["id"]); e != nil {
		t.Fatal(e)
	}
	receipt := uuid()
	for range 2 {
		created, e := s.RetryConfirmed(ctx, original, str(preview, "revision"), receipt)
		if e != nil || str(created["batch"].(map[string]any), "id") != receipt {
			t.Fatal(created, e)
		}
	}
	if e = s.db.Get(&n, "SELECT COUNT(*) FROM lx_download_batch"); e != nil || n != 2 {
		t.Fatal("duplicate retry batch", n, e)
	}
	result, e := s.ReconcileRetry(ctx, original, receipt)
	if e != nil || str(result, "id") != receipt {
		t.Fatal("retry receipt not recoverable", result, e)
	}
}
