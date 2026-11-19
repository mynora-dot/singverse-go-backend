package metadata

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/qishui"
)

const qishuiCarFixture = `{"err_no":0,"data":{"result_groups":[{"id":"tracks","data":[{"entity":{"track":{"base_info":{"id":6850116360324810753,"name":"Song","duration_ms":257254},"related_info":{"artist_links":[{"id":6807264292040083458,"name":"Artist"}],"album_link":{"name":"Album","release_date":1594944000}},"display_info":{"cover_url":{"uri":"tos-cn-v-2774c002/cover","urls":["https://p3-luna.douyinpic.com/img/"],"template_prefix":"tplv-b829550vbb"}}}}}]}]}}`

// Opt-in real catalogue and cover check; it never opens a business database.
func TestLiveQishuiSearchAndCover(t *testing.T) {
	if os.Getenv("SINGVERSE_QISHUI_LIVE_TEST") != "1" {
		t.Skip("set SINGVERSE_QISHUI_LIVE_TEST=1 to query the public catalogue")
	}
	client, err := qishui.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	provider := &musicProvider{service: &Service{qishui: client}, name: "QISHUI"}
	for _, query := range []string{"传奇 郭沁", "晴天 周杰伦"} {
		t.Run(query, func(t *testing.T) {
			tracks, err := provider.Search(context.Background(), query, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(tracks) == 0 {
				t.Fatal("live search returned no parseable candidates")
			}
			first := tracks[0]
			t.Logf("query=%s candidates=%d first=%s artists=%v id=%s", query, len(tracks), *first.Title, first.Artists, *first.ExternalID)
			if query == "传奇 郭沁" && (*first.Title != "传奇" || len(first.Artists) == 0 || first.Artists[0] != "郭沁") {
				t.Fatal("expected 郭沁《传奇》 as the first candidate")
			}
			if first.CoverURL == nil {
				t.Fatal("first candidate has no accepted cover URL")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			u, _ := url.Parse(*first.CoverURL)
			imageClient, err := safeClient(ctx, u, false)
			if err != nil {
				t.Fatal(err)
			}
			defer imageClient.CloseIdleConnections()
			cover, err := fetchCover(ctx, imageClient, u.String())
			if err != nil {
				t.Fatal(err)
			}
			if cover.Width < 600 || cover.Height < 600 {
				t.Fatal("live cover is a small thumbnail", cover.Width, cover.Height)
			}
			t.Logf("cover=%dx%d mime=%s bytes=%d", cover.Width, cover.Height, cover.MIME, len(cover.Bytes))
		})
	}
}

func TestQishuiRejectedSearchKeepsMiguCandidatesAndSafeDetail(t *testing.T) {
	f := setup(t)
	_, ids := f.local(t, "Artist-Song.mkv")
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: true, Providers: []string{"QISHUI", "MIGU"}, Threshold: .95}, 200)
	f.s.client.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
		body, status := miguSearchFixture, 200
		if r.URL.Hostname() == "open.qishui.com" {
			body, status = "private-token-and-cookie", 403
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	result := request(t, f.router, "POST", "/api/v1/metadata/songs/"+ids[0]+"/search", SearchInput{Providers: []string{"QISHUI", "MIGU"}}, 200)
	message := text(path(array(result["errors"])[0], "message"))
	if len(array(result["candidates"])) != 1 || !strings.Contains(message, "HTTP 403") || strings.Contains(message, "private-token") {
		t.Fatal("upstream error was hidden or leaked details", result)
	}
}

type contextProvider func(context.Context) ([]Track, error)

func (fn contextProvider) Search(ctx context.Context, _ string, _ int) ([]Track, error) {
	return fn(ctx)
}

func TestQishuiGuardIncludesTokenRefreshBudget(t *testing.T) {
	f := setup(t)
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: true, Providers: []string{"QISHUI"}, Threshold: .95}, 200)
	_, err := f.s.callProvider(context.Background(), "QISHUI", contextProvider(func(ctx context.Context) ([]Track, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 12*time.Second {
			t.Error("anonymous catalogue still has the old five-second budget")
		}
		return nil, nil
	}), "Song")
	if err != nil {
		t.Fatal(err)
	}
}
