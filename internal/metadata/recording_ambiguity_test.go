package metadata

import (
	"context"
	"strings"
	"testing"
)

func TestAutomaticBatchLeavesAmbiguousRecordingsForReview(t *testing.T) {
	f := setup(t)
	_, ids := f.local(t, "A-Song.mkv")
	f.settings(t, true)
	studio, live := testTrack("Song"), testTrack("Song")
	live.ExternalID = ptr("live")
	live.Album = ptr("演唱会")
	live.DurationMs = ptr(130000)
	f.s.providers["NETEASE"] = fakeProvider{tracks: []Track{studio, live}}
	batch := request(t, f.router, "POST", "/api/v1/metadata/batches", StartInput{SongIDs: ids}, 201)
	if err := f.s.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	detail := request(t, f.router, "GET", "/api/v1/metadata/batches/"+text(batch["id"]), nil, 200)
	if number(detail["review"]) != 1 || number(detail["applied"]) != 0 {
		t.Fatalf("ambiguous recording was automatically applied: %#v", detail)
	}
	item := detail["items"].([]any)[0].(map[string]any)
	if !strings.Contains(text(item["error"]), "录音版本") {
		t.Fatalf("missing actionable review reason: %#v", item)
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM song_metadata WHERE song_id=?", ids[0]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous recording modified metadata: count=%d error=%v", count, err)
	}
}

func TestCloseRecordingAlternativeMargin(t *testing.T) {
	for _, test := range []struct {
		name       string
		candidates []Candidate
		want       bool
	}{
		{"single", []Candidate{{ID: "one", Score: .95}}, false},
		{"tie", []Candidate{{ID: "one", Score: .95}, {ID: "two", Score: .95}}, true},
		{"near", []Candidate{{ID: "one", Score: .95}, {ID: "two", Score: .90}}, true},
		{"exact eight points rounded down by floating subtraction", []Candidate{{ID: "one", Score: .95}, {ID: "two", Score: .87}}, false},
		{"exact eight points rounded up by floating subtraction", []Candidate{{ID: "one", Score: .93}, {ID: "two", Score: .85}}, false},
		{"just below eight points", []Candidate{{ID: "one", Score: .95}, {ID: "two", Score: .871}}, true},
		{"exact margin with floating noise", []Candidate{{ID: "one", Score: .9499999999999999}, {ID: "two", Score: .8700000000000001}}, false},
		{"close margin with floating noise", []Candidate{{ID: "one", Score: .9500000000000001}, {ID: "two", Score: .8710000000000001}}, true},
		{"normalize to thousandths", []Candidate{{ID: "one", Score: .9496}, {ID: "two", Score: .8704}}, false},
		{"distinct", []Candidate{{ID: "one", Score: .95}, {ID: "two", Score: .80}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := closeRecordingAlternative(test.candidates); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}
