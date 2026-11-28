package downloads

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

type catalogSearchNetwork func(*url.URL, string, []byte) (Reply, error)

func (f catalogSearchNetwork) Request(_ context.Context, address, method string, _ map[string]string, body []byte, _ int, _ bool) (Reply, error) {
	u, err := url.Parse(address)
	if err != nil {
		return Reply{}, err
	}
	return f(u, method, body)
}

func TestCatalogCoverVariantsFallBackToOriginal(t *testing.T) {
	for _, tc := range []struct{ name, original, preferred string }{
		{"NetEase resized mirror", "https://p1.music.126.net/key/cover.jpg", "https://p2.music.126.net/key/cover.jpg?param=400y400"},
		{"Kuwo mirror", "https://img1.kuwo.cn/star/albumcover/120/cover.jpg", "https://img4.kuwo.cn/star/albumcover/120/cover.jpg"},
		{"Qishui resized", "https://p3-luna.douyinpic.com/img/test~tplv-example-image.image", "https://p3-luna.douyinpic.com/img/test~tplv-example-resize:480:480.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := []string{}
			network := catalogSearchNetwork(func(u *url.URL, _ string, _ []byte) (Reply, error) {
				calls = append(calls, u.String())
				return Reply{Status: http.StatusNotFound}, nil
			})
			_, _, err := fetchCatalogCover(t.Context(), network, tc.original)
			if err == nil || !slices.Equal(calls, []string{tc.preferred, tc.original}) {
				t.Fatalf("cover attempts=%v error=%v", calls, err)
			}
		})
	}
}

func TestCatalogSearchMapsSixPlatformsAndProxiesArtwork(t *testing.T) {
	const query = "与天齐"
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		platform, path, queryParam, remoteID, artwork, response string
	}{
		{"wy", "/api/cloudsearch/pc", "s", "19", "https://p1.music.126.net/cover.jpg",
			`{"code":200,"result":{"songCount":31,"songs":[{"id":19,"name":"与天齐","ar":[{"name":"歌手"}],"al":{"name":"专辑"},"dt":180000}]}}`},
		{"tx", "/soso/fcgi-bin/client_search_cp", "w", "001Song", "https://y.gtimg.cn/music/photo_new/T002R300x300M000001Album.jpg",
			`{"code":0,"subcode":0,"data":{"song":{"totalnum":31,"curpage":1,"list":[{"songmid":"001Song","songname":"与天齐","singer":[{"name":"歌手"}],"albummid":"001Album","albumname":"专辑","interval":180}]}}}`},
		{"kg", "/v3/search/song", "keyword", "0123456789ABCDEF0123456789ABCDEF", "https://imge.kugou.com/stdmusic/400/cover.jpg",
			`{"status":1,"error_code":0,"data":{"total":31,"page":1,"lists":[{"FileHash":"0123456789abcdef0123456789abcdef","FileName":"歌手 - 与天齐","Singers":[{"name":"歌手"}],"AlbumName":"专辑","Duration":180,"AlbumImage":"","Image":"http://imge.kugou.com/stdmusic/{size}/cover.jpg","HQ":{"Hash":"0123456789abcdee0123456789abcdef","FileSize":123}}]}}`},
		{"kw", "/r.s", "all", "29", "https://img1.kuwo.cn/star/albumcover/120/example/cover.jpg",
			`{'TOTAL':'31','abslist':[{'MUSICRID':'MUSIC_29','SONGNAME':'与天齐','ARTIST':'歌手','ALBUM':'专辑','DURATION':'180','webAlbumpicShort':'120/example/cover.jpg'}]}`},
		{"mg", "/MIGUM2.0/v1.0/content/search_all.do", "text", "39", "https://d.musicapp.migu.cn/example/cover.jpg",
			`{"code":"000000","songResultData":{"totalCount":"31","result":[[{"id":"39","copyrightId":"1234","name":"与天齐","singers":[{"name":"歌手"}],"albums":[{"id":"2","name":"专辑"}],"duration":"03:00","imgItems":[{"img":""},{"img":"/example/cover.jpg"}]}]]}}`},
		{"qs", "/api/luna/v1/platform/search/", "q", "7777777777777777777", "https://p3-luna.douyinpic.com/img/abc/cover~example-image.image",
			`{"data":{"result_groups":[{"id":"tracks","has_more":true,"next_cursor":16,"data":[{"entity":{"track_wrapper":{"track":{"id":7777777777777777777,"name":"与天齐","artists":[{"name":"歌手"}],"album":{"name":"专辑"},"duration":180000,"url_cover":{"urls":["https://p3-luna.douyinpic.com/img/"],"uri":"abc/cover","template_prefix":"example"}}}}}]}]}}`},
	} {
		t.Run(fixture.platform, func(t *testing.T) {
			network := catalogSearchNetwork(func(u *url.URL, method string, body []byte) (Reply, error) {
				jsonReply := func(body string) Reply { return Reply{Status: http.StatusOK, Bytes: []byte(body)} }
				switch {
				case u.String() == fixture.artwork:
					return Reply{Status: http.StatusOK, Bytes: imageData.Bytes()}, nil
				case fixture.platform == "wy" && u.Path == "/api/v3/song/detail":
					return jsonReply(`{"code":200,"songs":[{"id":19,"name":"同 ID 详情标题","ar":[{"name":"歌手"}],"al":{"name":"专辑","picUrl":"https://p1.music.126.net/cover.jpg"},"dt":180000,"l":{"size":1234}}]}`), nil
				case fixture.platform == "qs" && strings.HasSuffix(u.Path, "/client/token/get/"):
					return jsonReply(`{"client_token":"fixture-token"}`), nil
				case u.Path == fixture.path:
					values, _ := url.ParseQuery(string(body))
					if fixture.platform == "qs" && !bytes.Contains(body, []byte(`"q":"与天齐"`)) ||
						fixture.platform == "wy" && (method != http.MethodPost || values.Get(fixture.queryParam) != query || values.Get("offset") != "0") ||
						method == http.MethodGet && u.Query().Get(fixture.queryParam) != query {
						return Reply{}, fmt.Errorf("search parameters did not include the keyword")
					}
					return jsonReply(fixture.response), nil
				default:
					return Reply{}, fmt.Errorf("unexpected request path %s", u.Path)
				}
			})
			result, err := newCatalog(network).Search(t.Context(), fixture.platform, query, 1)
			if err != nil || len(result.Tracks) != 1 || !result.HasMore {
				t.Fatalf("tracks=%d more=%t error=%v", len(result.Tracks), result.HasMore, err)
			}
			track := result.Tracks[0]
			if track.Platform != fixture.platform || track.RemoteID != fixture.remoteID || track.Title != query ||
				len(track.Artists) != 1 || track.Artists[0] != "歌手" || track.Album != "专辑" ||
				track.DurationMS == nil || *track.DurationMS != 180000 || track.CoverURL != fixture.artwork {
				t.Fatalf("unexpected metadata: %+v", track)
			}
			if fixture.platform == "kg" && catalogText(at(track.MusicInfo, "meta", "_qualitys", "320k"), "hash") == "" {
				t.Fatal("missing gateway quality hash")
			}
			covers := newCovers(network)
			reference := covers.Reference(track.CoverURL)
			if reference == nil || !strings.HasPrefix(*reference, "/api/v1/lx-downloads/covers/") {
				t.Fatal("missing same-origin cover URL")
			}
			data, mime, err := covers.Get(t.Context(), strings.TrimPrefix(*reference, "/api/v1/lx-downloads/covers/"))
			if err != nil || mime != "image/png" || !bytes.Equal(data, imageData.Bytes()) {
				t.Fatalf("unexpected cover: %s, %d bytes, %v", mime, len(data), err)
			}
		})
	}
}

func TestCatalogSearchDistinguishesEmptyAndUnrecognizedNeteaseResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"unrecognized", `{"code":200,"result":"unexpected opaque result"}`, true},
		{"missing songs", `{"code":200,"result":{"songCount":20}}`, true},
		{"empty", `{"code":200,"result":{"songCount":0}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network := catalogSearchNetwork(func(*url.URL, string, []byte) (Reply, error) {
				return Reply{Status: http.StatusOK, Bytes: []byte(tc.body)}, nil
			})
			result, err := newCatalog(network).Search(t.Context(), "wy", "query", 1)
			if (err != nil) != tc.wantError || len(result.Tracks) > 0 || result.HasMore {
				t.Fatalf("tracks=%d more=%t error=%v", len(result.Tracks), result.HasMore, err)
			}
		})
	}
}
