package metadata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Public catalogue shape, reduced to metadata and album artwork only.
const miguSearchFixture = `{"code":"000000","info":"成功","songResultData":{"totalCount":"1","result":[{"id":9007199254740993,"name":"Song","singers":[{"id":9007199254740995,"name":"Artist"}],"albums":[{"id":"8592","name":"Album"}],"songAliasName":"Alias","imgItems":[{"imgSizeType":"01","img":"https://d.musicapp.migu.cn/data/small.webp"},{"imgSizeType":"03","img":"https://d.musicapp.migu.cn/data/large.webp"}]}]}}`

func TestMiguPublicSongProviderProtocol(t *testing.T) {
	f := setup(t)
	calls := 0
	f.s.client.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if r.URL.Hostname() != "app.c.nf.migu.cn" || r.URL.Path != "/MIGUM2.0/v1.0/content/search_all.do" || q.Get("text") != "Song Artist" || q.Get("searchSwitch") != `{"song":1}` || q.Get("pageSize") != "8" || r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected Migu public metadata request", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(miguSearchFixture))}, nil
	})
	tracks, err := (&musicProvider{service: f.s, name: "MIGU"}).Search(context.Background(), "Song Artist", 8)
	if err != nil || len(tracks) != 1 || calls != 1 {
		t.Fatal(tracks, calls, err)
	}
	track := tracks[0]
	if *track.Provider != "MIGU" || *track.ExternalID != "9007199254740993" || *track.Title != "Song" || *track.Album != "Album" || *track.CoverURL != "https://d.musicapp.migu.cn/data/large.webp" || len(track.ArtistRefs) != 1 || track.ArtistRefs[0].ID != "9007199254740995" || track.Aliases[0] != "Alias" || track.DurationMs != nil || track.Availability != "UNKNOWN" {
		t.Fatalf("Migu metadata, largest supplied cover or ID precision lost: %+v", track)
	}
}

func TestMiguEmptyResultsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		body string
		fail bool
	}{
		{`{"code":"000000","songResultData":{"totalCount":"0","result":[]}}`, false},
		{`{"code":"000000","data":{"songResultData":{"totalCount":"0"}}}`, false},
		{`{"code":"999999","info":"upstream failure"}`, true},
		{`{"code":"000000"}`, true},
		{`{"code":"000000","songResultData":{"totalCount":"1"}}`, true},
		{`{"code":"000000","songResultData":{"totalCount":"0","result":{}}}`, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			f := setup(t)
			f.s.client.Transport = artworkTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			tracks, err := (&musicProvider{service: f.s, name: "MIGU"}).Search(context.Background(), "Song", 8)
			if (err != nil) != tc.fail || (!tc.fail && len(tracks) != 0) {
				t.Fatal("Migu failure was confused with no matches", tracks, err)
			}
		})
	}
}

func TestMiguAlbumCoverBoundariesAndDuration(t *testing.T) {
	var rows []any
	err := decode(`[{"songId":"3790007","songName":"Song","singerList":[{"id":"112","name":"Artist"}],"albumName":"Album","duration":"04:30","publishDate":"2003-07-31","img3":"/data/cover.webp"},{"songId":"2","songName":"Second","duration":180},{"id":"bad/id","name":"Invalid"}]`, &rows)
	if err != nil {
		t.Fatal(err)
	}
	tracks := parseMigu(rows, 1)
	if len(tracks) != 1 || *tracks[0].DurationMs != 270000 || *tracks[0].ReleaseDate != "2003-07-31" || *tracks[0].CoverURL != "https://d.musicapp.migu.cn/data/cover.webp" {
		t.Fatal("detail shape or limit lost", tracks)
	}
	tracks = parseMigu(rows, 8)
	if len(tracks) != 2 || *tracks[1].DurationMs != 180000 {
		t.Fatal("invalid identity accepted or seconds misinterpreted", tracks)
	}
	for _, raw := range []string{"https://d.musicapp.migu.cn.evil.invalid/a.webp", "https://user@d.musicapp.migu.cn/a.webp", "https://d.musicapp.migu.cn:444/a.webp", "//evil.invalid/a.webp", "https://127.0.0.1/a.webp"} {
		if cover := miguAlbumCover(map[string]any{"img3": raw}); cover != nil {
			t.Fatal("foreign cover accepted", raw)
		}
	}
	if cover := miguAlbumCover(map[string]any{"singers": []any{map[string]any{"img": "https://d.musicapp.migu.cn/artist.webp"}}, "mvPicUrl": "https://d.musicapp.migu.cn/video.webp"}); cover != nil {
		t.Fatal("artist or video artwork substituted for an album cover")
	}
}

func TestMiguSearchSurvivesQishuiEmptyResponseAndRequiresReview(t *testing.T) {
	f := setup(t)
	_, ids := f.local(t, "Artist-Song.mkv")
	id := ids[0]
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: true, Providers: []string{"QISHUI", "MIGU"}, Threshold: .95}, 200)
	f.s.client.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.URL.Hostname() == "app.c.nf.migu.cn" {
			body = miguSearchFixture
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	result := request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/search", SearchInput{Providers: []string{"QISHUI", "MIGU"}}, 200)
	if len(array(result["candidates"])) != 1 || len(array(result["errors"])) != 1 || !strings.Contains(text(path(array(result["errors"])[0], "message")), "汽水音乐未返回搜索数据") {
		t.Fatal("Qishui failure hid the Migu candidate or lost its explanation", result)
	}
	review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+id+"/review", nil, 200)
	if path(review, "values", "album") != nil || path(review, "values", "cover") != nil {
		t.Fatal("search applied metadata before confirmation", review)
	}
	candidateID := text(path(array(result["candidates"])[0], "id"))
	input := ReviewApply{RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), CandidateID: &candidateID, Fields: []string{"album"}}
	applied := request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/review/apply", input, 200)
	if text(path(applied, "after", "album")) != "Album" {
		t.Fatal("Migu candidate was not applied after confirmation", applied)
	}
	covers := request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/covers/search", SearchInput{Providers: []string{"MIGU"}}, 200)
	if len(array(covers["items"])) != 1 || text(path(array(covers["items"])[0], "provider")) != "MIGU" {
		t.Fatal("Migu was not available in extra cover search", covers)
	}
	failure := request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/search", SearchInput{Providers: []string{"QISHUI"}}, 503)
	if !strings.Contains(text(failure["detail"]), "汽水音乐未返回搜索数据") {
		t.Fatal("single-provider failure lost its actionable detail", failure)
	}
	if safeDetail(errors.New("private transport details")) != "Service Unavailable" {
		t.Fatal("untrusted transport details were exposed")
	}
}
