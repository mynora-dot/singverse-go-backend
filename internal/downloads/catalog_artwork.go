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
	return firstCatalogArtwork([]string{"kugou.com", "kugoucdn.com", "kgimg.com"},
		row["AlbumImage"], row["Image"], row["imgurl"], row["image"], at(row, "trans_param", "union_cover"))
}

var kuwoCatalogAlbumPath = regexp.MustCompile(`(?i)^[0-9]+/[A-Za-z0-9_/-]+\.(jpg|jpeg|png|webp)$`)

func kuwoCatalogCover(row map[string]any) string {
	for _, field := range []string{"albumpic", "albumPic", "web_albumpic", "webAlbumpic", "web_albumpic_short", "webAlbumpicShort", "albumpic_short", "pic", "PIC", "MUSICPIC", "img"} {
		raw := catalogNonemptyText(row, field)
		if kuwoCatalogAlbumPath.MatchString(raw) {
			raw = "https://img1.kuwo.cn/star/albumcover/" + raw
		} else if host, _, ok := strings.Cut(raw, "/"); ok && strings.HasSuffix(host, ".kuwo.cn") {
			raw = "https://" + raw
		}
		if cover := publicCatalogArtwork(raw, "kuwo.cn"); cover != "" {
			return cover
		}
	}
	return ""
}

func miguCatalogCover(row map[string]any) string {
	images := asArray(row["imgItems"])
	candidates := []any{row["img1"], row["img"], row["imageUrl"], row["largePic"]}
	for _, image := range images {
		candidates = append(candidates, at(image, "img"), at(image, "imgUrl"))
	}
	for i, candidate := range candidates {
		if raw, ok := candidate.(string); ok {
			raw = strings.TrimSpace(raw)
			if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
				candidates[i] = "https://d.musicapp.migu.cn" + raw
			}
		}
	}
	return firstCatalogArtwork([]string{"migu.cn"}, candidates...)
}

func firstCatalogArtwork(httpsHosts []string, candidates ...any) string {
	for _, candidate := range candidates {
		raw, ok := candidate.(string)
		if !ok {
			continue
		}
		if cover := publicCatalogArtwork(raw, httpsHosts...); cover != "" {
			return cover
		}
	}
	return ""
}

func publicCatalogArtwork(raw string, httpsHosts ...string) string {
	cover := safeCatalogCover(strings.ReplaceAll(strings.TrimSpace(raw), "{size}", "400"))
	if cover == "" {
		return ""
	}
	u, _ := url.Parse(cover)
	host := strings.ToLower(u.Hostname())
	for _, domain := range httpsHosts {
		if u.Scheme == "http" && u.Port() == "" && (host == domain || strings.HasSuffix(host, "."+domain)) {
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
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
			if track.CoverURL == "" {
				track.CoverURL = detail.CoverURL
			}
			meta := asObject(track.MusicInfo["meta"])
			if len(asArray(meta["qualitys"])) == 0 {
				detailMeta := asObject(detail.MusicInfo["meta"])
				meta["qualitys"], meta["_qualitys"] = detailMeta["qualitys"], detailMeta["_qualitys"]
			}
			track.MusicInfo = sourceMusicInfo(track)
			page.Tracks[i] = track
		}
		missing = missing || page.Tracks[i].CoverURL == ""
	}
	if missing {
		if page.Warning != "" {
			page.Warning += "；"
		}
		page.Warning += "部分歌曲暂无封面"
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
