package execution

import (
	"context"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestAudioBatchConfirmationPreviewHasNoBusinessEffects(t *testing.T) {
	f := executionFixture(t, "")
	f.s.testProbe(t, f.song, audioStreams(1))
	counts := func() []int64 {
		t.Helper()
		out := []int64{}
		for _, table := range []string{"audio_batch", "audio_batch_item", "media_audio_job", "background_task"} {
			var n int64
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	before := counts()
	f.s.SetProjector(func(context.Context, store.DBTX, string, string) error {
		t.Error("confirmation preview projected a business task")
		return nil
	})
	// A busy execution slot does not prevent read-only confirmation previews.
	f.s.preparing.Store(true)
	defer f.s.preparing.Store(false)
	input := map[string]any{"ids": []any{f.song, f.song}, "backend": "cpu", "separationProfile": "DSP_FAST"}
	first, err := f.s.PreviewAudioBatch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.PreviewAudioBatch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Targets) != 1 || first.Targets[0].ID != f.song || first.Targets[0].Fingerprint == "" || first.Revision == "" || first.Revision != second.Revision || first.Page != "/library/audio-processing" {
		t.Fatal("target deduplication or stable snapshot failed", first, second)
	}
	if !f.s.preparing.Load() {
		t.Fatal("read preview changed the execution slot")
	}
	for i, after := range counts() {
		if after != before[i] {
			t.Fatalf("confirmation preview wrote business data: before=%v after=%v", before, counts())
		}
	}
	if _, err := f.s.PreviewAudioBatch(t.Context(), map[string]any{"ids": []any{"missing"}}); err == nil {
		t.Fatal("missing target accepted")
	}
	changed := map[string]any{"ids": []any{f.song}, "backend": "cpu", "separationProfile": "DSP_FAST", "outputFormat": "flac"}
	next, err := f.s.PreviewAudioBatch(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision == first.Revision {
		t.Fatal("changed recipe retained the approval revision")
	}
	// Changes must be visible immediately, even inside the normal catalog TTL.
	if _, err := f.db.Exec("UPDATE media_song SET file_size=file_size+1 WHERE id=?", f.song); err != nil {
		t.Fatal(err)
	}
	next, err = f.s.PreviewAudioBatch(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision == first.Revision || next.Targets[0].Fingerprint == first.Targets[0].Fingerprint {
		t.Fatal("fresh target change retained the approval revision")
	}
}

func TestConfirmedAudioBatchRejectsStaleTargetsAndRecoversReceipt(t *testing.T) {
	f := executionFixture(t, "")
	f.s.testProbe(t, f.song, audioStreams(1))
	input := map[string]any{"ids": []any{f.song}, "backend": "cpu", "separationProfile": "DSP_FAST"}
	p, e := f.s.PreviewAudioBatch(t.Context(), input)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Exec("UPDATE media_song SET file_size=file_size+1 WHERE id=?", f.song); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.CreateConfirmedAudioBatch(t.Context(), input, p.Revision, uuid()); e == nil {
		t.Fatal("changed audio selection accepted")
	}
	var n int
	if e = f.db.Get(&n, "SELECT COUNT(*) FROM audio_batch"); e != nil || n != 0 {
		t.Fatal("stale audio approval created work", n, e)
	}
	p, e = f.s.PreviewAudioBatch(t.Context(), input)
	if e != nil {
		t.Fatal(e)
	}
	receipt := uuid()
	for range 2 {
		if _, e = f.s.CreateConfirmedAudioBatch(t.Context(), input, p.Revision, receipt); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.db.Get(&n, "SELECT COUNT(*) FROM audio_batch WHERE id=?", receipt); e != nil || n != 1 {
		t.Fatal("audio receipt replayed a batch", n, e)
	}
}
