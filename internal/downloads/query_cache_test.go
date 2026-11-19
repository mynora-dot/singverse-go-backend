package downloads

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestPublicCatalogQueryCacheSkipsNetworkAndSeparatesPages(t *testing.T) {
	network := &catalogFixture{route: func(u *url.URL) (any, error) { return catalogWrap("tx", catalogRow("tx"), "search"), nil }}
	c := &publicCatalog{network: network}
	first, err := c.Search(t.Context(), "tx", "query", 1)
	if err != nil {
		t.Fatal(err)
	}
	first.Tracks[0].MusicInfo["name"] = "edited"
	second, err := c.Search(t.Context(), "tx", "query", 1)
	if err != nil || network.calls != 1 || second.Tracks[0].MusicInfo["name"] == "edited" {
		t.Fatal(second, network.calls, err)
	}
	if _, err := c.Search(t.Context(), "tx", "query", 2); err != nil || network.calls != 2 {
		t.Fatal("page key collided", network.calls, err)
	}
	if _, err := c.Search(t.Context(), "tx", "other", 1); err != nil || network.calls != 3 {
		t.Fatal("query key collided", network.calls, err)
	}
	network.route = func(*url.URL) (any, error) { return nil, errors.New("unavailable") }
	for range 2 {
		if _, err := c.Search(t.Context(), "tx", "failed", 1); err == nil {
			t.Fatal("failure lost")
		}
	}
	if network.calls != 5 {
		t.Fatal("failed query cached", network.calls)
	}
}

func TestPublicCatalogCachesOnlyExternalDataAndValidReferences(t *testing.T) {
	network := &catalogFixture{route: func(u *url.URL) (any, error) {
		if u.Hostname() == "qm.qq.com" {
			return Reply{Status: 200, URL: "https://y.qq.com/n/ryqq/playlist/456"}, nil
		}
		if u.Path == "/lyric/fcgi-bin/fcg_query_lyric_new.fcg" {
			return map[string]any{"code": 0, "lyric": "[00:01]line"}, nil
		}
		if u.Path == "/v8/fcg-bin/fcg_play_single_song.fcg" {
			return catalogWrap("tx", catalogRow("tx"), "detail"), nil
		}
		return catalogWrap("tx", catalogRow("tx"), "playlist"), nil
	}}
	c := &publicCatalog{network: network}
	for range 2 {
		ref, err := c.Reference(t.Context(), "https://qm.qq.com/q/fixture")
		if err != nil {
			t.Fatal(err)
		}
		page, err := c.Collection(t.Context(), ref, 1)
		if err != nil || len(page.Tracks) != 1 {
			t.Fatal(page, err)
		}
		track, err := c.Detail(t.Context(), page.Tracks[0])
		if err != nil {
			t.Fatal(err)
		}
		if text, err := c.Lyrics(t.Context(), track); err != nil || text != "[00:01]line" {
			t.Fatal(text, err)
		}
	}
	if network.calls != 4 {
		t.Fatal("reference, collection, detail or lyrics hit fetched upstream", network.calls)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Reference(canceled, "https://qm.qq.com/q/fixture"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
