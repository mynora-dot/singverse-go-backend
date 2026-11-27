package metadata

import (
	"context"
	"testing"
)

func TestBatchUsesFirstRankedCandidateWhenAutoApplyAllowsIt(t *testing.T) {
	const title = "LongMelodySong"
	for _, test := range []struct {
		name              string
		alternativeTitle  string
		settingsAutoApply bool
		batchAutoApply    *bool
		threshold         float64
		wantStatus        string
	}{
		{"tied scores with explicit auto apply and global off", title, false, ptr(true), .95, "AUTO_APPLIED"},
		{"close scores with inherited auto apply", "LongMelodySonx", true, nil, .9, "AUTO_APPLIED"},
		{"below threshold", title, false, ptr(true), .96, "REVIEW"},
		{"review only with global on", title, true, ptr(false), .9, "REVIEW"},
		{"legacy request with global off", title, false, nil, .9, "REVIEW"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f := setup(t)
			_, ids := f.local(t, "A-"+title+".mkv")
			request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{
				Enabled: true, Providers: []string{"NETEASE"}, AutoApply: test.settingsAutoApply, Threshold: test.threshold,
			}, 200)
			studio, live := testTrack(title), testTrack(test.alternativeTitle)
			live.ExternalID = ptr("live")
			live.Album = ptr("演唱会")
			f.s.providers["NETEASE"] = fakeProvider{tracks: []Track{live, studio}}
			batch := request(t, f.router, "POST", "/api/v1/metadata/batches", StartInput{SongIDs: ids, AutoApply: test.batchAutoApply}, 201)
			if err := f.s.ProcessNext(ctx); err != nil {
				t.Fatal(err)
			}
			detail := request(t, f.router, "GET", "/api/v1/metadata/batches/"+text(batch["id"]), nil, 200)
			item := array(detail["items"])[0]
			candidates := array(path(item, "candidates"))
			if len(candidates) != 2 || path(item, "status") != test.wantStatus {
				t.Fatalf("want two candidates and status %s, got %#v", test.wantStatus, item)
			}
			song, err := f.lib.Song(ctx, ids[0])
			if err != nil {
				t.Fatal(err)
			}
			var wantAlbum any
			if test.wantStatus == "AUTO_APPLIED" {
				wantAlbum = path(candidates[0], "track", "album")
			}
			if got := current(song)["album"]; got != wantAlbum {
				t.Fatalf("stored album = %v, want %v", got, wantAlbum)
			}
		})
	}
}
