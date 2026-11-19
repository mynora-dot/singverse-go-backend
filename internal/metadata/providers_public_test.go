package metadata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPublicSongProviderProtocols(t *testing.T) {
	for _, provider := range []string{"KUWO", "QISHUI"} {
		t.Run(provider, func(t *testing.T) {
			f := setup(t)
			calls := 0
			f.s.client.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body := ""
				if provider == "KUWO" {
					if r.URL.Hostname() != "search.kuwo.cn" || r.URL.Path != "/r.s" || r.URL.Query().Get("ft") != "music" || r.URL.Query().Get("all") != "Song Artist" {
						t.Fatal("wrong Kuwo catalogue request", r.URL)
					}
					body = `{'TOTAL':'1','abslist':[{'MUSICRID':'MUSIC_145389372','SONGNAME':'Song&nbsp;(Live)','ARTIST':'Artist','ARTISTID':'947','ALBUM':'Album','DURATION':'257','web_timingonline':'2020-07-17&nbsp;00:00:00','web_albumpic_short':'120/64/30/2753403960.jpg'}]}`
				} else {
					if r.URL.Hostname() == "open.qishui.com" {
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"client_token":"test-token"}`))}, nil
					}
					if r.URL.Hostname() != "open.douyin.com" || r.URL.Path != "/api/luna/v1/platform/search/" || r.Method != "POST" || r.Header.Get("Access-Token") != "test-token" {
						t.Fatal("wrong Qishui catalogue request", r.URL)
					}
					body = qishuiCarFixture
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			p := &musicProvider{service: f.s, name: provider}
			tracks, err := p.Search(context.Background(), "Song Artist", 8)
			wantCalls := 1
			if provider == "QISHUI" {
				wantCalls = 2
			}
			if err != nil || len(tracks) != 1 || calls != wantCalls || *tracks[0].Provider != provider || tracks[0].CoverURL == nil {
				t.Fatalf("tracks=%+v calls=%d error=%v", tracks, calls, err)
			}
			track := tracks[0]
			if provider == "KUWO" && (*track.Title != "Song (Live)" || *track.DurationMs != 257000 || *track.ReleaseDate != "2020-07-17" || *track.CoverURL != "https://img1.kuwo.cn/star/albumcover/0/64/30/2753403960.jpg") {
				t.Fatalf("Kuwo metadata lost or thumbnail substituted: %+v", track)
			}
			if provider == "QISHUI" && (*track.ExternalID != "6850116360324810753" || track.ArtistRefs[0].ID != "6807264292040083458" || *track.DurationMs != 257254 || !strings.HasSuffix(*track.CoverURL, "-image.image")) {
				t.Fatalf("Qishui ID precision, duration or original lost: %+v", track)
			}
		})
	}
}

func TestPublicProviderFailuresRemainDistinctFromNoMatches(t *testing.T) {
	for _, tc := range []struct {
		provider, body string
		wantError      bool
	}{
		{"KUWO", `{'TOTAL':'0','abslist':[]}`, false},
		{"QISHUI", `{"err_no":0,"data":{"result_groups":[]}}`, false},
		{"KUWO", `null`, true},
		{"KUWO", `{'TOTAL':'0','abslist':[]}; alert(1)`, true},
		{"QISHUI", `{"err_no":28001003}`, true},
		{"QISHUI", `{"data":{"result_groups":[]}} {}`, true},
	} {
		t.Run(tc.provider+tc.body[:min(12, len(tc.body))], func(t *testing.T) {
			f := setup(t)
			f.s.client.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() == "open.qishui.com" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"client_token":"test-token"}`))}, nil
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			tracks, err := (&musicProvider{service: f.s, name: tc.provider}).Search(context.Background(), "Song", 8)
			if (err != nil) != tc.wantError || (!tc.wantError && len(tracks) != 0) {
				t.Fatal("no results and failed source conflated", tracks, err)
			}
		})
	}
	if cover := kuwoAlbumCover(map[string]any{"web_artistpic_short": "120/artist.jpg", "MVPIC": "120/video.jpg"}); cover != nil {
		t.Fatal("artist or video image substituted for album cover")
	}
	for _, uri := range []string{"../private", "https://evil.invalid/x", "//evil.invalid/x"} {
		if cover := qishuiAlbumCover(map[string]any{"uri": uri, "urls": []any{"https://p3-luna.douyinpic.com/img/"}, "template_prefix": "tplv-test"}); cover != nil {
			t.Fatal("unsafe Qishui image template accepted", uri)
		}
	}
}

func TestQishuiWrappedTrackKeepsLongIDs(t *testing.T) {
	var root map[string]any
	err := decode(`{"result_groups":[{"data":[{"entity":{"track_wrapper":{"track":{"id":6850116360324810753,"name":"Song","artists":[{"id":6807264292040083458,"name":"Artist"}],"duration":257254,"album":{"name":"Album"}}}}}]}]}`, &root)
	if err != nil {
		t.Fatal(err)
	}
	tracks := parseQishui(array(root["result_groups"]), 8)
	if len(tracks) != 1 || *tracks[0].ExternalID != "6850116360324810753" || len(tracks[0].ArtistRefs) != 1 || tracks[0].ArtistRefs[0].ID != "6807264292040083458" || *tracks[0].DurationMs != 257254 {
		t.Fatal("wrapped Qishui track was dropped or IDs lost precision", tracks)
	}
}

func TestQishuiComprehensiveSearchDeduplicatesTopResults(t *testing.T) {
	row := map[string]any{"entity": map[string]any{"track": map[string]any{"id": "6850116360324810753", "name": "Song", "artists": []any{map[string]any{"name": "Artist"}}}}}
	groups := []any{map[string]any{"data": []any{row}}, map[string]any{"data": []any{row}}}
	if tracks := parseQishui(groups, 8); len(tracks) != 1 {
		t.Fatal("same track repeated in top results and song group", tracks)
	}
}

func TestKugouAlbumCoverPreferredAndPortraitUsesLargeVersion(t *testing.T) {
	portrait := "http://singerimg.kugou.com/uploadpic/softhead/{size}/20230804/a.jpg"
	for _, albumField := range []string{"album_sizable_cover", "AlbumImage", ""} {
		row := map[string]any{"FileHash": "43F99722D7E04663F24998D9922088B5", "OriSongName": "传奇", "SingerName": "郭沁", "Image": portrait, "sizable_cover": portrait}
		want := "https://singerimg.kugou.com/uploadpic/softhead/2048/20230804/a.jpg"
		if albumField != "" {
			row[albumField] = "http://imge.kugou.com/stdmusic/{size}/album.jpg"
			want = "https://imge.kugou.com/stdmusic/0/album.jpg"
		}
		tracks := parseKugou([]any{row}, 8)
		if len(tracks) != 1 || tracks[0].CoverURL == nil || *tracks[0].CoverURL != want {
			t.Fatalf("wrong cover for album field %q: %+v", albumField, tracks)
		}
	}
}
