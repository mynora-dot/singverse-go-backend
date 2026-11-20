package downloads

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Search and collection endpoints use different names for the same artwork.
// Empty album fields must not hide the usable fallback supplied alongside them.
func kugouCatalogCover(row map[string]any) string {
	for _, raw := range []string{catalogText(row, "AlbumImage"), catalogText(row, "Image"), catalogText(row, "imgurl"), catalogText(row["trans_param"], "union_cover")} {
		if cover := publicCatalogArtwork(strings.ReplaceAll(raw, "{size}", "400"), "kugou.com", "kugoucdn.com", "kgimg.com"); cover != "" {
			return cover
		}
	}
	return ""
}

var kuwoCatalogAlbumPath = regexp.MustCompile(`^[0-9]+/[A-Za-z0-9_/-]+\.(jpg|jpeg|png)$`)

func kuwoCatalogCover(row map[string]any) string {
	for _, field := range []string{"albumpic", "pic", "img"} {
		if cover := publicCatalogArtwork(catalogText(row, field), "kuwo.cn"); cover != "" {
			return cover
		}
	}
	path := catalogText(row, "web_albumpic_short")
	if !kuwoCatalogAlbumPath.MatchString(path) || strings.Contains(path, "..") {
		return ""
	}
	return "https://img1.kuwo.cn/star/albumcover/500/" + strings.SplitN(path, "/", 2)[1]
}

func publicCatalogArtwork(raw string, httpsHosts ...string) string {
	cover := safeCatalogCover(strings.TrimSpace(raw))
	if cover == "" {
		return ""
	}
	u, _ := url.Parse(cover)
	for _, host := range httpsHosts {
		if u.Scheme == "http" && (u.Hostname() == host || strings.HasSuffix(u.Hostname(), "."+host)) {
			u.Scheme = "https"
			break
		}
	}
	return u.String()
}

// The lightweight NetEase search omits picUrl and quality metadata. Fetch the
// page's details in one request, retaining search order and partial results.
func (c *publicCatalog) completeNeteaseSearch(ctx context.Context, page CatalogPage) CatalogPage {
	ids := []any{}
	for _, track := range page.Tracks {
		if catalogDigits.MatchString(track.RemoteID) && (track.CoverURL == "" || len(asArray(at(track.MusicInfo, "meta", "qualitys"))) == 0) {
			ids = append(ids, map[string]any{"id": json.Number(track.RemoteID)})
		}
	}
	if len(ids) == 0 {
		return page
	}
	root, err := c.get(ctx, catalogURL("https://music.163.com/api/v3/song/detail", "c", encode(ids)), "wy")
	details := map[string]Track{}
	if err == nil {
		for _, row := range asArray(root["songs"]) {
			if track, e := mapCatalogTrack("wy", asObject(row)); e == nil {
				details[track.RemoteID] = track
			}
		}
	}
	missing := false
	for i, track := range page.Tracks {
		if detail, ok := details[track.RemoteID]; ok {
			if detail.CoverURL == "" {
				detail.CoverURL = track.CoverURL
				detail.MusicInfo = sourceMusicInfo(detail)
			}
			page.Tracks[i] = detail
		}
		missing = missing || page.Tracks[i].CoverURL == ""
	}
	if missing {
		if page.Warning != "" {
			page.Warning += "；"
		}
		page.Warning += "部分歌曲的封面或音质信息暂不可用，可稍后重新搜索"
	}
	return page
}

// Some Kuwo search hits have no album image at all. Its picture endpoint can
// still identify the song artwork; optional lookups have a shared short budget.
func (c *publicCatalog) completeKuwoSearch(ctx context.Context, page CatalogPage) CatalogPage {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, track := range page.Tracks {
		if track.CoverURL != "" || !catalogDigits.MatchString(track.RemoteID) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			address := catalogURL("https://artistpicserver.kuwo.cn/pic.web", "corp", "kuwo", "type", "rid_pic", "pictype", 500, "size", 500, "rid", track.RemoteID)
			reply, err := c.network.Request(ctx, address, "GET", nil, nil, 4096, true)
			if err != nil || reply.Status != 200 {
				return
			}
			cover := publicCatalogArtwork(string(reply.Bytes), "kuwo.cn")
			u, err := url.Parse(cover)
			if err != nil || !strings.HasSuffix(u.Hostname(), ".kuwo.cn") {
				return
			}
			page.Tracks[i].CoverURL = cover
			page.Tracks[i].MusicInfo = sourceMusicInfo(page.Tracks[i])
		}()
	}
	wg.Wait()
	return page
}
