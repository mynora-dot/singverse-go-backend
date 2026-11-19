package downloads

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type qishuiFixture struct {
	search   func(string, string, string) (map[string]any, error)
	playlist func(string, string) (map[string]any, error)
	detail   map[string]any
}

func (f qishuiFixture) SearchPage(_ context.Context, q, k, cursor string) (map[string]any, error) {
	return f.search(q, k, cursor)
}
func (f qishuiFixture) Playlist(_ context.Context, id, cursor string) (map[string]any, error) {
	return f.playlist(id, cursor)
}
func (f qishuiFixture) Track(_ context.Context, id string) (map[string]any, error) {
	return f.detail, nil
}
func qishuiRow(id string) map[string]any {
	return map[string]any{"base_info": map[string]any{"id": json.Number(id), "name": "测试歌曲", "duration_ms": 180000}, "related_info": map[string]any{"artist_links": []any{map[string]any{"name": "测试歌手"}}, "album_link": map[string]any{"id": "123", "name": "测试专辑"}}, "display_info": map[string]any{"cover_url": map[string]any{"urls": []any{"https://p3-luna.douyinpic.com/img/"}, "uri": "tos-cn-test/cover", "template_prefix": "tplv-b829550vbb"}}, "player_info": map[string]any{"private_token": "must-not-persist"}}
}
func withQishui(reader qishuiReader) *publicCatalog {
	c := &publicCatalog{}
	c.qsOnce.Do(func() { c.qs = &qishuiCatalog{client: reader, pages: map[string]*qishuiSearchPages{}} })
	return c
}
func TestQishuiSearchKeepsLongIDAndOpaquePagination(t *testing.T) {
	calls := []string{}
	reader := qishuiFixture{search: func(q, k, cursor string) (map[string]any, error) {
		calls = append(calls, cursor)
		id, next, more := "7146244493748996128", "47", true
		if cursor == "47" {
			id, next, more = "7146244493748996129", "48", false
		}
		return map[string]any{"result_groups": []any{map[string]any{"id": "tracks", "has_more": more, "next_cursor": next, "data": []any{map[string]any{"entity": map[string]any{"track_wrapper": map[string]any{"track": qishuiRow(id)}}}}}}}, nil
	}, detail: qishuiRow("7146244493748996128")}
	c := withQishui(reader)
	first, err := c.Search(t.Context(), "qs", "测试", 1)
	if err != nil || len(first.Tracks) != 1 || !first.HasMore {
		t.Fatal(first, err)
	}
	track := first.Tracks[0]
	if track.RemoteID != "7146244493748996128" || track.MusicInfo["songmid"] != track.RemoteID || catalogText(track.MusicInfo["meta"], "songId") != track.RemoteID || track.Title != "测试歌曲" || track.Album != "测试专辑" || track.DurationMS == nil || *track.DurationMS != 180000 || !strings.Contains(track.CoverURL, "/img/tos-cn-test/cover~") {
		t.Fatal(track)
	}
	encoded, _ := json.Marshal(track)
	if strings.Contains(string(encoded), "private_token") || strings.Contains(string(encoded), "must-not-persist") {
		t.Fatal("player secrets entered the catalogue cache")
	}
	next, err := c.Search(t.Context(), "qs", "测试", 2)
	if err != nil || len(next.Tracks) != 1 || next.HasMore || next.Tracks[0].ID == track.ID || strings.Join(calls, ",") != "0,47" {
		t.Fatal(next, err, calls)
	}
	detail, err := c.Detail(t.Context(), track)
	if err != nil || detail.ID != track.ID {
		t.Fatal(detail, err)
	}
}
func TestQishuiSearchRejectsRepeatedCursor(t *testing.T) {
	c := withQishui(qishuiFixture{search: func(q, k, cursor string) (map[string]any, error) {
		return map[string]any{"result_groups": []any{map[string]any{"id": "tracks", "data": []any{}, "has_more": true, "next_cursor": "0"}}}, nil
	}})
	if _, err := c.Search(t.Context(), "qs", "测试", 1); err == nil {
		t.Fatal("accepted a repeated cursor")
	}
}

func TestQishuiCursorWaitDoesNotBlockIndependentCachedQuery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c := withQishui(qishuiFixture{search: func(query, kind, cursor string) (map[string]any, error) {
		if query == "slow" {
			close(started)
			<-release
		}
		return map[string]any{"result_groups": []any{map[string]any{"id": "tracks", "has_more": cursor == "0", "next_cursor": "47", "data": []any{map[string]any{"entity": map[string]any{"track": qishuiRow("7146244493748996128")}}}}}}, nil
	}})
	if _, err := c.Search(t.Context(), "qs", "fast", 1); err != nil {
		t.Fatal(err)
	}
	slow := make(chan error, 1)
	go func() { _, err := c.Search(t.Context(), "qs", "slow", 1); slow <- err }()
	<-started
	fast := make(chan error, 1)
	go func() { _, err := c.Search(t.Context(), "qs", "fast", 2); fast <- err }()
	select {
	case err := <-fast:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-slow
		<-fast
		t.Fatal("independent query blocked behind cursor-state mutex")
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}
func TestQishuiPlaylistPagingAndLinks(t *testing.T) {
	calls := []string{}
	c := withQishui(qishuiFixture{playlist: func(id, cursor string) (map[string]any, error) {
		calls = append(calls, cursor)
		start, more, next := 0, true, "opaque-next"
		if cursor == "opaque-next" {
			start, more, next = 20, false, ""
		}
		rows := []any{}
		for i := start; i < start+20; i++ {
			r := qishuiRow(stringID(int64(100 + i)))
			rows = append(rows, map[string]any{"entity": map[string]any{"media": map[string]any{"track_entity": r}}})
		}
		return map[string]any{"playlist": map[string]any{"base_info": map[string]any{"title": "公开歌单"}, "count_info": map[string]any{"count_tracks": 40}}, "media_entities": rows, "has_more": more, "next_cursor": next}, nil
	}})
	for page := 1; page <= 2; page++ {
		result, err := c.Collection(t.Context(), CatalogReference{"qs", "playlist", "123"}, page)
		if err != nil || len(result.Tracks) != 20 || result.Total != 40 || result.HasMore != (page == 1) || result.Tracks[0].RemoteID != stringID(int64(100+(page-1)*20)) {
			t.Fatal(result, err)
		}
	}
	if strings.Join(calls, ",") != "0,0,opaque-next" {
		t.Fatal(calls)
	}
	if _, err := c.Collection(t.Context(), CatalogReference{"qs", "album", "123"}, 1); err == nil || !strings.Contains(err.Error(), "专辑") {
		t.Fatal("unsupported album did not explain the limitation", err)
	}
	for _, address := range []string{"https://qishui.douyin.com/playlist?playlist_id=123", "https://qishui.douyin.com/playlist/123", "https://www.qishui.com/playlist?id=123"} {
		p, err := catalogPlatform(address)
		ref := parseCatalogReference(address, p)
		if err != nil || p != "qs" || ref == nil || ref.ID != "123" {
			t.Fatal(address, ref, err)
		}
	}
	if _, err := catalogPlatform("https://qishui.com.evil.example/playlist?id=1"); err == nil {
		t.Fatal("accepted lookalike host")
	}
}
func stringID(n int64) string { return strconv.FormatInt(n, 10) }

func TestQishuiHTTPBridgeUsesPublicNetworkAndPreservesJSONNumbers(t *testing.T) {
	network := &catalogFixture{route: func(u *url.URL) (any, error) {
		if strings.HasSuffix(u.Path, "/client/token/get/") {
			return map[string]any{"client_token": "anonymous-token"}, nil
		}
		return map[string]any{"err_no": 0, "data": map[string]any{"result_groups": []any{map[string]any{"id": "tracks", "data": []any{map[string]any{"entity": map[string]any{"track": qishuiRow("7146244493748996128")}}}, "has_more": false}}}}, nil
	}}
	c := &publicCatalog{network: network}
	result, err := c.Search(t.Context(), "qs", "测试", 1)
	if err != nil || len(result.Tracks) != 1 || result.Tracks[0].RemoteID != "7146244493748996128" || network.calls != 2 {
		t.Fatal(result, err, network.calls)
	}
}

type refreshedCapabilities struct{ testRuntime }

func (*refreshedCapabilities) Inspect(context.Context, string, string) (map[string][]string, error) {
	return map[string][]string{"wy": {"128k"}, "qs": {"flac", "256k", "128k"}}, nil
}
func TestRetestingExistingSourceRefreshesStoredCapabilities(t *testing.T) {
	s, _, _ := fixture(t)
	defer s.Close()
	before, err := s.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	source := asObject(before["activeSource"])
	id := str(source, "id")
	s.runtime = &refreshedCapabilities{}
	if _, err = s.Inspect(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	row, err := s.script(t.Context(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := capabilities(row)
	if err != nil || strings.Join(cap["qs"], ",") != "flac,256k,128k" {
		t.Fatal(cap, err)
	}
	after, err := s.Status(t.Context())
	if err != nil || str(asObject(after["activeSource"]), "id") != id {
		t.Fatal("retesting switched source", err)
	}
}

func TestQishuiLyricsConvertsMillisecondsAndWordTiming(t *testing.T) {
	if got := qishuiLRC("[65010,900]<0,400,0>测试<400,500,0>歌词\n[120000,200]第二行"); got != "[01:05.01]测试歌词\n[02:00.00]第二行\n" {
		t.Fatal(got)
	}
	if got := qishuiLRC("[00:01.20]标准歌词"); got != "[00:01.20]标准歌词" {
		t.Fatal(got)
	}
	for _, bad := range []string{"无时间戳", "[999999999999999999999,2]错误", strings.Repeat("a", 256*1024+1)} {
		if qishuiLRC(bad) != "" {
			t.Fatal("accepted invalid lyric timing")
		}
	}
}
