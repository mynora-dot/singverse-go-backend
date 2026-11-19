// Package qishui implements anonymous, read-only car catalogue requests in Go.
// Protocol adapted from GSQZ/qishui-music-api at 924add376cadf8a2268f11999051eb976e3b8e0d.
// See LICENSE.txt for the MIT notice.
package qishui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/querycache"
)

const (
	SearchTimeout = 15 * time.Second
	apiTimeout    = 5 * time.Second
	maxJSON       = 2_000_000
	prefix        = "/api/luna/v1/platform"
	userAgent     = "LunaOpenSDK/1.0.10"
)

// Failure contains controlled diagnostics; upstream bodies and tokens stay private.
type Failure struct {
	Status       int
	Stage        string
	Detail       string
	RetryAfter   time.Duration
	upstreamCode int64
}

func (f *Failure) Error() string   { return f.Detail }
func (f *Failure) StatusCode() int { return f.Status }

// Client shares an anonymous application token and request gate across song and
// artist searches. All mutable state is protected by permit, including refreshes.
type Client struct {
	api             *http.Client
	deviceID        string
	token           string
	permit          chan struct{}
	interval        time.Duration
	next            time.Time
	cooldown        time.Time
	cooldownFailure *Failure
	queryCache      querycache.Cache
}

func New(api *http.Client) (*Client, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, errors.New("cannot generate Qishui device identity")
	}
	deviceID := strconv.FormatUint(binary.BigEndian.Uint64(raw[:])%9_000_000_000_000_000_000+1_000_000_000_000_000_000, 10)
	if api == nil {
		api = &http.Client{Transport: &http.Transport{
			Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: apiTimeout, IdleConnTimeout: 30 * time.Second,
		}}
	}
	return &Client{api: api, deviceID: deviceID, permit: make(chan struct{}, 1), interval: 2 * time.Second}, nil
}

func (c *Client) Close() error {
	if c != nil {
		c.api.CloseIdleConnections()
	}
	return nil
}

func failure(stage, detail string) *Failure {
	return &Failure{Status: 503, Stage: stage, Detail: detail}
}

// Search reads the first catalogue page without fetching audio or signing URLs.
func (c *Client) Search(ctx context.Context, keyword, kind string) (map[string]any, error) {
	return c.SearchPage(ctx, keyword, kind, "0")
}

var numericID = regexp.MustCompile(`^[0-9]{1,30}$`)

// SearchPage preserves the catalogue's next_cursor, rather than guessing its batch size.
func (c *Client) SearchPage(ctx context.Context, keyword, kind, cursor string) (map[string]any, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || len(keyword) > 2000 || (kind != "track" && kind != "artist" && kind != "playlist") || !numericID.MatchString(cursor) {
		return nil, failure("request", "汽水音乐搜索参数无效")
	}
	return c.cached(ctx, querycache.Key("search", keyword, kind, cursor), 15*time.Minute, func(ctx context.Context) (map[string]any, error) {
		searchID := uuid.NewString()
		data, err := c.call(ctx, "search", "/search/", map[string]any{
			"q": keyword, "cursor": cursor, "search_id": searchID, "from_search_id": searchID,
			"search_method": "input", "search_type": kind, "search_scene": "normal", "scene_name": "search",
			"ab_param": "", "last_viewed_media": map[string]any{}, "common_params": map[string]any{},
		})
		if err != nil {
			return nil, err
		}
		if _, ok := data["result_groups"].([]any); !ok {
			return nil, failure("search", "汽水音乐搜索响应缺少候选分组")
		}
		return data, nil
	})
}

// Track and Playlist read anonymous public catalogue metadata only.
func (c *Client) Track(ctx context.Context, id string) (map[string]any, error) {
	if !numericID.MatchString(id) {
		return nil, failure("request", "汽水音乐歌曲编号无效")
	}
	return c.cached(ctx, querycache.Key("track", id), 6*time.Hour, func(ctx context.Context) (map[string]any, error) {
		data, err := c.call(ctx, "detail", "/media/mget/", map[string]any{"media_list": []any{map[string]any{"id": id, "type": "track"}}, "card_context": map[string]any{}})
		if err != nil {
			return nil, err
		}
		rows, ok := data["media_list"].([]any)
		if !ok {
			return nil, failure("detail", "汽水音乐未返回歌曲详情")
		}
		for _, raw := range rows {
			media, _ := raw.(map[string]any)
			track, _ := media["track_entity"].(map[string]any)
			base, _ := track["base_info"].(map[string]any)
			if fmt.Sprint(base["id"]) == id || fmt.Sprint(media["media_id"]) == id {
				return track, nil
			}
		}
		return nil, failure("detail", "汽水音乐未返回匹配的歌曲详情")
	})
}

func (c *Client) Playlist(ctx context.Context, id, cursor string) (map[string]any, error) {
	if !numericID.MatchString(id) || len(cursor) > 200 {
		return nil, failure("request", "汽水音乐歌单参数无效")
	}
	return c.cached(ctx, querycache.Key("playlist", id, cursor), 5*time.Minute, func(ctx context.Context) (map[string]any, error) {
		data, err := c.call(ctx, "playlist", "/playlist/detail/", map[string]any{"playlist_id": id, "owner_id": "", "count": 100, "cursor": cursor, "type": nil})
		if err != nil {
			return nil, err
		}
		if _, ok := data["media_entities"].([]any); !ok {
			return nil, failure("playlist", "汽水音乐未返回公开歌单歌曲")
		}
		return data, nil
	})
}

func (c *Client) cached(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) (map[string]any, error)) (map[string]any, error) {
	if c == nil {
		return nil, failure("client", "汽水音乐暂时不可用")
	}
	return querycache.Load(ctx, &c.queryCache, key, func(ctx context.Context) (map[string]any, time.Duration, error) {
		data, err := c.run(ctx, fn)
		if groups, ok := data["result_groups"].([]any); ok {
			empty := true
			for _, raw := range groups {
				group, _ := raw.(map[string]any)
				if rows, _ := group["data"].([]any); len(rows) > 0 {
					empty = false
				}
			}
			if empty {
				ttl = time.Minute
			}
		}
		return data, ttl, err
	})
}

func (c *Client) run(ctx context.Context, fn func(context.Context) (map[string]any, error)) (map[string]any, error) {
	if c == nil {
		return nil, failure("client", "汽水音乐暂时不可用")
	}
	ctx, cancel := context.WithTimeout(ctx, SearchTimeout)
	defer cancel()
	select {
	case c.permit <- struct{}{}:
	case <-ctx.Done():
		return nil, failure("queue", "汽水音乐请求等待超时或已取消")
	}
	defer func() { <-c.permit }()
	if ctx.Err() != nil {
		return nil, failure("queue", "汽水音乐请求等待超时或已取消")
	}
	if remaining := time.Until(c.cooldown); remaining > 0 {
		failed := *c.cooldownFailure
		failed.Stage, failed.RetryAfter = "cooldown", remaining
		return nil, &failed
	}
	if wait := time.Until(c.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, failure("queue", "汽水音乐请求等待超时或已取消")
		}
	}
	c.next = time.Now().Add(c.interval)
	root, err := fn(ctx)
	var failed *Failure
	if errors.As(err, &failed) && failed.RetryAfter > 0 {
		c.cooldown = time.Now().Add(failed.RetryAfter)
		copy := *failed
		c.cooldownFailure = &copy
	}
	return root, err
}

func (c *Client) call(ctx context.Context, stage, path string, input map[string]any) (map[string]any, error) {
	if input["common_params"] == nil {
		input["common_params"] = map[string]any{}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, failure("request", "汽水音乐请求无效")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if c.token == "" {
			root, err := c.request(ctx, "token", "https://open.qishui.com"+prefix+"/client/token/get/", nil, "")
			if err != nil {
				return nil, err
			}
			token, _ := root["client_token"].(string)
			if !validToken(token) {
				return nil, failure("token", "汽水音乐未返回有效的搜索凭证，请稍后重试")
			}
			c.token = token
		}
		root, err := c.request(ctx, stage, "https://open.douyin.com"+prefix+path, body, c.token)
		if err != nil {
			var failed *Failure
			if errors.As(err, &failed) && failed.upstreamCode == 28001008 {
				c.token = ""
				if attempt == 0 {
					continue
				}
			}
			return nil, err
		}
		data, ok := root["data"].(map[string]any)
		if !ok {
			return nil, failure(stage, "汽水音乐响应缺少数据")
		}
		return data, nil
	}
	return nil, failure(stage, "汽水音乐请求凭证已失效，请稍后重试")
}

func (c *Client) params() (url.Values, error) {
	var nonce [2]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, failure("request", "汽水音乐搜索请求初始化失败")
	}
	return url.Values{
		"aid": {"765213"}, "app_id": {"765213"}, "app_name": {"byte_autoservice_lite"},
		"version_code": {"100010"}, "version_name": {"1.0.10"}, "channel": {"bydSong"},
		"sdk_aid": {"753999"}, "caller_name": {"luna_open_sdk"}, "sdk_version_code": {"100000100"}, "sdk_version_name": {"0.0.1"}, "sdk_channel": {"Test"},
		"vehicle_type": {"BYD"}, "device_brand": {"BYD"}, "device_type": {"BYD"}, "device_platform": {"android"}, "resolution": {"1920x1080"}, "os_version": {"13"}, "ac": {"wifi"},
		"device_id": {c.deviceID}, "did": {c.deviceID}, "iid": {c.deviceID}, "vehicle_id": {"qishui-" + c.deviceID},
		"_timestamp": {strconv.FormatInt(time.Now().Unix(), 10)}, "_nonce": {strconv.Itoa(int(binary.BigEndian.Uint16(nonce[:])))},
	}, nil
}

func (c *Client) request(ctx context.Context, stage, endpoint string, body []byte, token string) (map[string]any, error) {
	params, err := c.params()
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
		params.Set("trigger_type", "cold_start")
	}
	callCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, method, endpoint+"?"+params.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, failure(stage, "汽水音乐请求无效")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Access-Token", token)
	}
	client := *c.api
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, failure(stage, "汽水音乐连接失败或请求超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &Failure{Status: 429, Stage: stage, Detail: "汽水音乐返回限流，请稍后重试", RetryAfter: max(time.Minute, retryAfter(resp.Header, time.Minute))}
	}
	if resp.StatusCode != http.StatusOK {
		failed := failure(stage, fmt.Sprintf("汽水音乐返回 HTTP %d，请稍后重试或改用其他平台", resp.StatusCode))
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") != "" {
			failed.RetryAfter = retryAfter(resp.Header, time.Second)
		}
		return nil, failed
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil || len(data) > maxJSON {
		return nil, failure(stage, "汽水音乐响应无效或过大")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, failure(stage, "汽水音乐未返回搜索数据，请稍后重试或改用其他平台")
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Music and artist IDs exceed the exact integer range of float64.
	var extra any
	if decoder.Decode(&root) != nil || root == nil || decoder.Decode(&extra) != io.EOF {
		return nil, failure(stage, "汽水音乐返回了无效的搜索数据")
	}
	for _, code := range []any{root["err_no"], object(root["data"])["error_code"]} {
		if code == nil {
			continue
		}
		number, err := strconv.ParseInt(fmt.Sprint(code), 10, 64)
		if err != nil {
			return nil, failure(stage, "汽水音乐返回了无效的状态码")
		}
		if number != 0 {
			failed := failure(stage, "汽水音乐拒绝搜索请求，请稍后重试或改用其他平台")
			failed.upstreamCode = number
			return nil, failed
		}
	}
	return root, nil
}

func validToken(token string) bool {
	if token == "" || len(token) > 16384 {
		return false
	}
	for _, char := range token {
		if char < 33 || char > 126 {
			return false
		}
	}
	return true
}

func retryAfter(headers http.Header, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && time.Until(date) > 0 {
		return min(24*time.Hour, time.Until(date))
	}
	return fallback
}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
