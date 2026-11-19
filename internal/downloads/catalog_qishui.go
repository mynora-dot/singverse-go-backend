package downloads

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/qishui"
)

// Route catalogue requests through the same bounded, public-only network boundary.
type qishuiTransport struct{ network catalogNetwork }

func (t qishuiTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, 64*1024))
		if err != nil {
			return nil, err
		}
	}
	headers := map[string]string{}
	for k := range req.Header {
		headers[k] = req.Header.Get(k)
	}
	r, err := t.network.Request(req.Context(), req.URL.String(), req.Method, headers, body, 2_000_000, true)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	for k, v := range r.Headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: r.Status, Header: h, Body: io.NopCloser(bytes.NewReader(r.Bytes)), Request: req}, nil
}

type qishuiReader interface {
	SearchPage(context.Context, string, string, string) (map[string]any, error)
	Track(context.Context, string) (map[string]any, error)
	Playlist(context.Context, string, string) (map[string]any, error)
}
type qishuiSearchPages struct {
	at      time.Time
	cursors map[int]string
	permit  chan struct{}
}
type qishuiCatalog struct {
	client qishuiReader
	mu     sync.Mutex
	pages  map[string]*qishuiSearchPages
}

func (c *publicCatalog) qishui() (*qishuiCatalog, error) {
	c.qsOnce.Do(func() {
		client, err := qishui.New(&http.Client{Transport: qishuiTransport{c.network}})
		if err != nil {
			c.qsErr = unavailable("汽水音乐初始化失败，请稍后重试")
			return
		}
		c.qs = &qishuiCatalog{client: client, pages: map[string]*qishuiSearchPages{}}
	})
	return c.qs, c.qsErr
}
func qishuiTrack(raw any) map[string]any {
	row := asObject(raw)
	for _, value := range []any{row["track_entity"], at(row, "entity", "media", "track_entity"), at(row, "entity", "track"), at(row, "entity", "track_wrapper", "track"), row["track"]} {
		if m, ok := value.(map[string]any); ok {
			return m
		}
	}
	return row
}
func (c *publicCatalog) searchQishui(ctx context.Context, query string, page int) (CatalogPage, error) {
	q, err := c.qishui()
	if err != nil {
		return CatalogPage{}, err
	}
	// Cache opaque upstream cursors for subsequent pages, bounded by TTL and query count.
	q.mu.Lock()
	for key, entry := range q.pages {
		if time.Since(entry.at) > 5*time.Minute {
			delete(q.pages, key)
		}
	}
	entry := q.pages[query]
	if entry == nil || page == 1 {
		if len(q.pages) >= 32 {
			for key := range q.pages {
				delete(q.pages, key)
				break
			}
		}
		entry = &qishuiSearchPages{at: time.Now(), cursors: map[int]string{1: "0"}, permit: make(chan struct{}, 1)}
		q.pages[query] = entry
	}
	q.mu.Unlock()
	// Cursor coordination belongs to this query, not every Qishui search. A
	// different query can now use its cached page without waiting for this one.
	select {
	case entry.permit <- struct{}{}:
	case <-ctx.Done():
		return CatalogPage{}, ctx.Err()
	}
	defer func() { <-entry.permit }()
	cursor := "0"
	start := 1
	for n, mark := range entry.cursors {
		if n <= page && n > start {
			start, cursor = n, mark
		}
	}
	seen := map[string]bool{}
	for n := start; n <= page; n++ {
		if seen[cursor] {
			return CatalogPage{}, unavailable("汽水音乐未返回新的搜索页，请重新搜索")
		}
		seen[cursor] = true
		data, err := q.client.SearchPage(ctx, query, "track", cursor)
		if err != nil {
			return CatalogPage{}, err
		}
		var group map[string]any
		for _, raw := range asArray(data["result_groups"]) {
			g := asObject(raw)
			if catalogText(g, "id") == "tracks" {
				group = g
				break
			}
			if group == nil {
				group = g
			}
		}
		rows := []any{}
		for _, raw := range asArray(group["data"]) {
			row := qishuiTrack(raw)
			if row["base_info"] != nil || row["id"] != nil {
				rows = append(rows, row)
			}
		}
		more, _ := group["has_more"].(bool)
		next := catalogText(group, "next_cursor")
		if more && (next == "" || next == cursor || seen[next] || !catalogDigits.MatchString(next)) {
			return CatalogPage{}, unavailable("汽水音乐未返回新的搜索页，请重新搜索")
		}
		if more {
			entry.cursors[n+1] = next
		}
		if n == page {
			return catalogPage("qs", rows, int64(len(rows)), more && len(rows) > 0, "")
		}
		if !more || len(rows) == 0 {
			return CatalogPage{Tracks: []Track{}}, nil
		}
		cursor = next
	}
	return CatalogPage{}, unavailable("汽水音乐搜索页无效")
}

var qishuiImageURI = regexp.MustCompile(`^[A-Za-z0-9_/.-]{1,500}$`)
var qishuiImageTemplate = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func qishuiCover(raw any) string {
	image := asObject(raw)
	for _, v := range asArray(image["urls"]) {
		address := valueText(v)
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !contains([]string{"p3-luna.douyinpic.com", "p6-luna.douyinpic.com"}, u.Hostname()) {
			continue
		}
		if u.Path == "/img/" && u.RawQuery == "" {
			uri, template := catalogText(image, "uri"), catalogText(image, "template_prefix")
			if qishuiImageURI.MatchString(uri) && !strings.Contains(uri, "..") && qishuiImageTemplate.MatchString(template) {
				return address + uri + "~" + template + "-image.image"
			}
			continue
		}
		if strings.HasPrefix(u.Path, "/img/") {
			return safeCatalogCover(address)
		}
	}
	return ""
}
func (c *publicCatalog) qishuiDetail(ctx context.Context, track Track) (Track, error) {
	q, err := c.qishui()
	if err != nil {
		return Track{}, err
	}
	row, err := q.client.Track(ctx, track.RemoteID)
	if err != nil {
		return Track{}, err
	}
	next, err := mapCatalogTrack("qs", row)
	if err != nil || next.RemoteID != track.RemoteID {
		return Track{}, unavailable("汽水音乐未返回匹配的歌曲详情，请重新搜索")
	}
	return next, nil
}
func (c *publicCatalog) qishuiCollection(ctx context.Context, ref CatalogReference, page int) (Collection, error) {
	if ref.Kind != "playlist" {
		return Collection{}, bad("汽水音乐暂不提供公开专辑导入，请搜索歌曲或使用公开歌单链接")
	}
	q, err := c.qishui()
	if err != nil {
		return Collection{}, err
	}
	start, end := (page-1)*20, page*20
	cursor := "0"
	seen := map[string]bool{}
	consumed := 0
	rows := []any{}
	name, cover := "", ""
	total := int64(0)
	more := false
	for calls := 0; calls < 100; calls++ {
		seen[cursor] = true
		data, err := q.client.Playlist(ctx, ref.ID, cursor)
		if err != nil {
			return Collection{}, err
		}
		info := asObject(data["playlist"])
		if name == "" {
			name = cleanCatalog(catalogText(info["base_info"], "title", "name"))
			if name == "" {
				name = cleanCatalog(catalogText(info, "title", "name"))
			}
			cover = qishuiCover(first(info["display_info"], "cover_url", "cover"))
			total = integer(first(info["count_info"], "count_tracks", "track_count"), integer(first(info, "track_count", "count_tracks"), 0))
		}
		batch, ok := data["media_entities"].([]any)
		if !ok {
			return Collection{}, unavailable("汽水音乐未返回歌单歌曲，请检查歌单是否公开")
		}
		for i, raw := range batch {
			pos := consumed + i
			if pos >= start && pos < end {
				rows = append(rows, qishuiTrack(raw))
			}
		}
		consumed += len(batch)
		upstreamMore, _ := data["has_more"].(bool)
		next := catalogText(data, "next_cursor")
		if upstreamMore && (len(batch) == 0 || next == "" || seen[next]) {
			return Collection{}, unavailable("汽水音乐未返回新的歌单页，已停止读取")
		}
		more = consumed > end || upstreamMore
		if consumed >= end || !upstreamMore {
			break
		}
		cursor = next
		if calls == 99 {
			return Collection{}, unavailable("汽水歌单过大，请拆分后导入")
		}
	}
	if name == "" {
		return Collection{}, unavailable("汽水音乐未返回歌单名称，请检查歌单是否公开")
	}
	parsed, err := catalogPage("qs", rows, int64(len(rows)), more, "")
	if err != nil {
		return Collection{}, err
	}
	if total < int64(consumed) {
		total = int64(consumed)
	}
	warning := parsed.Warning
	if more && total == int64(consumed) {
		warning = strings.TrimSpace(warning + " 平台未公开完整数量，当前总数为已读取范围")
	}
	return Collection{Platform: "qs", Kind: "playlist", Name: name, CoverURL: cover, Tracks: parsed.Tracks, Page: page, Total: total, HasMore: more, Warning: warning}, nil
}

var qishuiLineTiming = regexp.MustCompile(`^\[([0-9]+),[0-9]+\](.*)$`)
var qishuiWordTiming = regexp.MustCompile(`<[-0-9]+,[0-9]+,[0-9]+>`)
var qishuiLRCTiming = regexp.MustCompile(`(?m)^\[[0-9]{2,}:[0-9]{2}(?:\.[0-9]+)?\]`)

func qishuiLRC(content string) string {
	if len(content) > 256*1024 {
		return ""
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		match := qishuiLineTiming.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		millis, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || millis < 0 || millis > 604800000 {
			continue
		}
		text := strings.TrimSpace(qishuiWordTiming.ReplaceAllString(match[2], ""))
		if text == "" {
			continue
		}
		fmt.Fprintf(&out, "[%02d:%02d.%02d]%s\n", millis/60000, millis/1000%60, millis/10%100, text)
	}
	if out.Len() > 0 {
		return out.String()
	}
	if qishuiLRCTiming.MatchString(content) {
		return content
	}
	return ""
}
func (c *publicCatalog) qishuiLyrics(ctx context.Context, track Track) (string, error) {
	q, err := c.qishui()
	if err != nil {
		return "", err
	}
	row, err := q.client.Track(ctx, track.RemoteID)
	if err != nil {
		return "", err
	}
	return qishuiLRC(catalogText(at(row, "display_info", "lyric_info", "lyric_entity"), "content")), nil
}
