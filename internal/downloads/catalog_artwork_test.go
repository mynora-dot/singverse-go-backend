package downloads

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestCatalogArtworkFromSearchFields(t *testing.T) {
	for _, test := range []struct {
		name, platform string
		fields         map[string]any
		want           string
	}{
		{"kugou image after empty album", "kg", map[string]any{"AlbumImage": "", "Image": "http://imge.kugou.com/stdmusic/{size}/cover.jpg"}, "https://imge.kugou.com/stdmusic/400/cover.jpg"},
		{"kugou album priority", "kg", map[string]any{"AlbumImage": "https://imge.kugou.com/album.jpg", "Image": "https://imge.kugou.com/other.jpg"}, "https://imge.kugou.com/album.jpg"},
		{"kugou collection", "kg", map[string]any{"imgurl": "//imge.kugou.com/stdmusic/{size}/cover.jpg"}, "https://imge.kugou.com/stdmusic/400/cover.jpg"},
		{"kugou union cover", "kg", map[string]any{"trans_param": map[string]any{"union_cover": "http://imge.kugou.com/stdmusic/{size}/cover.jpg"}}, "https://imge.kugou.com/stdmusic/400/cover.jpg"},
		{"kuwo relative album", "kw", map[string]any{"web_albumpic_short": "120/s4s48/11/4091686476.png"}, "https://img1.kuwo.cn/star/albumcover/500/s4s48/11/4091686476.png"},
		{"kuwo direct after empty field", "kw", map[string]any{"albumpic": "", "pic": "http://img2.kuwo.cn/star/albumcover/500/a.jpg"}, "https://img2.kuwo.cn/star/albumcover/500/a.jpg"},
		{"kuwo no artist substitution", "kw", map[string]any{"web_artistpic_short": "120/artist.jpg", "MVPIC": "120/video.jpg"}, ""},
		{"kuwo unsafe path", "kw", map[string]any{"web_albumpic_short": "120/../private.jpg"}, ""},
		{"private cover", "kg", map[string]any{"Image": "http://127.0.0.1/cover.jpg"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := catalogRow(test.platform)
			for key, value := range test.fields {
				row[key] = value
			}
			track, err := mapCatalogTrack(test.platform, row)
			if err != nil || track.CoverURL != test.want {
				t.Fatalf("cover=%q want=%q error=%v", track.CoverURL, test.want, err)
			}
		})
	}
}

func TestNeteaseSearchCompletesPageInOneRequest(t *testing.T) {
	first, second := catalogRow("wy"), catalogRow("wy")
	second["id"] = 102
	second["name"] = "第二首"
	detail := catalogRow("wy")
	detail["al"] = map[string]any{"id": 3, "name": "测试专辑", "picUrl": "https://p1.music.126.net/cover.jpg"}
	detail["l"] = map[string]any{"size": 1000}
	detail["h"] = map[string]any{"size": 2000}
	detail["sq"] = map[string]any{"size": 3000}
	detail["hr"] = nil
	unrelated := catalogRow("wy")
	unrelated["id"] = 999
	network := &catalogFixture{route: func(u *url.URL) (any, error) {
		if u.Path == "/api/search/get/web" {
			return map[string]any{"code": 200, "result": map[string]any{"songs": []any{first, second}, "songCount": 60}}, nil
		}
		if u.Path != "/api/v3/song/detail" || u.Query().Get("c") != `[{"id":101},{"id":102}]` {
			t.Fatal("unexpected detail request", u)
		}
		return map[string]any{"code": 200, "songs": []any{unrelated, second, detail}}, nil
	}}
	c := &publicCatalog{network: network}
	for range 2 {
		page, err := c.Search(t.Context(), "wy", "测试", 1)
		if err != nil || len(page.Tracks) != 2 || !page.HasMore {
			t.Fatal(page, err)
		}
		if page.Tracks[0].RemoteID != "101" || page.Tracks[1].RemoteID != "102" || page.Tracks[0].CoverURL == "" {
			t.Fatal("search order or artwork lost", page)
		}
		q := asObject(at(page.Tracks[0].MusicInfo, "meta", "_qualitys"))
		if len(q) != 3 || integer(at(q, "flac", "size"), 0) != 3000 || q["flac24bit"] != nil {
			t.Fatal("quality metadata missing", q)
		}
	}
	if network.calls != 2 {
		t.Fatal("page details should be batched and cached", network.calls)
	}
}

func TestNeteaseSearchRetainsResultsWhenDetailsUnavailable(t *testing.T) {
	for _, failed := range []bool{false, true} {
		network := &catalogFixture{route: func(u *url.URL) (any, error) {
			if u.Path == "/api/search/get/web" {
				return catalogWrap("wy", catalogRow("wy"), "search"), nil
			}
			if failed {
				return nil, errors.New("temporarily unavailable")
			}
			return map[string]any{"code": 200, "songs": []any{}}, nil
		}}
		c := &publicCatalog{network: network}
		page, err := c.Search(t.Context(), "wy", "测试", 1)
		if err != nil || len(page.Tracks) != 1 || page.Tracks[0].Title != "测试歌曲" || !strings.Contains(page.Warning, "暂不可用") {
			t.Fatal(page, err)
		}
	}
}

func TestKuwoSearchResolvesOnlyMissingSongArtwork(t *testing.T) {
	for _, picture := range []string{"http://img4.kuwo.cn/pic_music/500/cover.jpg", "http://127.0.0.1/cover.jpg", "https://img4.kuwo.cn.evil.invalid/cover.jpg", "", "<html>unavailable</html>"} {
		t.Run(picture, func(t *testing.T) {
			first, second := catalogRow("kw"), catalogRow("kw")
			first["web_albumpic_short"] = "120/album.jpg"
			second["MUSICRID"] = "MUSIC_102"
			network := &catalogFixture{route: func(u *url.URL) (any, error) {
				if u.Hostname() == "search.kuwo.cn" {
					return map[string]any{"TOTAL": 2, "abslist": []any{first, second}}, nil
				}
				if u.Hostname() != "artistpicserver.kuwo.cn" || u.Query().Get("rid") != "102" || u.Query().Get("type") != "rid_pic" {
					t.Fatal("unexpected picture lookup", u)
				}
				return Reply{Status: 200, Bytes: []byte(picture)}, nil
			}}
			c := &publicCatalog{network: network}
			page, err := c.Search(t.Context(), "kw", "测试", 1)
			if err != nil || len(page.Tracks) != 2 || network.calls != 2 {
				t.Fatal(page, network.calls, err)
			}
			want := ""
			if picture == "http://img4.kuwo.cn/pic_music/500/cover.jpg" {
				want = "https://img4.kuwo.cn/pic_music/500/cover.jpg"
			}
			if page.Tracks[0].CoverURL != "https://img1.kuwo.cn/star/albumcover/500/album.jpg" || page.Tracks[1].CoverURL != want || catalogText(page.Tracks[1].MusicInfo, "img") != want {
				t.Fatal("artwork fallback mismatch", page.Tracks)
			}
		})
	}
}
