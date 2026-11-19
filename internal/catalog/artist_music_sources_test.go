package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/metadata"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/qishui"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

func musicArtworkClient(t *testing.T, provider, name, title string, failure bool) *http.Client {
	t.Helper()
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		if provider == "KUWO" && r.URL.Hostname() == "search.kuwo.cn" {
			if r.URL.Query().Get("ft") == "artist" {
				body = fmt.Sprintf(`{'TOTAL':'2','BASEPICPATH':'http://img1.kuwo.cn/star/starheads/','abslist':[{'ARTIST':'%s','ARTISTID':'947','PICPATH':'240/portrait.jpg'},{'ARTIST':'%s','ARTISTID':'948','PICPATH':'240/other.jpg'}]}`, name, name)
			} else {
				body = fmt.Sprintf(`{'TOTAL':'1','abslist':[{'SONGNAME':'%s','ARTISTID':'947','PICPATH':'album.jpg'}]}`, title)
			}
		} else if provider == "QISHUI" && r.URL.Hostname() == "open.qishui.com" {
			body = `{"client_token":"anonymous-token"}`
		} else if provider == "QISHUI" && r.URL.Hostname() == "open.douyin.com" {
			var query map[string]any
			if json.NewDecoder(r.Body).Decode(&query) != nil {
				t.Fatal("invalid car search body")
			}
			if query["search_type"] == "artist" {
				body = fmt.Sprintf(`{"data":{"result_groups":[{"id":"artists","data":[{"entity":{"artist":{"base_info":{"id":6807264292040083458,"name":"%s","url_avatar":{"uri":"tos-cn-v-2774c002/portrait","urls":["https://p3-luna.douyinpic.com/img/"],"template_prefix":"tplv-b829550vbb"}}}}},{"entity":{"artist":{"base_info":{"id":"7288900311811754021","name":"%s","url_avatar":{"uri":"tos-cn-v-2774c002/other","urls":["https://p6-luna.douyinpic.com/img/"],"template_prefix":"tplv-b829550vbb"}}}}}]}]}}`, name, name)
			} else if query["search_type"] == "track" {
				body = fmt.Sprintf(`{"data":{"result_groups":[{"id":"tracks","data":[{"entity":{"track":{"base_info":{"name":"%s"},"related_info":{"artist_links":[{"id":6807264292040083458,"name":"%s"}],"album_link":{"cover":"https://p3-luna.douyinpic.com/album.jpg"}}}}}]}]}}`, title, name)
			} else {
				t.Fatal("unexpected qishui endpoint", r.URL)
			}
		} else if has(r.URL.Hostname(), "img1.kuwo.cn", "p3-luna.douyinpic.com", "singerimg.kugou.com") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(picture.Bytes()))}, nil
		} else {
			t.Fatal("explicit platform fell back or requested an album image", r.URL)
		}
		// Keep this fixture fast while retaining the real per-host request gate.
		if provider == "KUWO" {
			sourceGate.Lock()
			sourceGate.next[r.URL.Hostname()] = time.Time{}
			sourceGate.Unlock()
		}
		if failure {
			body = `{"err_no":28001003,"status_code":403,"message":"login required"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func qishuiArtworkClient(t *testing.T, api *http.Client) *qishui.Client {
	t.Helper()
	client, err := qishui.New(api)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestQishuiArtistAndSongUseTheSharedAnonymousToken(t *testing.T) {
	api := musicArtworkClient(t, "QISHUI", "Artist", "Song", false)
	transport := api.Transport
	tokens := 0
	api.Transport = artworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "open.qishui.com" {
			tokens++
		}
		return transport.RoundTrip(r)
	})
	s := &Service{qishui: qishuiArtworkClient(t, api)}
	choices, err := s.findMusicArtwork(context.Background(), "QISHUI", "Artist", []string{"Song"}, nil)
	if err != nil || len(choices) != 1 || !choices[0].Verified || choices[0].ExternalID != "6807264292040083458" || tokens != 1 {
		t.Fatal("anonymous artist verification failed", choices, tokens, err)
	}
}

func TestLiveQishuiArtistSearch(t *testing.T) {
	if os.Getenv("SINGVERSE_QISHUI_LIVE_TEST") != "1" {
		t.Skip("set SINGVERSE_QISHUI_LIVE_TEST=1 to query the public catalogue")
	}
	s := &Service{qishui: qishuiArtworkClient(t, nil)}
	choices, err := s.findMusicArtwork(context.Background(), "QISHUI", "郭沁", []string{"传奇"}, nil)
	if err != nil || len(choices) != 1 || !choices[0].Verified || choices[0].ExternalID != "6935546743946041346" || choices[0].Portrait == "" {
		t.Fatal("live artist identity verification failed", choices, err)
	}
	t.Logf("artist=郭沁 id=%s verified=%v portrait=%v", choices[0].ExternalID, choices[0].Verified, choices[0].Portrait != "")
}

func TestMusicArtistArtworkRequiresIdentity(t *testing.T) {
	prior := sourceClient
	t.Cleanup(func() { sourceClient = prior })
	for _, provider := range []string{"KUWO", "QISHUI"} {
		for _, mode := range []string{"matching song", "unverified", "source failure", "wrong name", "conflicting IDs"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				sourceClient = musicArtworkClient(t, provider, "Artist", map[bool]string{true: "Song", false: "Different song"}[mode == "matching song"], mode == "source failure")
				var refs []metadata.ArtistRef
				if mode == "conflicting IDs" {
					refs = []metadata.ArtistRef{{Provider: provider, ID: "947", Name: "Artist"}, {Provider: provider, ID: "948", Name: "Artist"}}
				}
				name := "Artist"
				if mode == "wrong name" {
					name = "Someone else"
				}
				s := &Service{qishui: qishuiArtworkClient(t, sourceClient)}
				chosen, address, data, review, err := s.lookupArtwork(context.Background(), name, []string{"Song"}, refs, "PORTRAIT", provider)
				if mode == "source failure" {
					if err == nil || data != nil {
						t.Fatal("upstream failure became no-match or success", chosen, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "wrong name" {
					if data != nil || len(review) != 0 {
						t.Fatal("wrong identity applied")
					}
					return
				}
				if mode == "matching song" {
					if chosen == nil || !chosen.Verified || chosen.Provider != provider || len(data) == 0 || len(review) != 0 || !strings.HasPrefix(address, "https://") || strings.Contains(address, "album") {
						t.Fatal(chosen, address, len(data), review)
					}
					if provider == "QISHUI" && (chosen.ExternalID != "6807264292040083458" || !strings.HasSuffix(address, "-resize:640:640.png")) {
						t.Fatal("rounded ID or invalid thumbnail", chosen)
					}
				} else if data != nil || len(review) != 2 || review[0].Verified {
					t.Fatal("unverified artist bypassed review", chosen, len(data), review)
				}
			})
		}
	}
}

func TestQishuiPortraitRejectsForeignURLsAndTemplates(t *testing.T) {
	for _, uri := range []string{"../private", "https://evil.example/x", "image?url=private", ""} {
		if v := qishuiPortrait(map[string]any{"uri": uri, "template_prefix": "tplv-b829550vbb", "urls": []any{"https://p3-luna.douyinpic.com/img/"}}); v != "" {
			t.Fatal("untrusted URI accepted", v)
		}
	}
	for _, base := range []string{"http://p3-luna.douyinpic.com/img/", "https://evil.example/img/", "https://p3-luna.douyinpic.com:8443/img/", "https://user@p3-luna.douyinpic.com/img/"} {
		if v := qishuiPortrait(map[string]any{"uri": "portrait", "template_prefix": "tplv-b829550vbb", "urls": []any{base}}); v != "" {
			t.Fatal("untrusted base accepted", v)
		}
	}
}

func TestPublicArtistArtworkBatchSQLite(t *testing.T) {
	for _, provider := range []string{"KUGOU", "KUWO", "QISHUI"} {
		t.Run(provider, func(t *testing.T) { testPublicArtistArtworkBatch(t, provider, false) })
	}
}
func TestMySQLPublicArtistArtworkBatch(t *testing.T) {
	for _, provider := range []string{"KUGOU", "KUWO", "QISHUI"} {
		t.Run(provider, func(t *testing.T) { testPublicArtistArtworkBatch(t, provider, true) })
	}
}
func testPublicArtistArtworkBatch(t *testing.T, provider string, mysql bool) {
	db, dialect := testutil.Database(t), "sqlite"
	if mysql {
		db, _ = testutil.MySQLDatabase(t)
		dialect = "mysql"
	}
	s, r := batchFixture(t, db, dialect)
	if provider == "KUGOU" {
		s.SetArtistProvider(batchArtistProvider{enabled: []string{"KUGOU"}, call: func(name, selected string) (*metadata.Artwork, error) {
			if selected != "KUGOU" {
				t.Fatal("explicit provider lost", selected)
			}
			return &metadata.Artwork{Candidates: []metadata.Artwork{{Provider: "KUGOU", ExternalID: "3060", Portrait: "https://singerimg.kugou.com/portrait.jpg"}, {Provider: "KUGOU", ExternalID: "759405", Portrait: "https://singerimg.kugou.com/other.jpg"}}}, nil
		}})
	}
	prior := sourceClient
	t.Cleanup(func() { sourceClient = prior })
	sourceClient = musicArtworkClient(t, provider, "周杰伦", "Different song", false)
	s.qishui = qishuiArtworkClient(t, sourceClient)
	ctx := context.Background()
	platforms := request(t, r, "GET", "/api/v1/artists/artwork-platforms", nil, 200)
	if !strings.Contains(encode(platforms), provider) {
		t.Fatal("public source unavailable", platforms)
	}
	batch := request(t, r, "POST", "/api/v1/artists/artwork-batches", ArtworkBatchStart{RequestID: id(), Provider: provider, ArtistIDs: []string{artistID("周杰伦")}}, 201)
	if !s.processArtworkBatch(ctx) {
		t.Fatal("batch not processed")
	}
	view := request(t, r, "GET", "/api/v1/artists/artwork-batches/"+str(batch["id"]), nil, 200)
	if num(obj(view["counts"])["REVIEW"]) != 1 {
		t.Fatal("review not persisted", view)
	}
	choices, err := s.candidates(ctx, s.db, artistID("周杰伦"), "portrait")
	if err != nil || len(choices) != 2 || choices[0].Provider != provider {
		t.Fatal(choices, err)
	}
	request(t, r, "POST", "/api/v1/artists/"+artistID("周杰伦")+"/images/portrait/selection", map[string]any{"provider": provider, "externalId": choices[0].ExternalID}, 200)
	view = request(t, r, "GET", "/api/v1/artists/artwork-batches/"+str(batch["id"]), nil, 200)
	if num(obj(view["counts"])["APPLIED"]) != 1 || num(obj(view["counts"])["REVIEW"]) != 0 {
		t.Fatal("confirmation did not resolve batch", view)
	}
	artist := request(t, r, "GET", "/api/v1/artists/"+artistID("周杰伦"), nil, 200)
	if artist["portraitPath"] == nil || str(artist["portraitOrigin"]) != "SELECTED" {
		t.Fatal("confirmed portrait not saved", artist)
	}
}
