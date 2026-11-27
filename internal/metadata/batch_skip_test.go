package metadata

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

const batchTestPath = "/api/v1/metadata/batches"

func TestBatchSkipsSavedMetadataAndPriorApplications(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sourceID, ids := f.local(t, "A-History.mkv", "A-Manual.mkv", "A-Pending.mkv")
	byTitle := map[string]string{}
	for _, id := range ids {
		song, err := f.lib.Song(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		byTitle[text(current(song)["title"])] = id
	}
	f.settings(t, false)
	history := testTrack("History")
	history.Lyrics = map[string]any{"timing": "none", "text": "已应用的歌词"}
	f.s.providers["NETEASE"] = fakeProvider{tracks: []Track{history}}
	previous := request(t, f.router, "POST", batchTestPath, StartInput{SongIDs: []string{byTitle["History"]}}, 201)
	if err := f.s.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	previousPath := batchTestPath + "/" + text(previous["id"])
	previous = request(t, f.router, "GET", previousPath, nil, 200)
	item := array(previous["items"])[0]
	candidateID := text(path(array(path(item, "candidates"))[0], "id"))
	review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+byTitle["History"]+"/review", nil, 200)
	firstResult := request(t, f.router, "POST", previousPath+"/items/"+text(path(item, "id"))+"/review/apply", ReviewApply{
		RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]),
		CandidateID: &candidateID, Fields: []string{"lyrics"},
	}, 200)
	if !contains(stringsOf(firstResult["updated"]), "lyrics") {
		t.Fatalf("expected a prior lyrics application: %#v", firstResult)
	}
	request(t, f.router, "POST", previousPath+"/items/"+text(path(item, "id"))+"/retry", nil, 409)
	song, err := f.lib.Song(ctx, byTitle["History"])
	if err != nil || confirmed(song) {
		t.Fatalf("the prior application should be discoverable via its batch history: %v", err)
	}
	applyBatchTestAlbum(t, f, byTitle["Manual"], "保留手工专辑")

	var requests atomic.Int32
	f.s.providers["NETEASE"] = contextProvider(func(context.Context) ([]Track, error) {
		requests.Add(1)
		return []Track{testTrack("Pending")}, nil
	})
	input := StartInput{All: true, SourceID: &sourceID, AutoApply: ptr(true)}
	batch := request(t, f.router, "POST", batchTestPath, input, 201)
	if batch["status"] != "RUNNING" || number(batch["skippedExisting"]) != 2 || number(batch["pending"]) != 1 {
		t.Fatalf("want two skips and one match: %#v", batch)
	}
	if err = f.s.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	finished := request(t, f.router, "GET", batchTestPath+"/"+text(batch["id"]), nil, 200)
	if finished["status"] != "COMPLETED" || number(finished["applied"]) != 1 || requests.Load() != 1 {
		t.Fatalf("want one search and application: %#v, requests=%d", finished, requests.Load())
	}

	allSkipped := request(t, f.router, "POST", batchTestPath, input, 201)
	if allSkipped["status"] != "COMPLETED" || number(allSkipped["skippedExisting"]) != 3 || number(allSkipped["completed"]) != 3 || number(allSkipped["applied"]) != 0 {
		t.Fatalf("want a completed batch with all songs skipped: %#v", allSkipped)
	}
	if err = f.s.ProcessNext(ctx); err != nil || requests.Load() != 1 {
		t.Fatalf("an all-skipped batch should send no requests: %v, requests=%d", err, requests.Load())
	}
	for _, item := range array(allSkipped["items"]) {
		if path(item, "status") != "SKIPPED_EXISTING" || len(array(path(item, "candidates"))) != 0 {
			t.Fatalf("unexpected skipped item: %#v", item)
		}
	}
}

func TestBatchSkipsSongsAppliedAfterItStarts(t *testing.T) {
	for _, test := range []struct {
		name      string
		inFlight  bool
		wantCalls int32
	}{
		{"before the platform query", false, 0},
		{"during the platform query", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, ids := f.local(t, "A-Song.mkv")
			f.settings(t, false)
			var calls atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			f.s.providers["NETEASE"] = contextProvider(func(ctx context.Context) ([]Track, error) {
				calls.Add(1)
				close(started)
				select {
				case <-release:
					return []Track{testTrack("Song")}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			batch := request(t, f.router, "POST", batchTestPath, StartInput{SongIDs: ids, AutoApply: ptr(true)}, 201)
			if test.inFlight {
				done := make(chan error, 1)
				go func() { done <- f.s.ProcessNext(ctx) }()
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				applyBatchTestAlbum(t, f, ids[0], "保留手工专辑")
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else {
				applyBatchTestAlbum(t, f, ids[0], "保留手工专辑")
				close(release)
				if err := f.s.ProcessNext(ctx); err != nil {
					t.Fatal(err)
				}
			}
			finished := request(t, f.router, "GET", batchTestPath+"/"+text(batch["id"]), nil, 200)
			item := array(finished["items"])[0]
			if finished["status"] != "COMPLETED" || number(finished["skippedExisting"]) != 1 || number(finished["review"]) != 0 || path(item, "status") != "SKIPPED_EXISTING" || calls.Load() != test.wantCalls {
				t.Fatalf("want a skipped song and %d searches: %#v, requests=%d", test.wantCalls, finished, calls.Load())
			}
			song, err := f.lib.Song(ctx, ids[0])
			if err != nil || current(song)["album"] != "保留手工专辑" {
				t.Fatalf("existing metadata should be preserved: %#v, %v", song, err)
			}
		})
	}
}

func applyBatchTestAlbum(t *testing.T, f *fixture, songID, album string) {
	t.Helper()
	root := "/api/v1/metadata/songs/" + songID + "/review"
	review := request(t, f.router, "GET", root, nil, 200)
	request(t, f.router, "POST", root+"/apply", ReviewApply{
		RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]),
		Fields: []string{"album"}, Edits: map[string]any{"album": album},
	}, 200)
}
