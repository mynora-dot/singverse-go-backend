package metadata

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (p *musicProvider) searchMigu(ctx context.Context, keyword string, limit int) ([]Track, error) {
	q := url.Values{"text": {keyword}, "pageNo": {"1"}, "pageSize": {strconv.Itoa(limit)}, "searchSwitch": {`{"song":1}`}}
	root, err := p.request(ctx, "GET", "https://app.c.nf.migu.cn/MIGUM2.0/v1.0/content/search_all.do?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	if text(root["code"]) != "000000" {
		return nil, &problem{503, "咪咕音乐搜索暂时不可用"}
	}
	data, ok := root["songResultData"].(map[string]any)
	if !ok {
		data, ok = path(root, "data", "songResultData").(map[string]any)
	}
	if !ok || data["totalCount"] == nil {
		return nil, &problem{503, "咪咕音乐搜索响应无效"}
	}
	var rows []any
	for _, field := range []string{"result", "resultList", "songList"} {
		if value, exists := data[field]; exists {
			var valid bool
			rows, valid = value.([]any)
			if !valid {
				return nil, &problem{503, "咪咕音乐搜索响应无效"}
			}
			break
		}
	}
	if rows == nil && number(data["totalCount"]) != 0 {
		return nil, &problem{503, "咪咕音乐搜索响应无效"}
	}
	flat := []any{}
	for _, row := range rows {
		if nested, ok := row.([]any); ok {
			flat = append(flat, nested...)
		} else {
			flat = append(flat, row)
		}
	}
	return parseMigu(flat, limit), nil
}

func miguAlbumCover(row map[string]any) *string {
	check := func(raw *string) *string {
		if raw != nil && strings.HasPrefix(*raw, "/") && !strings.HasPrefix(*raw, "//") {
			raw = ptr("https://d.musicapp.migu.cn" + *raw)
		}
		return checkedCover("MIGU", raw)
	}
	// The public catalogue supplies 200/400/800 px files with independent URLs.
	// Keep the largest supplied album image; never invent a CDN resize URL or
	// substitute singer/MV artwork. Both quality options retain this file.
	images := array(row["imgItems"])
	for _, size := range []string{"03", "02", "01"} {
		for _, raw := range images {
			image, _ := raw.(map[string]any)
			if text(image["imgSizeType"]) == size {
				if cover := check(first(image, "img", "imgUrl")); cover != nil {
					return cover
				}
			}
		}
	}
	if cover := check(first(row, "img3", "img2", "img1", "img", "imageUrl", "largePic")); cover != nil {
		return cover
	}
	for _, raw := range images {
		image, _ := raw.(map[string]any)
		if cover := check(first(image, "img", "imgUrl")); cover != nil {
			return cover
		}
	}
	return nil
}

func parseMigu(rows []any, limit int) []Track {
	out := []Track{}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := first(row, "songId", "id")
		if id == nil || !publicMusicID.MatchString(*id) {
			continue
		}
		singers := row["singers"]
		if singers == nil {
			singers = row["singerList"]
		}
		album := publicText(row, "albumName", "album")
		if album == nil && len(array(row["albums"])) > 0 {
			firstAlbum, _ := array(row["albums"])[0].(map[string]any)
			album = publicText(firstAlbum, "name")
		}
		var duration *int
		if rawDuration := text(row["duration"]); rawDuration != "" {
			seconds := number(row["duration"])
			if parts := strings.Split(rawDuration, ":"); len(parts) == 2 {
				minutes, e1 := strconv.Atoi(parts[0])
				remainder, e2 := strconv.Atoi(parts[1])
				seconds = 0
				if e1 == nil && e2 == nil && minutes >= 0 && minutes < 1440 && remainder >= 0 && remainder < 60 {
					seconds = int64(minutes*60 + remainder)
				}
			}
			if seconds > 0 && seconds <= 86400 {
				duration = ptr(int(seconds * 1000))
			}
		}
		var release *string
		if value := publicText(row, "publishDate", "releaseDate"); value != nil && len(*value) >= 10 {
			if _, err := time.Parse("2006-01-02", (*value)[:10]); err == nil {
				release = ptr((*value)[:10])
			}
		}
		aliases := []string{}
		for _, field := range []string{"songAliasName", "translateName"} {
			if value := publicText(row, field); value != nil && !contains(aliases, *value) {
				aliases = append(aliases, *value)
			}
		}
		t := track("MIGU", id, publicText(row, "songName", "name"), names(singers, "name"), album, duration, release, aliases, miguAlbumCover(row), "UNKNOWN")
		if t != nil {
			t.ArtistRefs = refs(singers, "MIGU", "id")
			out = append(out, *t)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}
