package metadata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMetadataQueryCacheSkipsProviderAndKeepsFreshCandidates(t *testing.T) {
	f, song := searchFixture(t)
	calls := 0
	f.s.providers["QQ"] = contextProvider(func(context.Context) ([]Track, error) {
		calls++
		track := testTrack("Song")
		track.Provider = ptr("QQ")
		return []Track{track}, nil
	})
	query := "Song Artist"
	in := SearchInput{Keyword: &query, Providers: []string{"QQ"}}
	first, err := f.s.search(t.Context(), song, in, []string{"QQ"})
	if err != nil || len(first.Candidates) != 1 {
		t.Fatal(first, err)
	}
	// A valid hit must not enter either the network gate or its cooldown.
	guard := f.s.guards["QQ"]
	guard.permit <- struct{}{}
	guard.next, guard.cooldown = time.Now().Add(time.Hour), time.Now().Add(time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	second, err := f.s.search(ctx, song, in, []string{"QQ"})
	<-guard.permit
	if err != nil || len(second.Candidates) != 1 || calls != 1 {
		t.Fatal("hit queried provider or waited for gate", second, calls, err)
	}
	if first.Candidates[0].ID == second.Candidates[0].ID || first.Candidates[0].Track.FetchedAt != second.Candidates[0].Track.FetchedAt {
		t.Fatal("cached a local candidate, or rewrote the upstream fetch time")
	}
	first.Candidates[0].Track.Artists[0] = "edited"
	if second.Candidates[0].Track.Artists[0] == "edited" {
		t.Fatal("cached track aliases local edit")
	}
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: true, Providers: []string{"NETEASE"}, Threshold: .95}, 200)
	if _, err := f.s.callProvider(t.Context(), "QQ", f.s.providers["QQ"], query); err == nil || calls != 1 {
		t.Fatal("hit bypassed disabled-platform validation", err, calls)
	}
}

func TestArtistMetadataCacheIncludesIdentityAndSettings(t *testing.T) {
	f, _ := searchFixture(t)
	calls := 0
	f.s.client.Transport = artworkTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":200,"artist":{"name":"Artist","img1v1Url":"https://p1.music.126.net/portrait.jpg"}}`))}, nil
	})
	refs := []ArtistRef{{Provider: "NETEASE", ID: "123", Name: "Artist"}}
	for range 2 {
		art, err := f.s.ArtistArtworkFrom(t.Context(), "Artist", []string{"Song"}, refs, "NETEASE")
		if err != nil || art == nil || art.ExternalID != "123" {
			t.Fatal(art, err)
		}
	}
	if calls != 1 {
		t.Fatal("artist hit queried external source", calls)
	}
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: false, Providers: []string{"NETEASE"}, Threshold: .95}, 200)
	if _, err := f.s.ArtistArtworkFrom(t.Context(), "Artist", []string{"Song"}, refs, "NETEASE"); err == nil {
		t.Fatal("cached artist bypassed settings")
	}
}
